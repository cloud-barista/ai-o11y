// Command ai-o11y-server serves the AI-O11y monitoring control plane: it lists
// what to monitor, renders the configuration of the collectors that monitor it,
// and answers queries against the metrics backend. Samples go from the
// collectors to the backend and never through this service.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/ai-o11y/internal/config"
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

	server := rest.NewServer(cfg)

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
