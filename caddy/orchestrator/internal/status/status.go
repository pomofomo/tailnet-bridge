// Package status serves /__bridge_error (HTML, rendered for Caddy's
// handle_errors upstream) and /__bridge_status (JSON, for the user's own
// debugging). Bound to 127.0.0.1:<port>; never reachable from outside
// the container.
//
// SPEC §9.2, §9.3.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"bridge/internal/health"
)

// Server is the HTTP server.
type Server struct {
	// Addr is the bind address, e.g. "127.0.0.1:8081".
	Addr string
	// Health is the per-community snapshot store.
	Health *health.Store

	mu             sync.RWMutex
	prefixToCommID map[string]string // "smithfamily-" -> "smithfamily"
}

// SetPrefixMap replaces the prefix→communityID lookup table used by
// /__bridge_error to identify which community a failed request was for.
// The poller calls this after every successful directory merge.
func (s *Server) SetPrefixMap(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	s.mu.Lock()
	s.prefixToCommID = cp
	s.mu.Unlock()
}

// Run binds and serves until ctx is cancelled. Returns once Shutdown
// completes or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	if s.Health == nil {
		return fmt.Errorf("status: nil Health store")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/__bridge_error", s.handleError)
	mux.HandleFunc("/__bridge_status", s.handleStatus)

	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("status: listen %s: %w", s.Addr, err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// handleError serves /__bridge_error. Caddy reaches it via handle_errors
// after rewriting the URI; the original host is in X-Forwarded-Host.
func (s *Server) handleError(w http.ResponseWriter, r *http.Request) {
	fwdHost := r.Header.Get("X-Forwarded-Host")
	if fwdHost == "" {
		fwdHost = r.Host
	}

	// X-Forwarded-Host may include port; strip.
	if h, _, err := net.SplitHostPort(fwdHost); err == nil {
		fwdHost = h
	}

	commID := s.communityIDForHost(fwdHost)
	snap, _ := s.Health.Get(commID)

	data := errorPageData{
		Host:        fwdHost,
		CommunityID: commID,
	}
	if snap.CurrentDirectory != nil {
		data.CommunityName = snap.CurrentDirectory.Community.Name
		data.CommunityContact = snap.CurrentDirectory.Community.Contact
	}
	data.LastError = snap.LastError
	if !snap.LastSuccessfulPoll.IsZero() {
		data.LastSuccessful = snap.LastSuccessfulPoll.UTC().Format(time.RFC3339)
	}

	// 502: the upstream failed. The caller (Caddy) records its own
	// status; this body is what the user sees in their browser.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)
	_ = errorTmpl.Execute(w, data)
}

// handleStatus serves /__bridge_status: a JSON dump of the health store.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	all := s.Health.All()
	// Sorted output for stable presentation.
	out := struct {
		Communities map[string]health.Snapshot `json:"communities"`
		Order       []string                   `json:"order"`
	}{Communities: all}
	for k := range all {
		out.Order = append(out.Order, k)
	}
	sort.Strings(out.Order)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func (s *Server) communityIDForHost(host string) string {
	if host == "" {
		return ""
	}
	// The personal-side host always begins with one of the known
	// community prefixes (e.g. "smithfamily-wiki.alice.ts.net").
	s.mu.RLock()
	defer s.mu.RUnlock()
	for prefix, id := range s.prefixToCommID {
		if strings.HasPrefix(host, prefix) {
			return id
		}
	}
	return ""
}

type errorPageData struct {
	Host             string
	CommunityID      string
	CommunityName    string
	CommunityContact string
	LastError        string
	LastSuccessful   string
}

var errorTmpl = template.Must(template.New("err").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Bridge: service unreachable</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 36rem; margin: 4rem auto; padding: 0 1rem; color: #222; line-height: 1.4; }
  h1 { font-size: 1.4rem; margin-bottom: 1rem; }
  code { background: #f3f3f3; padding: 0 .25rem; border-radius: 3px; }
  .err { background: #fff5f5; border: 1px solid #f0c5c5; padding: .75rem 1rem; border-radius: 4px; white-space: pre-wrap; }
  .meta { color: #666; font-size: .9rem; margin-top: 1.5rem; }
</style>
</head>
<body>
<h1>This community service can't be reached right now.</h1>
<p>You requested <code>{{.Host}}</code>{{if .CommunityName}}, which belongs to the <strong>{{.CommunityName}}</strong> community{{end}}.</p>
{{if .LastError}}
<p>The bridge last saw this error talking to that community's directory:</p>
<div class="err">{{.LastError}}</div>
{{else}}
<p>The bridge is connected to that community's directory, so the service itself is most likely down or restarting.</p>
{{end}}
<p>What to do:</p>
<ul>
{{if .CommunityContact}}
  <li>Contact your community admin: <code>{{.CommunityContact}}</code>. Ask them to check the service and your access.</li>
{{else}}
  <li>Contact your community admin. Ask them to check the service and your access.</li>
{{end}}
  <li>Or remove the community from your bridge's <code>config.yml</code> if you no longer want it.</li>
</ul>
<p class="meta">
{{if .LastSuccessful}}Last successful directory poll: {{.LastSuccessful}}.{{end}}
{{if .CommunityID}}Community id: <code>{{.CommunityID}}</code>.{{end}}
</p>
</body>
</html>
`))
