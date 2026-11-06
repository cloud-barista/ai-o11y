// Package tumblebug is the TargetSource provider for CB-Tumblebug: it lists the
// nodes CB-Tumblebug runs as monitoring targets.
package tumblebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	headerCredentialHolder = "x-credential-holder"
	maxErrorBodyBytes      = 4 << 10
	// maxRetries is how many times a request CB-Tumblebug rate-limited (429)
	// is sent again, waiting longer each time.
	maxRetries       = 4
	defaultRetryBase = 500 * time.Millisecond
)

// Config is where CB-Tumblebug is reached and how.
type Config struct {
	// BaseURL includes the /tumblebug prefix, such as
	// "http://localhost:1323/tumblebug".
	BaseURL          string
	Username         string
	Password         string
	CredentialHolder string
	Timeout          time.Duration
}

// Client calls the CB-Tumblebug REST API. Only the read calls monitoring needs
// are here.
type Client struct {
	baseURL          string
	username         string
	password         string
	credentialHolder string
	httpClient       *http.Client
	// retryBase is the first wait after a 429; each retry doubles it unless
	// CB-Tumblebug names a wait in Retry-After.
	retryBase time.Duration
}

// NewClient builds a Client from configuration.
func NewClient(cfg Config) *Client {
	return &Client{
		baseURL:          strings.TrimRight(cfg.BaseURL, "/"),
		username:         cfg.Username,
		password:         cfg.Password,
		credentialHolder: cfg.CredentialHolder,
		httpClient:       &http.Client{Timeout: cfg.Timeout},
		retryBase:        defaultRetryBase,
	}
}

// APIError is a non-2xx response from CB-Tumblebug.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tumblebug responded %d: %s", e.StatusCode, e.Message)
}

// StatusCode reports the upstream HTTP status for err, or 0 when err is not an APIError.
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// get sends a GET request to CB-Tumblebug and decodes the JSON response into
// out. A 429 is retried up to maxRetries times with a growing wait; the last
// 429 is returned as an APIError.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	wait := c.retryBase
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.getOnce(ctx, path, query, out)
		if StatusCode(err) != http.StatusTooManyRequests || attempt == maxRetries {
			return err
		}
		if retryAfter > 0 {
			wait = retryAfter
		}
		log.Debug().Str("path", path).Dur("wait", wait).Int("retry", attempt+1).Msg("CB-Tumblebug rate-limited the request")
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		wait *= 2
	}
}

// getOnce sends one GET request. On a 429 it also returns the wait
// CB-Tumblebug named in Retry-After, if any.
func (c *Client) getOnce(ctx context.Context, path string, query url.Values, out any) (time.Duration, error) {
	endpoint := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.username, c.password)
	if c.credentialHolder != "" {
		req.Header.Set(headerCredentialHolder, c.credentialHolder)
	}

	// Path only: query values may carry operational detail.
	log.Debug().Str("path", path).Msg("Calling CB-Tumblebug")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to call tumblebug GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var retryAfter time.Duration
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			retryAfter = time.Duration(secs) * time.Second
		}
		return retryAfter, &APIError{StatusCode: resp.StatusCode, Message: readErrorMessage(resp.Body)}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, fmt.Errorf("failed to decode tumblebug response for GET %s: %w", path, err)
	}
	return 0, nil
}

// readErrorMessage extracts the Tumblebug SimpleMsg message, falling back to raw text.
func readErrorMessage(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	if err != nil {
		return "no response body"
	}
	var simple struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &simple) == nil && simple.Message != "" {
		return simple.Message
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "empty response body"
	}
	return trimmed
}

// namespace is the part of a CB-Tumblebug namespace monitoring reads.
type namespace struct {
	ID string `json:"id"`
}

// node is the part of a CB-Tumblebug node (model.NodeInfo) monitoring reads.
// Only these fields are decoded, so fields CB-Tumblebug adds or renames
// elsewhere do not matter.
type node struct {
	InfraID          string `json:"infraId"`
	ID               string `json:"id"`
	UID              string `json:"uid"`
	Name             string `json:"name"`
	CspResourceID    string `json:"cspResourceId"`
	ConnectionName   string `json:"connectionName"`
	ConnectionConfig struct {
		ProviderName string `json:"providerName"`
	} `json:"connectionConfig"`
	Region struct {
		Region string `json:"region"`
		Zone   string `json:"zone"`
	} `json:"region"`
	Spec struct {
		AcceleratorType  string `json:"acceleratorType"`
		AcceleratorModel string `json:"acceleratorModel"`
		AcceleratorCount int    `json:"acceleratorCount"`
	} `json:"spec"`
	Image struct {
		OSType string `json:"osType"`
	} `json:"image"`
}

// listNamespaces returns every namespace (GET /ns).
func (c *Client) listNamespaces(ctx context.Context) ([]namespace, error) {
	var out struct {
		Ns []namespace `json:"ns"`
	}
	if err := c.get(ctx, "/ns", nil, &out); err != nil {
		return nil, err
	}
	return out.Ns, nil
}

// listNodes returns every node in a namespace across its infras
// (GET /ns/{nsId}/resources/node).
func (c *Client) listNodes(ctx context.Context, nsID string) ([]node, error) {
	var out struct {
		Node []node `json:"node"`
	}
	path := fmt.Sprintf("/ns/%s/resources/node", url.PathEscape(nsID))
	if err := c.get(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Node, nil
}

// getNode returns one node (GET /ns/{nsId}/infra/{infraId}/node/{nodeId}).
func (c *Client) getNode(ctx context.Context, nsID, infraID, nodeID string) (node, error) {
	var out node
	path := fmt.Sprintf("/ns/%s/infra/%s/node/%s",
		url.PathEscape(nsID), url.PathEscape(infraID), url.PathEscape(nodeID))
	err := c.get(ctx, path, nil, &out)
	return out, err
}
