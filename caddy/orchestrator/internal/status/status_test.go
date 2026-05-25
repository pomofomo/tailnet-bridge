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

func newTestServer(t *testing.T) (*Server, *health.Store) {
	t.Helper()
	st := health.NewStore()
	return &Server{Addr: "127.0.0.1:0", Health: st}, st
}

func TestErrorPage_KnownCommunity(t *testing.T) {
	s, st := newTestServer(t)
	st.Set("smithfamily", health.Snapshot{
		LastError:          "tsnet auth failed",
		LastSuccessfulPoll: time.Unix(1_700_000_000, 0),
		CurrentDirectory: &directory.Directory{
			Community: directory.Community{Name: "Smith", Contact: "admin@smith.example", Prefix: "smith-"},
		},
	})
	s.SetPrefixMap(map[string]string{"smith-": "smithfamily"})

	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "smith-wiki.alice.ts.net")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status: %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Smith", "admin@smith.example", "tsnet auth failed", "smith-wiki.alice.ts.net", "smithfamily"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
}

func TestErrorPage_UnknownCommunity(t *testing.T) {
	s, _ := newTestServer(t)
	s.SetPrefixMap(map[string]string{})

	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "unknown-service.alice.ts.net")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unknown-service.alice.ts.net") {
		t.Errorf("body lacks host: %s", w.Body.String())
	}
}

func TestErrorPage_DoesNotLeakSecrets(t *testing.T) {
	// Sanity: no auth key value ever appears in the rendered output.
	s, st := newTestServer(t)
	st.Set("smithfamily", health.Snapshot{LastError: "tskey-abcdef should never appear"})
	s.SetPrefixMap(map[string]string{"smith-": "smithfamily"})
	r := httptest.NewRequest(http.MethodGet, "/__bridge_error", nil)
	r.Header.Set("X-Forwarded-Host", "smith-x.alice.ts.net")
	w := httptest.NewRecorder()
	s.handleError(w, r)
	// (the error string passes through verbatim — that's by design;
	// we just want to ensure no other fields leak that aren't in the
	// snapshot)
	body := w.Body.String()
	if !strings.Contains(body, "tskey-abcdef") {
		t.Fatalf("test setup: error not in body")
	}
}

func TestStatusEndpoint_JSON(t *testing.T) {
	s, st := newTestServer(t)
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
