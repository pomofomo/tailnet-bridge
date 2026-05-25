package directory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validDir() *Directory {
	return &Directory{
		Version: 1,
		Community: Community{
			Name:    "Smith",
			Tailnet: "smithfamily.ts.net",
			Prefix:  "smith-",
		},
		Services: []Service{
			{Name: "wiki", UpstreamHost: "wiki.smithfamily.ts.net", UpstreamPort: 443, UpstreamScheme: "https"},
		},
	}
}

func TestValidate_OK(t *testing.T) {
	if err := Validate(validDir()); err != nil {
		t.Fatalf("Validate OK: %v", err)
	}
}

func TestValidate_Cases(t *testing.T) {
	type mut func(*Directory)
	cases := []struct {
		name      string
		m         mut
		errSubstr string
	}{
		{"wrong version", func(d *Directory) { d.Version = 2 }, "version"},
		{"empty tailnet", func(d *Directory) { d.Community.Tailnet = "" }, "tailnet"},
		{"bad prefix uppercase", func(d *Directory) { d.Community.Prefix = "Smith-" }, "prefix"},
		{"bad prefix missing dash", func(d *Directory) { d.Community.Prefix = "smith" }, "prefix"},
		{"bad service name", func(d *Directory) { d.Services[0].Name = "-bad" }, "name"},
		{"http scheme rejected", func(d *Directory) { d.Services[0].UpstreamScheme = "http" }, "scheme"},
		{"port out of range", func(d *Directory) { d.Services[0].UpstreamPort = 0 }, "port"},
		{"host outside tailnet", func(d *Directory) { d.Services[0].UpstreamHost = "wiki.other.ts.net" }, "subdomain"},
		{"host equals tailnet", func(d *Directory) { d.Services[0].UpstreamHost = "smithfamily.ts.net" }, "subdomain"},
		{"dup service names", func(d *Directory) {
			d.Services = append(d.Services, Service{Name: "wiki", UpstreamHost: "wiki.smithfamily.ts.net", UpstreamPort: 443, UpstreamScheme: "https"})
		}, "duplicate"},
		{"rewrite extra outside tailnet", func(d *Directory) {
			d.Services[0].RewriteBody = true
			d.Services[0].RewriteExtraHosts = []string{"static.example.com"}
		}, "subdomain"},
		{"rewrite extra outside tailnet (rewrite_body=false)", func(d *Directory) {
			d.Services[0].RewriteBody = false
			d.Services[0].RewriteExtraHosts = []string{"static.example.com"}
		}, "subdomain"},
		{"label too long", func(d *Directory) {
			d.Community.Prefix = strings.Repeat("a", 30) + "-"
			d.Services[0].Name = strings.Repeat("b", 40)
		}, "label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDir()
			tc.m(d)
			err := Validate(d)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errSubstr)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.errSubstr) {
				t.Fatalf("expected substring %q in %v", tc.errSubstr, err)
			}
		})
	}
}

func TestIsSubdomainOf(t *testing.T) {
	cases := []struct {
		host, parent string
		want         bool
	}{
		{"wiki.smithfamily.ts.net", "smithfamily.ts.net", true},
		{"a.b.smithfamily.ts.net", "smithfamily.ts.net", true},
		{"smithfamily.ts.net", "smithfamily.ts.net", false},
		{"otherfamily.ts.net", "smithfamily.ts.net", false},
		{"wikismithfamily.ts.net", "smithfamily.ts.net", false},
		{"WIKI.SmithFamily.ts.net", "smithfamily.ts.net", true},
	}
	for _, c := range cases {
		if got := isSubdomainOf(c.host, c.parent); got != c.want {
			t.Errorf("isSubdomainOf(%q,%q)=%v want %v", c.host, c.parent, got, c.want)
		}
	}
}

func TestFetch_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{
"version":1,
"community":{"name":"Smith","tailnet":"smithfamily.ts.net","prefix":"smith-"},
"services":[{"name":"wiki","upstream_host":"wiki.smithfamily.ts.net","upstream_port":443,"upstream_scheme":"https"}]
}`))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.NotModified {
		t.Fatal("unexpected NotModified")
	}
	if res.ETag != `"v1"` {
		t.Errorf("etag: %q", res.ETag)
	}
	if res.Directory.Services[0].Name != "wiki" {
		t.Errorf("body parsed wrong: %+v", res.Directory)
	}
}

func TestFetch_304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("missing If-None-Match: %q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.Client(), srv.URL, `"v1"`)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.NotModified {
		t.Fatal("expected NotModified")
	}
	if res.ETag != `"v1"` {
		t.Errorf("etag preservation: %q", res.ETag)
	}
}

func TestFetch_UnknownField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":1,"community":{"name":"S","tailnet":"s.ts.net","prefix":"s-"},"services":[],"extra":"junk"}`))
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestFetch_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream is on fire"))
	}))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	if err == nil {
		t.Fatal("expected error")
	}
}
