// Package status serves /__bridge_error (HTML, rendered for Caddy's
// handle_errors upstream) and /__bridge_status (JSON, for the user's own
// debugging). Bound to 127.0.0.1:<port>; never reachable from outside the
// container.
//
// SPEC §9.2, §9.3.
package status

import (
	"bridge/internal/health"
	"context"
)

// Server is the HTTP server.
type Server struct {
	Addr   string
	Health *health.Store
	// CommunityIDByPrefix maps a personal-side hostname prefix back to a
	// community ID, so /__bridge_error can identify which community the
	// failed request was for. Populated by the poller after each successful
	// directory merge.
	CommunityIDByPrefix map[string]string
}

// Run blocks until ctx is cancelled.
//
// TODO:
//   - GET /__bridge_error: take X-Forwarded-Host, derive prefix, look up
//     community, render HTML template with community name/contact and
//     Health.Get(...).LastError.
//   - GET /__bridge_status: marshal Health.All() as JSON.
//   - The page MUST NOT leak auth keys or personal-tailnet internals.
func (s *Server) Run(ctx context.Context) error { return nil }
