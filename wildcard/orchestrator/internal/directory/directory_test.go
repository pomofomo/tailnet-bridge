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
			Name:    "Smith Family",
			Domain:  "smithfamily.ts.example.com",
			Tailnet: "smithfamily.ts.net",
			Contact: "admin@example.com",
		},
		Services: []Service{
			{Name: "wiki", UpstreamTailnetHost: "wiki.smithfamily.ts.net", UpstreamPort: 443},
			{Name: "git", UpstreamTailnetHost: "git.smithfamily.ts.net", UpstreamPort: 443},
		},
	}
}

func TestValidate_OK(t *testing.T) {
	if err := Validate(validDir(), "smithfamily.ts.example.com"); err != nil {
		t.Fatalf("Validate OK: %v", err)
	}
}

func TestValidate_DomainMatchesLocal(t *testing.T) {
	d := validDir()
	d.Community.Domain = "other.ts.example.com"
	err := Validate(d, "smithfamily.ts.example.com")
	if err == nil || !strings.Contains(err.Error(), "local config") {
		t.Fatalf("expected local-config mismatch error, got %v", err)
	}
}

func TestValidate_Cases(t *testing.T) {
	cases := []struct {
		name      string
		mut       func(*Directory)
		errSubstr string
	}{
		{"wrong version", func(d *Directory) { d.Version = 2 }, "version"},
		{"empty domain", func(d *Directory) { d.Community.Domain = "" }, "community.domain"},
		{"domain missing ts", func(d *Directory) { d.Community.Domain = "smith.example.com" }, "4 labels"},
		{"domain not enough labels", func(d *Directory) { d.Community.Domain = "ts.example.com" }, "4 labels"},
		{"empty tailnet", func(d *Directory) { d.Community.Tailnet = "" }, "tailnet"},
		{"non ts.net tailnet", func(d *Directory) { d.Community.Tailnet = "smith.example.com" }, "ts.net"},
		{"bad service name", func(d *Directory) { d.Services[0].Name = "-bad" }, "name"},
		{"service name with dot", func(d *Directory) { d.Services[0].Name = "a.b" }, "name"},
		{"duplicate service", func(d *Directory) {
			d.Services = append(d.Services, Service{Name: "wiki", UpstreamTailnetHost: "wiki.smithfamily.ts.net", UpstreamPort: 443})
		}, "duplicate"},
		{"port out of range", func(d *Directory) { d.Services[0].UpstreamPort = 0 }, "port"},
		{"port too high", func(d *Directory) { d.Services[0].UpstreamPort = 70000 }, "port"},
		{"upstream not on tailnet", func(d *Directory) {
			d.Services[0].UpstreamTailnetHost = "wiki.other.ts.net"
		}, "subdomain"},
		{"upstream equals tailnet", func(d *Directory) {
			d.Services[0].UpstreamTailnetHost = "smithfamily.ts.net"
		}, "subdomain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDir()
			tc.mut(d)
			err := Validate(d, d.Community.Domain) // skip local-config check
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errSubstr)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.errSubstr)) {
				t.Errorf("error %q does not contain %q", err, tc.errSubstr)
			}
		})
	}
}

func TestCanonicalHostname(t *testing.T) {
	d := validDir()
	got := d.CanonicalHostname(d.Services[0])
	if got != "wiki.smithfamily.ts.example.com" {
		t.Errorf("CanonicalHostname: %q", got)
	}
}

func TestFetch_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{
"version":1,
"community":{"name":"Smith","domain":"smithfamily.ts.example.com","tailnet":"smithfamily.ts.net"},
"services":[{"name":"wiki","upstream_tailnet_host":"wiki.smithfamily.ts.net","upstream_port":443}]
}`))
	}))
	defer srv.Close()

	res, err := Fetch(context.Background(), srv.Client(), srv.URL, "", "smithfamily.ts.example.com")
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

	res, err := Fetch(context.Background(), srv.Client(), srv.URL, `"v1"`, "smithfamily.ts.example.com")
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
		_, _ = w.Write([]byte(`{"version":1,"community":{"name":"S","domain":"s.ts.example.com","tailnet":"s.ts.net"},"services":[],"extra":"junk"}`))
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, "", "s.ts.example.com"); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestFetch_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream is on fire"))
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, "", "any.ts.example.com"); err == nil {
		t.Fatal("expected error")
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
