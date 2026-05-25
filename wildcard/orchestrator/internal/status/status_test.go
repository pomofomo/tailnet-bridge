package status

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bridge/internal/directory"
	"bridge/internal/health"
)

func newTestServer() (*Server, *health.Store) {
	st := health.NewStore()
	return &Server{Addr: "127.0.0.1:0", Health: st}, st
}

func TestErrorPage_KnownCommunity(t *testing.T) {
	s, st := newTestServer()
	st.SetDomain("smithfamily", "smithfamily.ts.example.com")
	st.Set("smithfamily", health.Snapshot{
		LastError:          "tsnet auth failed",
		LastSuccessfulPoll: time.Unix(1_700_000_000, 0),
		CertNotAfter:       time.Now().Add(7 * 24 * time.Hour),
		CurrentDirectory: &directory.Directory{
			Community: directory.Community{Name: "Smith", Domain: "smithfamily.ts.example.com", Contact: "admin@smith.example"},
		},
	})

	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "wiki.smithfamily.ts.example.com")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Smith", "admin@smith.example", "tsnet auth failed", "wiki.smithfamily.ts.example.com", "smithfamily"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
}

func TestErrorPage_CertExpired(t *testing.T) {
	s, st := newTestServer()
	st.SetDomain("smithfamily", "smithfamily.ts.example.com")
	st.Set("smithfamily", health.Snapshot{
		CertNotAfter:     time.Now().Add(-time.Hour),
		CurrentDirectory: &directory.Directory{Community: directory.Community{Name: "Smith", Domain: "smithfamily.ts.example.com"}},
	})

	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "wiki.smithfamily.ts.example.com")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "expired") {
		t.Errorf("expected cert-expired diagnosis in body:\n%s", body)
	}
}

func TestErrorPage_DNSLeak(t *testing.T) {
	s, st := newTestServer()
	st.SetDomain("smithfamily", "smithfamily.ts.example.com")
	st.Set("smithfamily", health.Snapshot{
		CurrentDirectory: &directory.Directory{Community: directory.Community{Name: "Smith", Domain: "smithfamily.ts.example.com"}},
		DNSLeak: &health.DNSLeak{
			Domain:   "any.smithfamily.ts.example.com",
			Resolver: "8.8.8.8:53",
			Answers:  []string{"203.0.113.42"},
			When:     time.Now(),
		},
	})

	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "wiki.smithfamily.ts.example.com")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "Operator warning") || !strings.Contains(body, "203.0.113.42") {
		t.Errorf("expected DNS leak warning in body:\n%s", body)
	}
}

func TestErrorPage_UnknownCommunity(t *testing.T) {
	s, _ := newTestServer()
	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "unknown.example.com")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unknown.example.com") {
		t.Errorf("body lacks host: %s", w.Body.String())
	}
}

func TestStatusEndpoint_JSON(t *testing.T) {
	s, st := newTestServer()
	st.Set("a", health.Snapshot{LastError: "boom"})
	st.Set("b", health.Snapshot{ETag: "v2"})

	srv := httptest.NewServer(http.HandlerFunc(s.handleStatus))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Communities map[string]health.Snapshot `json:"communities"`
		Order       []string                   `json:"order"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, body)
	}
	if out.Communities["a"].LastError != "boom" {
		t.Errorf("missing a entry: %+v", out)
	}
	if len(out.Order) != 2 || out.Order[0] != "a" || out.Order[1] != "b" {
		t.Errorf("order: %+v", out.Order)
	}
}
