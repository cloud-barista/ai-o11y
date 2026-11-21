// Command ai-o11y-server serves the AI-O11y monitoring control plane: it lists
// what to monitor, renders the configuration of the collectors that monitor it,
// and answers queries against the metrics backend. Samples go from the
// collectors to the backend and never through this service.
package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/ai-o11y/internal/config"
	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/backend/promapi"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/backend/thanos"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/backend/victoria"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/collector"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/mapping"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/query"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/source/tumblebug"
	"github.com/cloud-barista/ai-o11y/internal/rest"
)

const shutdownTimeout = 10 * time.Second

// @title AI-MCMP AI-O11y Service (Prototype)
// @version 0.1.0
// @description Monitoring control plane for AI-MCMP: monitoring targets, collector configuration and metric queries over a Prometheus-family backend.
// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html
// @BasePath /aio11y
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description "Bearer <token>". Required on every route but readiness and this documentation when AIO11Y_AUTH_TOKEN is set.
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Error().Err(err).Msg("Failed to load configuration")
		os.Exit(1)
	}
	if cfg.Auth.Token == "" {
		log.Warn().Msg("AIO11Y_AUTH_TOKEN is not set: every route is open")
	}

	sources, err := newTargetSources(cfg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to register target sources")
		os.Exit(1)
	}
	log.Info().Strs("targetSources", sources.Names()).Msg("Target sources registered")

	deps, err := newDeps(cfg, sources)
	if err != nil {
		log.Error().Err(err).Msg("Failed to set up monitoring")
		os.Exit(1)
	}

	server := rest.NewServer(cfg, deps)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start()
	}()
	rest.MarkReady()
	log.Info().Int("port", server.Port()).Str("basePath", rest.BasePath).Msg("AI-O11y server started")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil {
			log.Error().Err(err).Msg("Server stopped")
			os.Exit(1)
		}
	case sig := <-stop:
		log.Info().Str("signal", sig.String()).Msg("Shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("Failed to shut down cleanly")
		os.Exit(1)
	}
}

// newTargetSources registers the target source providers the configuration
// enables. A provider without its settings is left out rather than failing
// startup, so the service runs with whatever inventories are reachable.
func newTargetSources(cfg *config.Config) (*monitoring.Registry[monitoring.TargetSource], error) {
	sources := monitoring.NewRegistry[monitoring.TargetSource]("target source")
	if cfg.Tumblebug.BaseURL == "" {
		log.Warn().Msg("AIO11Y_TUMBLEBUG_BASE_URL is not set: CB-Tumblebug nodes are not listed")
		return sources, nil
	}
	client := tumblebug.NewClient(tumblebug.Config{
		BaseURL:          cfg.Tumblebug.BaseURL,
		Username:         cfg.Tumblebug.Username,
		Password:         cfg.Tumblebug.Password,
		CredentialHolder: cfg.Tumblebug.CredentialHolder,
		Timeout:          cfg.Tumblebug.Timeout,
	})
	if err := sources.Register(tumblebug.ProviderName, tumblebug.NewSource(client)); err != nil {
		return nil, err
	}
	return sources, nil
}

// newDeps builds what the monitoring routes run on from what is configured:
// the target table needs the database, syncing it also the management
// platform, and queries and collector configuration the metrics backend.
func newDeps(cfg *config.Config, sources *monitoring.Registry[monitoring.TargetSource]) (rest.Deps, error) {
	cat, err := catalog.New(catalog.Builtin())
	if err != nil {
		return rest.Deps{}, fmt.Errorf("built-in metric catalog: %w", err)
	}
	access, err := writeAccess(cfg.Backend)
	if err != nil {
		return rest.Deps{}, err
	}
	deps := rest.Deps{
		Catalog:    cat,
		Collectors: []monitoring.CollectorKind{collector.Hostmetrics{}, collector.NvidiaGPU{}, collector.AMDGPU{}},
		CollectorOptions: collector.Options{
			TelemetryPort:   cfg.Collector.TelemetryPort,
			StorageDir:      cfg.Collector.StorageDir,
			WriteMode:       collector.WriteMode(cfg.Backend.WriteMode),
			LogsEndpoints:   writeEndpoints(cfg.Backend.LogsWriteURLs, access),
			TracesEndpoints: writeEndpoints(cfg.Backend.TracesWriteURLs, access),
		},
	}
	if src, err := sources.Get(tumblebug.ProviderName); err == nil {
		deps.Source = src
	}

	if cfg.DB.Host == "" {
		log.Warn().Msg("AIO11Y_DB_HOST is not set: target routes and queries are unavailable")
	} else {
		db, err := mapping.OpenDB(mapping.DBConfig{
			Host: cfg.DB.Host, Port: cfg.DB.Port, Name: cfg.DB.Name, User: cfg.DB.User, Password: cfg.DB.Password,
			ConnectTimeout: cfg.DB.ConnectTimeout, ReadTimeout: cfg.DB.ReadTimeout, WriteTimeout: cfg.DB.WriteTimeout,
		})
		if err != nil {
			return rest.Deps{}, err
		}
		store := mapping.NewStore(db, tumblebug.ProviderName)
		deps.Targets = store
		if deps.Source != nil {
			deps.Syncer = mapping.NewSyncer(store, deps.Source, mapping.DefaultSyncPause)
		}
	}

	b, err := newBackend(cfg, query.PromQL{Catalog: cat}, access)
	if err != nil {
		return rest.Deps{}, err
	}
	if b != nil {
		deps.Backend = b
	}
	return deps, nil
}

