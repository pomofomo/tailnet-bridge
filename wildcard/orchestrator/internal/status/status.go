// Package status serves /__bridge_error (HTML rendered for Caddy's
// handle_errors upstream) and /__bridge_status (JSON for the user's own
// debugging). Bound to 127.0.0.1:<port>; never reachable from outside
// the container.
//
// SPEC §10.4, §12.
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
	"time"

	"bridge/internal/health"
)

// Server is the HTTP server.
type Server struct {
	Addr   string // e.g. "127.0.0.1:8081"
	Health *health.Store
}

// Run binds and serves until ctx is cancelled.
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

// handleError serves /__bridge_error.
//
// Caddy reaches it via handle_errors AND via the unknown-subdomain
// catch-all. The original host is in X-Forwarded-Host.
func (s *Server) handleError(w http.ResponseWriter, r *http.Request) {
	fwdHost := r.Header.Get("X-Forwarded-Host")
	if fwdHost == "" {
		fwdHost = r.Host
	}
	if h, _, err := net.SplitHostPort(fwdHost); err == nil {
		fwdHost = h
	}

	commID := s.Health.CommunityIDForHost(fwdHost)
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
	data.CertError = snap.CertError
	if !snap.LastSuccessfulPoll.IsZero() {
		data.LastSuccessful = snap.LastSuccessfulPoll.UTC().Format(time.RFC3339)
	}
	if !snap.CertNotAfter.IsZero() {
		data.CertNotAfter = snap.CertNotAfter.UTC().Format(time.RFC3339)
		remain := time.Until(snap.CertNotAfter)
		if remain > 0 {
			data.CertDaysLeft = int(remain.Hours()) / 24
			// SPEC §9.4 step 3: <24h is the "expiring soon" threshold.
			data.CertExpiringSoon = remain < 24*time.Hour
		} else {
			data.CertExpired = true
		}
	}
	if !snap.CertLastReload.IsZero() {
		data.CertLastReload = snap.CertLastReload.UTC().Format(time.RFC3339)
	}
	if snap.DNSLeak != nil {
		data.DNSLeakDomain = snap.DNSLeak.Domain
		data.DNSLeakAnswers = strings.Join(snap.DNSLeak.Answers, ", ")
	}

	// Diagnosis: prefer the most actionable thing.
	switch {
	case data.CertExpired:
		data.Diagnosis = "wildcard cert expired"
	case data.CertError != "":
		data.Diagnosis = "wildcard cert problem"
	case data.CertExpiringSoon:
		data.Diagnosis = "wildcard cert expires soon"
	case data.LastError != "":
		data.Diagnosis = "directory or upstream problem"
	case commID == "":
		data.Diagnosis = "unknown hostname"
	default:
		data.Diagnosis = "upstream unreachable"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadGateway)
	_ = errorTmpl.Execute(w, data)
}

// handleStatus serves /__bridge_status: a JSON dump of the health store.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	all := s.Health.All()
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

type errorPageData struct {
	Host             string
	CommunityID      string
	CommunityName    string
	CommunityContact string
	LastError        string
	CertError        string
	LastSuccessful   string
	CertNotAfter     string
	CertDaysLeft     int
	CertExpired      bool
	CertExpiringSoon bool
	CertLastReload   string
	DNSLeakDomain    string
	DNSLeakAnswers   string
	Diagnosis        string
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
  .leak { background: #fff8e1; border: 1px solid #f0d68a; padding: .75rem 1rem; border-radius: 4px; margin-top: 1rem; }
</style>
</head>
<body>
<h1>This community service can't be reached right now.</h1>
<p>You requested <code>{{.Host}}</code>{{if .CommunityName}}, which belongs to the <strong>{{.CommunityName}}</strong> community{{end}}.</p>
<p>Diagnosis: <strong>{{.Diagnosis}}</strong>.</p>

{{if .CertExpired}}
<p>The wildcard TLS cert for this community has <strong>expired</strong>{{if .CertNotAfter}} (not-after: <code>{{.CertNotAfter}}</code>){{end}}.
The community admin rotates the cert periodically; if you didn't receive the rotation, that's how community membership is enforced (SPEC §3.5, §6.5).</p>
{{else if .CertError}}
<p>The wildcard TLS cert for this community failed validation:</p>
<div class="err">{{.CertError}}</div>
{{else if .CertExpiringSoon}}
<div class="leak">
<strong>Heads up:</strong> the wildcard TLS cert for this community expires in under 24 hours
{{if .CertNotAfter}}(not-after: <code>{{.CertNotAfter}}</code>){{end}}.
Ask the community admin to rotate (SPEC §6.3).
</div>
{{else if .LastError}}
<p>The bridge last saw this error talking to that community:</p>
<div class="err">{{.LastError}}</div>
{{end}}

{{if .DNSLeakDomain}}
<div class="leak">
<strong>Operator warning:</strong> the public-DNS sanity probe found a record for
<code>{{.DNSLeakDomain}}</code>{{if .DNSLeakAnswers}} → <code>{{.DNSLeakAnswers}}</code>{{end}}.
Per SPEC §3.5, names under <code>ts.&lt;base&gt;</code> must never resolve on the public internet.
The bridge still works but the wildcard-cert trust model is compromised; tell the community admin.
</div>
{{end}}

<p>What to do:</p>
<ul>
{{if .CommunityContact}}
  <li>Contact your community admin: <code>{{.CommunityContact}}</code>.</li>
{{else}}
  <li>Contact your community admin and ask them to check the service and your access.</li>
{{end}}
  <li>Or remove the community from your bridge's <code>config.yml</code> if you no longer want it.</li>
</ul>

<p class="meta">
{{if .LastSuccessful}}Last successful directory poll: {{.LastSuccessful}}.{{end}}
{{if .CertNotAfter}} Cert not-after: {{.CertNotAfter}}{{if and (gt .CertDaysLeft 0) (not .CertExpired)}} ({{.CertDaysLeft}} days remaining){{end}}.{{end}}
{{if .CertLastReload}} Last cert rotation picked up: {{.CertLastReload}}.{{end}}
{{if .CommunityID}} Community id: <code>{{.CommunityID}}</code>.{{end}}
</p>
</body>
</html>
`))
