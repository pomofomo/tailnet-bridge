// Package status serves /__bridge_error and /__bridge_status on a
// loopback HTTP server. Caddy's handle_errors and catch-all handlers
// reverse-proxy to this server; humans `curl 127.0.0.1:<port>` to see it.
//
// /__bridge_error  → human-readable HTML rendered from the latest
//                    health snapshot, naming the community, the kind of
//                    failure, the admin's contact, and the cert
//                    expiry timestamp.
// /__bridge_status → read-only JSON of the entire health snapshot.
//                    Intended for `make status` and external probes.
//
// Status: STUB.
package status

import (
	"context"
	"errors"
	"net/http"

	"bridge/internal/health"
)

// Server is the loopback status/error server.
type Server struct {
	Addr    string // "127.0.0.1:8081"
	Tracker *health.Tracker

	srv *http.Server
}

// New returns a Server bound to addr. addr SHOULD be 127.0.0.1:<port>
// (or [::1]:<port>); the package will not refuse non-loopback addresses
// but doing so would defeat the design.
func New(addr string, t *health.Tracker) *Server {
	return &Server{Addr: addr, Tracker: t}
}

// Run blocks until ctx is cancelled. It serves /__bridge_error and
// /__bridge_status; every other path returns 404.
func (s *Server) Run(ctx context.Context) error {
	// TODO(impl):
	//   1. Build a *http.ServeMux:
	//        - "/__bridge_error" → renderErrorHTML(s.Tracker.Snapshot(), r.Host)
	//        - "/__bridge_status" → json.NewEncoder(w).Encode(snapshot)
	//   2. Start an *http.Server on s.Addr with that mux.
	//   3. On ctx cancellation, Shutdown with a short grace period.
	//
	// /__bridge_error rendering rules:
	//   - r.Host tells us *.<community.domain>; look up the community
	//     by suffix.
	//   - Title: "<service>.<community>.ts.<base> is unavailable".
	//   - Body explains the most likely cause based on the State:
	//       * CertError != ""        → "wildcard cert problem"
	//       * LastError != ""        → "directory or upstream problem"
	//       * PublicDNSWarning set   → loud warning that public DNS
	//                                  resolves a ts.* name (operator
	//                                  problem, not user's).
	//       * CertNotAfter <  now+7d → "cert expiring soon; ask admin"
	//   - Include the admin contact from CurrentDirectory.Community.Contact.
	//   - Plain HTML; no JS; mobile-friendly inline CSS.
	_ = ctx
	return errNotImplemented
}

var errNotImplemented = errors.New("status: not yet implemented")