// newBackend builds the metrics backend AIO11Y_BACKEND_PROVIDER selects, its
// write addresses written to as access says (see writeAccess). It returns nil
// when no query URL is set, leaving queries and collector configuration
// unavailable.
func newBackend(cfg *config.Config, renderer query.PromQL, access monitoring.Endpoint) (monitoring.Backend, error) {
	bc := cfg.Backend
	if len(bc.QueryURLs) == 0 {
		log.Warn().Msg("AIO11Y_BACKEND_QUERY_URL is not set: queries and collector configuration are unavailable")
		return nil, nil
	}
	if len(bc.WriteURLs) == 0 {
		log.Warn().Msg("AIO11Y_BACKEND_WRITE_URL is not set: collector configuration is unavailable")
	}
	write := writeEndpoints(bc.WriteURLs, access)
	limits := query.ChunkLimits{MaxIDs: bc.MaxIDs, MaxConcurrent: bc.MaxConcurrent}
	var headers map[string]string
	switch {
	case bc.QueryUsername != "":
		headers = map[string]string{"Authorization": basicAuth(bc.QueryUsername, bc.QueryPassword)}
	case bc.QueryBearerToken != "":
		headers = map[string]string{"Authorization": "Bearer " + bc.QueryBearerToken}
	}
	tlsCfg := promapi.TLS{CAFile: bc.QueryTLSCAFile, InsecureSkipVerify: bc.QueryTLSInsecureSkipVerify}
	if tlsCfg.InsecureSkipVerify {
		log.Warn().Msg("AIO11Y_BACKEND_QUERY_TLS_INSECURE_SKIP_VERIFY is on: query addresses' certificates are not verified")
	}
	switch bc.Provider {
	case config.BackendVictoria:
		return victoria.New(victoria.Config{
			QueryURLs: bc.QueryURLs, Write: write, Timeout: bc.Timeout, ConnectTimeout: bc.ConnectTimeout, Limits: limits, Headers: headers,
			TLS: tlsCfg, PartialResponse: bc.PartialResponse,
		}, renderer)
	case config.BackendThanos:
		return thanos.New(thanos.Config{
			QueryURLs: bc.QueryURLs, Write: write, Timeout: bc.Timeout, ConnectTimeout: bc.ConnectTimeout, Limits: limits, Headers: headers,
			TLS: tlsCfg, PartialResponse: bc.PartialResponse,
		}, renderer)
	default:
		return nil, fmt.Errorf("unknown metrics backend %q", bc.Provider)
	}
}

// writeEndpoints are the write addresses urls, each written to as access
// says.
func writeEndpoints(urls []string, access monitoring.Endpoint) []monitoring.Endpoint {
	write := make([]monitoring.Endpoint, len(urls))
	for i, u := range urls {
		write[i] = access
		write[i].URL = u
	}
	return write
}

// writeAccess is how collectors write to every write address, of every
// signal, with no URL set: the write account's username or a bearer token,
// whose password or token is read on the collector's host, and the CA the
// addresses are verified against. The CA file is read here, at startup, and
// rendered into each collector configuration; one that cannot be read or holds
// no PEM certificate is an error.
func writeAccess(bc config.BackendConfig) (monitoring.Endpoint, error) {
	var access monitoring.Endpoint
	switch {
	case bc.WriteUsername != "" && bc.WriteBearer:
		return access, errors.New("AIO11Y_BACKEND_WRITE_USERNAME and _WRITE_BEARER cannot be set together")
	case bc.WriteUsername != "":
		access.BasicAuth = &monitoring.BasicAuth{Username: bc.WriteUsername, PasswordEnv: collector.WritePasswordEnv}
	case bc.WriteBearer:
		access.BearerTokenEnv = collector.WriteTokenEnv
	}
	if bc.WriteTLSCAFile == "" && !bc.WriteTLSInsecureSkipVerify {
		return access, nil
	}
	access.TLS = &monitoring.EndpointTLS{InsecureSkipVerify: bc.WriteTLSInsecureSkipVerify}
	if bc.WriteTLSInsecureSkipVerify {
		log.Warn().Msg("AIO11Y_BACKEND_WRITE_TLS_INSECURE_SKIP_VERIFY is on: collectors do not verify write addresses' certificates")
	}
	if bc.WriteTLSCAFile != "" {
		pem, err := os.ReadFile(bc.WriteTLSCAFile)
		if err != nil {
			return access, fmt.Errorf("failed to read write CA file: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return access, fmt.Errorf("write CA file %s holds no PEM certificate", bc.WriteTLSCAFile)
		}
		access.TLS.CAPEM = string(pem)
	}
	return access, nil
}

// basicAuth is the Authorization header value of a basic auth account.
func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}
