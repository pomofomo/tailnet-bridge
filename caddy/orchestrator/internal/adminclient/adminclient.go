// Package adminclient is a thin client for Caddy's admin API.
//
// We only need /load. Validation errors are returned verbatim so callers
// can surface them; we never retry rapidly on a 4xx (SPEC §8.3).
package adminclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client targets a single Caddy admin endpoint.
type Client struct {
	// Addr is the host:port of the admin endpoint (e.g. "127.0.0.1:2019").
	Addr string
	// HTTP overrides the underlying client; nil means a sensible default.
	HTTP *http.Client
}

// Load POSTs jsonConfig to http://<addr>/load.
//
// On non-2xx the response body (Caddy's validation message) is returned
// verbatim wrapped in an error.
func (c *Client) Load(ctx context.Context, jsonConfig []byte) error {
	if c.Addr == "" {
		return fmt.Errorf("adminclient: empty Addr")
	}
	url := "http://" + c.Addr + "/load"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonConfig))
	if err != nil {
		return fmt.Errorf("adminclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("adminclient: POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	return fmt.Errorf("adminclient: POST %s: status %d: %s",
		url, resp.StatusCode, strings.TrimSpace(string(body)))
}
