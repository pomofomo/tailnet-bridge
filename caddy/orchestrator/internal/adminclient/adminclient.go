// Package adminclient is a thin client for Caddy's admin API.
//
// We only need /load. Validation errors are returned verbatim so callers
// can surface them; we never retry rapidly on a 4xx (SPEC §8.3).
package adminclient

import "context"

// Client targets a single Caddy admin endpoint.
type Client struct {
	Addr string // e.g. "127.0.0.1:2019"
}

// Load POSTs the JSON config to http://<addr>/load.
//
// TODO: set Content-Type: application/json, surface non-2xx body in the
// returned error.
func (c *Client) Load(ctx context.Context, jsonConfig []byte) error {
	return nil
}
