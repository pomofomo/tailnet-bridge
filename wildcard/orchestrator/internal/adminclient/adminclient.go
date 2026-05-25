// Package adminclient POSTs configurations to Caddy's admin API.
// See https://caddyserver.com/docs/api.
//
// Status: STUB.
package adminclient

import (
	"context"
	"errors"
)

// Client is the minimal surface the rest of the orchestrator depends on.
// Defined as an interface so tests can substitute a fake.
type Client interface {
	// Load POSTs the provided JSON config to <addr>/load. On a non-2xx
	// response, returns an error containing Caddy's response body
	// verbatim (Caddy's error messages are useful — don't swallow them).
	Load(ctx context.Context, jsonConfig []byte) error
}

// HTTP is the production Client backed by net/http. Addr is the
// host:port the Caddy admin API listens on (default 127.0.0.1:2019).
type HTTP struct {
	Addr string
}

// New returns an HTTP client bound to the given admin address.
func New(addr string) *HTTP { return &HTTP{Addr: addr} }

// Load implements Client.
func (h *HTTP) Load(ctx context.Context, jsonConfig []byte) error {
	// TODO(impl):
	//   1. http.NewRequestWithContext(ctx, "POST",
	//      "http://"+h.Addr+"/load", bytes.NewReader(jsonConfig)).
	//   2. Content-Type: application/json.
	//   3. Use a short timeout (e.g. 30s) on the HTTP client.
	//   4. On non-2xx, read the body and wrap it into an error.
	_, _ = ctx, jsonConfig
	return errNotImplemented
}

var errNotImplemented = errors.New("adminclient: not yet implemented")
