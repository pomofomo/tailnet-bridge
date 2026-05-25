package caddyconfig

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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
			{ID: "smithfamily", DirectoryURL: "https://d.smithfamily.ts.net/", AuthKey: "smith-key"},
			{ID: "austinmakers", DirectoryURL: "https://d.austinmakers.ts.net/", AuthKey: "austin-key"},
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
				Name: "Smith", Tailnet: "smithfamily.ts.net", Prefix: "smith-",
			},
			Services: []directory.Service{
				{Name: "wiki", UpstreamHost: "wiki.smithfamily.ts.net", UpstreamPort: 443, UpstreamScheme: "https"},
				{Name: "git", UpstreamHost: "git.smithfamily.ts.net", UpstreamPort: 443, UpstreamScheme: "https", RewriteBody: true, RewriteExtraHosts: []string{"git-static.smithfamily.ts.net"}},
			},
		},
		"austinmakers": {
			Version: 1,
			Community: directory.Community{
				Name: "Austin", Tailnet: "austinmakers.ts.net", Prefix: "austin-",
			},
			Services: []directory.Service{
				{Name: "wiki", UpstreamHost: "wiki.austinmakers.ts.net", UpstreamPort: 443, UpstreamScheme: "https"},
			},
		},
	}
}

func TestBuild_Determinism(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	a, err := Build(Input{Config: cfg, Directories: dirs})
	if err != nil {
		t.Fatalf("Build a: %v", err)
	}
	b, err := Build(Input{Config: cfg, Directories: dirs})
	if err != nil {
		t.Fatalf("Build b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("Build is non-deterministic.\nA:\n%s\nB:\n%s", a, b)
	}
	// Also: rebuild with reordered services slice — must be identical
	// because Build sorts by name internally.
	dirs2 := sampleDirs()
	dirs2["smithfamily"].Services[0], dirs2["smithfamily"].Services[1] = dirs2["smithfamily"].Services[1], dirs2["smithfamily"].Services[0]
	c, err := Build(Input{Config: cfg, Directories: dirs2})
	if err != nil {
		t.Fatalf("Build c: %v", err)
	}
	if !bytes.Equal(a, c) {
		t.Fatalf("Build sensitive to input service order:\nA:\n%s\nC:\n%s", a, c)
	}
}

func TestBuild_StructuralShape(t *testing.T) {
	out, err := Build(Input{Config: sampleCfg(), Directories: sampleDirs()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	apps := v["apps"].(map[string]any)
	ts := apps["tailscale"].(map[string]any)
	nodes := ts["nodes"].(map[string]any)
	wantNodes := []string{
		"community-dialer-smithfamily",
		"community-dialer-austinmakers",
		"personal-smithfamily-wiki",
		"personal-smithfamily-git",
		"personal-austinmakers-wiki",
	}
	for _, n := range wantNodes {
		if _, ok := nodes[n]; !ok {
			t.Errorf("missing node %q", n)
		}
	}
	smithDialer := nodes["community-dialer-smithfamily"].(map[string]any)
	if smithDialer["auth_key"].(string) != "smith-key" {
		t.Errorf("smith-key not propagated: %+v", smithDialer)
	}
	if smithDialer["hostname"].(string) != "alice-bridge" {
		t.Errorf("dialer hostname wrong: %v", smithDialer)
	}
	smithWiki := nodes["personal-smithfamily-wiki"].(map[string]any)
	if smithWiki["hostname"].(string) != "smith-wiki" {
		t.Errorf("listener hostname wrong: %v", smithWiki)
	}
	if smithWiki["auth_key"].(string) != "personal-key" {
		t.Errorf("listener auth key not personal: %v", smithWiki)
	}

	http := apps["http"].(map[string]any)
	servers := http["servers"].(map[string]any)
	if _, ok := servers["personal-smithfamily-git"]; !ok {
		t.Fatalf("missing git server")
	}

	// Body-rewrite path: rewrite_body=true must yield a replace_response
	// handler and an encode handler in the chain. Listener TLS must be
	// the tsnet+tls form.
	gitSrv := servers["personal-smithfamily-git"].(map[string]any)
	listen := gitSrv["listen"].([]any)
	if listen[0].(string) != "tailscale+tls/personal-smithfamily-git:443" {
		t.Errorf("listen address wrong: %v", listen)
	}
	routes := gitSrv["routes"].([]any)
	handle := routes[0].(map[string]any)["handle"].([]any)
	var handlers []string
	for _, h := range handle {
		handlers = append(handlers, h.(map[string]any)["handler"].(string))
	}
	want := []string{"encode", "replace_response", "authentication", "reverse_proxy"}
	if !equalStringSlice(handlers, want) {
		t.Errorf("handler order with rewrite_body=true: got %v want %v", handlers, want)
	}

	// Wiki (no body rewrite): chain is auth → reverse_proxy.
	wikiSrv := servers["personal-smithfamily-wiki"].(map[string]any)
	wHandle := wikiSrv["routes"].([]any)[0].(map[string]any)["handle"].([]any)
	var wHandlers []string
	for _, h := range wHandle {
		wHandlers = append(wHandlers, h.(map[string]any)["handler"].(string))
	}
	if !equalStringSlice(wHandlers, []string{"authentication", "reverse_proxy"}) {
		t.Errorf("handler order with rewrite_body=false: %v", wHandlers)
	}

	// handle_errors → /__bridge_error → status server.
	errors := wikiSrv["errors"].(map[string]any)
	erRoutes := errors["routes"].([]any)
	erHandle := erRoutes[0].(map[string]any)["handle"].([]any)
	if erHandle[0].(map[string]any)["handler"].(string) != "rewrite" {
		t.Errorf("error handler 0 wrong: %v", erHandle[0])
	}
	if erHandle[0].(map[string]any)["uri"].(string) != "/__bridge_error" {
		t.Errorf("error rewrite uri: %v", erHandle[0])
	}
	rp := erHandle[1].(map[string]any)
	if rp["handler"].(string) != "reverse_proxy" {
		t.Errorf("error handler 1 not reverse_proxy: %v", rp)
	}
	dial := rp["upstreams"].([]any)[0].(map[string]any)["dial"].(string)
	if dial != "127.0.0.1:8081" {
		t.Errorf("error upstream dial: %q", dial)
	}
}

func TestBuild_Collision(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	// Force both communities to share a prefix → collision on "wiki".
	dirs["austinmakers"].Community.Prefix = "smith-"
	_, err := Build(Input{Config: cfg, Directories: dirs})
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("expected collision error, got %v", err)
	}
}

func TestBuild_IgnoresUnknownCommunityInDirs(t *testing.T) {
	cfg := sampleCfg()
	dirs := sampleDirs()
	// A directory not referenced by config must be ignored.
	dirs["rogue"] = &directory.Directory{
		Version: 1,
		Community: directory.Community{Name: "Rogue", Tailnet: "rogue.ts.net", Prefix: "rogue-"},
	}
	out, err := Build(Input{Config: cfg, Directories: dirs})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if bytes.Contains(out, []byte("rogue")) {
		t.Fatalf("rogue directory leaked into output:\n%s", out)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
