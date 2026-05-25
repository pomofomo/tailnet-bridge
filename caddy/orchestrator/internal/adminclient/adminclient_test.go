package adminclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoad_OK(t *testing.T) {
	var gotCT, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/load" || r.Method != http.MethodPost {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	c := &Client{Addr: addr, HTTP: srv.Client()}
	if err := c.Load(context.Background(), []byte(`{"hello":"world"}`)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type: %q", gotCT)
	}
	if gotBody != `{"hello":"world"}` {
		t.Errorf("body: %q", gotBody)
	}
}

func TestLoad_PropagatesValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing field foo"}`))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	c := &Client{Addr: addr, HTTP: srv.Client()}
	err := c.Load(context.Background(), []byte(`{}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing field foo") {
		t.Errorf("error not propagated: %v", err)
	}
}
