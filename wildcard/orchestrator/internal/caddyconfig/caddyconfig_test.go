package caddyconfig

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"bridge/internal/cert"
	"bridge/internal/config"
	"bridge/internal/directory"
)

func sampleCfg() *config.Config {
	return &config.Config{
		Personal: config.Personal{
			AuthKey:        "personal-key",
			BridgeHostname: "alice-bridge",
		},
		Communities: []config.Community{
			{
				ID:           "smithfamily",
				Domain:       "smithfamily.ts.example.com",
				DirectoryURL: "https://d.smithfamily.ts.example.com/",
				AuthKey:      "smith-key",
				CertPath:     "/etc/bridge/certs/smithfamily/cert.pem",
				KeyPath:      "/etc/bridge/certs/smithfamily/key.pem",
			},
			{
				ID:           "austinmakers",
				Domain:       "austinmakers.ts.example.com",
				DirectoryURL: "https://d.austinmakers.ts.example.com/",
				AuthKey:      "austin-key",
				CertPath:     "/etc/bridge/certs/austinmakers/cert.pem",
				KeyPath:      "/etc/bridge/certs/austinmakers/key.pem",
			},
		},
		StateDir:              "/var/lib/bridge",
		CaddyAdminAddr:        "127.0.0.1:2019",
		OrchestratorErrorPort: 8081,
	}
}

func sampleDirs() map[string]*directory.Directory {
	return map[string]*directory.Directory{
		"smithfamily": {
			Version: 1,
			Community: directory.Community{
				Name: "Smith", Domain: "smithfamily.ts.example.com", Tailnet: "smithfamily.ts.net",
			},
			Services: []directory.Service{
				{Name: "wiki", UpstreamTailnetHost: "wiki.smithfamily.ts.net", UpstreamPort: 443},
				{Name: "git", UpstreamTailnetHost: "git.smithfamily.ts.net", UpstreamPort: 443},
			},
		},
		"austinmakers": {
			Version: 1,
			Community: directory.Community{
				Name: "Austin", Domain: "austinmakers.ts.example.com", Tailnet: "austinmakers.ts.net",
			},
			Services: []directory.Service{
				{Name: "wiki", UpstreamTailnetHost: "wiki.austinmakers.ts.net", UpstreamPort: 443},
			},
		},
	}
}

func sampleCerts() map[string]*cert.Bundle {
	return map[string]*cert.Bundle{
		"smithfamily":  {CertPath: "/etc/bridge/certs/smithfamily/cert.pem", KeyPath: "/etc/bridge/certs/smithfamily/key.pem"},
		"austinmakers": {CertPath: "/etc/bridge/certs/austinmakers/cert.pem", KeyPath: "/etc/bridge/certs/austinmakers/key.pem"},
	}
}

func TestBuild_Determinism(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	certs := sampleCerts()
	a, err := Build(Input{Config: cfg, Directories: dirs, Certs: certs})
	if err != nil {
		t.Fatalf("Build a: %v", err)
	}
	b, err := Build(Input{Config: cfg, Directories: dirs, Certs: certs})
	if err != nil {
		t.Fatalf("Build b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Build is non-deterministic.\nA:\n%s\nB:\n%s", a, b)
	}
	// Reorder services slice — must produce identical bytes.
	dirs2 := sampleDirs()
	dirs2["smithfamily"].Services[0], dirs2["smithfamily"].Services[1] =
		dirs2["smithfamily"].Services[1], dirs2["smithfamily"].Services[0]
	c, err := Build(Input{Config: cfg, Directories: dirs2, Certs: certs})
	if err != nil {
		t.Fatalf("Build c: %v", err)
	}
	if !bytes.Equal(a, c) {
		t.Fatalf("Build is sensitive to service ordering:\nA:\n%s\nC:\n%s", a, c)
	}
}

func TestBuild_StructuralShape(t *testing.T) {
	out, err := Build(Input{Config: sampleCfg(), Directories: sampleDirs(), Certs: sampleCerts()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	apps := v["apps"].(map[string]any)

	// tailscale nodes: 2 per community (listener + dialer).
	ts := apps["tailscale"].(map[string]any)["nodes"].(map[string]any)
	wantNodes := []string{
		"personal-smithfamily", "community-dialer-smithfamily",
		"personal-austinmakers", "community-dialer-austinmakers",
	}
	for _, n := range wantNodes {
		if _, ok := ts[n]; !ok {
			t.Errorf("missing tailscale node %q", n)
		}
	}
	smith := ts["personal-smithfamily"].(map[string]any)
	if smith["auth_key"].(string) != "personal-key" {
		t.Errorf("personal-smithfamily auth key wrong: %v", smith)
	}
	if smith["hostname"].(string) != "smithfamily-bridge" {
		t.Errorf("personal-smithfamily hostname: %v", smith["hostname"])
	}
	dialer := ts["community-dialer-smithfamily"].(map[string]any)
	if dialer["auth_key"].(string) != "smith-key" {
		t.Errorf("dialer auth key: %v", dialer)
	}
	if dialer["hostname"].(string) != "alice-bridge" {
		t.Errorf("dialer hostname: %v", dialer)
	}

	// TLS load_files: one entry per community.
	tls := apps["tls"].(map[string]any)
	files := tls["certificates"].(map[string]any)["load_files"].([]any)
	if len(files) != 2 {
		t.Errorf("load_files count: %d", len(files))
	}

	// One HTTP server per community.
	servers := apps["http"].(map[string]any)["servers"].(map[string]any)
	if _, ok := servers["smithfamily"]; !ok {
		t.Fatal("missing smithfamily server")
	}
	smithSrv := servers["smithfamily"].(map[string]any)
	listen := smithSrv["listen"].([]any)
	if listen[0].(string) != "tailscale/personal-smithfamily:443" {
		t.Errorf("listen address wrong: %v", listen)
	}
	autoHTTPS := smithSrv["automatic_https"].(map[string]any)
	if autoHTTPS["disable"] != true {
		t.Errorf("automatic_https should be disabled")
	}

	// Routes: 1 per service + 1 catch-all = 3.
	routes := smithSrv["routes"].([]any)
	if len(routes) != 3 {
		t.Errorf("expected 3 routes, got %d", len(routes))
	}
	// Routes are sorted by service name: "git", "wiki", catch-all.
	first := routes[0].(map[string]any)
	matchHost := first["match"].([]any)[0].(map[string]any)["host"].([]any)
	if matchHost[0].(string) != "git.smithfamily.ts.example.com" {
		t.Errorf("first route should match git: %v", matchHost)
	}
	// Handler chain: authentication → reverse_proxy.
	handle := first["handle"].([]any)
	var handlers []string
	for _, h := range handle {
		handlers = append(handlers, h.(map[string]any)["handler"].(string))
	}
	if len(handlers) != 2 || handlers[0] != "authentication" || handlers[1] != "reverse_proxy" {
		t.Errorf("handler chain: %v", handlers)
	}
	// Reverse-proxy: Host preserved, identity headers set, server_name override.
	rp := handle[1].(map[string]any)
	transport := rp["transport"].(map[string]any)
	if transport["protocol"].(string) != "tailscale" {
		t.Errorf("transport protocol: %v", transport["protocol"])
	}
	if transport["name"].(string) != "community-dialer-smithfamily" {
		t.Errorf("transport name: %v", transport["name"])
	}
	tlsCfg := transport["tls"].(map[string]any)
	if tlsCfg["server_name"].(string) != "git.smithfamily.ts.example.com" {
		t.Errorf("SNI override wrong: %v", tlsCfg)
	}
	dial := rp["upstreams"].([]any)[0].(map[string]any)["dial"].(string)
	if dial != "git.smithfamily.ts.net:443" {
		t.Errorf("dial target: %q", dial)
	}
	reqSet := rp["headers"].(map[string]any)["request"].(map[string]any)["set"].(map[string]any)
	if got := reqSet["Host"].([]any)[0].(string); got != "{http.request.host}" {
		t.Errorf("Host header should be preserved (canonical name on both sides): %q", got)
	}
	for _, hdr := range []string{"X-Tailscale-User", "X-Tailscale-User-Email", "X-Tailscale-Node"} {
		if _, ok := reqSet[hdr]; !ok {
			t.Errorf("missing identity header %q", hdr)
		}
	}

	// Catch-all (last route): host=*.smithfamily.ts.example.com, error chain.
	catchAll := routes[2].(map[string]any)
	cHost := catchAll["match"].([]any)[0].(map[string]any)["host"].([]any)
	if cHost[0].(string) != "*.smithfamily.ts.example.com" {
		t.Errorf("catch-all host: %v", cHost)
	}
	cHandle := catchAll["handle"].([]any)
	if cHandle[0].(map[string]any)["handler"].(string) != "rewrite" {
		t.Errorf("catch-all handler 0: %v", cHandle[0])
	}

	// errors block points at the status server too.
	errRoutes := smithSrv["errors"].(map[string]any)["routes"].([]any)
	errHandle := errRoutes[0].(map[string]any)["handle"].([]any)
	rp2 := errHandle[1].(map[string]any)
	dial2 := rp2["upstreams"].([]any)[0].(map[string]any)["dial"].(string)
	if dial2 != "127.0.0.1:8081" {
		t.Errorf("error route dial: %q", dial2)
	}

	// bridgedns has an entry per community.
	bd := apps["bridgedns"].(map[string]any)["nodes"].(map[string]any)
	if _, ok := bd["smithfamily"]; !ok {
		t.Errorf("bridgedns missing smithfamily")
	}
	smithBD := bd["smithfamily"].(map[string]any)
	if smithBD["tsnet_node"].(string) != "personal-smithfamily" {
		t.Errorf("bridgedns tsnet_node: %v", smithBD)
	}
	if smithBD["domain"].(string) != "smithfamily.ts.example.com" {
		t.Errorf("bridgedns domain: %v", smithBD)
	}
}

func TestBuild_SkipsMissingCert(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	certs := sampleCerts()
	delete(certs, "smithfamily") // missing cert → community omitted

	out, err := Build(Input{Config: cfg, Directories: dirs, Certs: certs})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bytes.Contains(out, []byte("personal-smithfamily")) {
		t.Errorf("missing-cert community leaked into config:\n%s", out)
	}
	if !bytes.Contains(out, []byte("personal-austinmakers")) {
		t.Errorf("rest of communities should still be there:\n%s", out)
	}
}

func TestBuild_SkipsMissingDirectory(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	delete(dirs, "smithfamily")
	out, err := Build(Input{Config: cfg, Directories: dirs, Certs: sampleCerts()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bytes.Contains(out, []byte("smithfamily.ts.example.com")) &&
		!bytes.Contains(out, []byte("austinmakers.ts.example.com")) {
		t.Errorf("missing-directory community should be omitted")
	}
}

func TestBuild_IgnoresUnknownCommunityInDirs(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	dirs["rogue"] = &directory.Directory{
		Version: 1,
		Community: directory.Community{
			Name: "Rogue", Domain: "rogue.ts.example.com", Tailnet: "rogue.ts.net",
		},
	}
	out, err := Build(Input{Config: cfg, Directories: dirs, Certs: sampleCerts()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bytes.Contains(out, []byte("rogue")) {
		t.Errorf("rogue directory leaked into output:\n%s", out)
	}
}

func TestHash_Stable(t *testing.T) {
	out, _ := Build(Input{Config: sampleCfg(), Directories: sampleDirs(), Certs: sampleCerts()})
	h1 := Hash(out)
	h2 := Hash(out)
	if h1 != h2 {
		t.Error("Hash not deterministic")
	}
	if h1 == ([32]byte{}) {
		t.Error("Hash zero")
	}
}

func TestBuild_NilConfig(t *testing.T) {
	if _, err := Build(Input{}); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "config") {
		t.Errorf("unexpected error: %v", err)
	}
}
