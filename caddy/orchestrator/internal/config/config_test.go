package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTmp(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("PK", "tskey-personal")
	t.Setenv("CK", "tskey-community")
	p := writeTmp(t, "config.yml", `
personal:
  auth_key_env: PK
  bridge_hostname: alice-bridge
communities:
  - id: smithfamily
    directory_url: https://directory.smithfamily.ts.net/services
    auth_key_env: CK
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Errorf("poll_interval default: got %v want %v", cfg.PollInterval, DefaultPollInterval)
	}
	if cfg.CommunityJoinTimeout != DefaultCommunityJoinTimeout {
		t.Errorf("community_join_timeout default mismatch: %v", cfg.CommunityJoinTimeout)
	}
	if cfg.StateDir != DefaultStateDir {
		t.Errorf("state_dir default mismatch: %v", cfg.StateDir)
	}
	if cfg.CaddyAdminAddr != DefaultCaddyAdminAddr {
		t.Errorf("caddy_admin_addr default mismatch: %v", cfg.CaddyAdminAddr)
	}
	if cfg.OrchestratorErrorPort != DefaultOrchestratorErrorPort {
		t.Errorf("orchestrator_error_port default mismatch: %v", cfg.OrchestratorErrorPort)
	}
	if cfg.Personal.AuthKey != "tskey-personal" {
		t.Errorf("personal key not resolved: %q", cfg.Personal.AuthKey)
	}
	if cfg.Personal.BridgeHostname != "alice-bridge" {
		t.Errorf("bridge_hostname: %q", cfg.Personal.BridgeHostname)
	}
	if got := cfg.Communities; len(got) != 1 || got[0].ID != "smithfamily" || got[0].AuthKey != "tskey-community" {
		t.Errorf("communities: %+v", got)
	}
}

func TestLoad_OverrideDurations(t *testing.T) {
	t.Setenv("PK", "k")
	p := writeTmp(t, "c.yml", `
personal: { auth_key_env: PK }
communities: []
poll_interval: 30s
community_join_timeout: 90s
orchestrator_error_port: 9090
caddy_admin_addr: 127.0.0.1:3000
state_dir: /tmp/bridge
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("poll_interval: %v", cfg.PollInterval)
	}
	if cfg.CommunityJoinTimeout != 90*time.Second {
		t.Errorf("community_join_timeout: %v", cfg.CommunityJoinTimeout)
	}
	if cfg.OrchestratorErrorPort != 9090 {
		t.Errorf("port: %d", cfg.OrchestratorErrorPort)
	}
	if cfg.CaddyAdminAddr != "127.0.0.1:3000" {
		t.Errorf("admin addr: %s", cfg.CaddyAdminAddr)
	}
	if cfg.StateDir != "/tmp/bridge" {
		t.Errorf("state_dir: %s", cfg.StateDir)
	}
}

func TestLoad_RejectsUnknownField(t *testing.T) {
	t.Setenv("PK", "k")
	p := writeTmp(t, "c.yml", `
personal:
  auth_key_env: PK
  surprise: yes
communities: []
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoad_RejectsTwoAuthSources(t *testing.T) {
	t.Setenv("PK", "k")
	p := writeTmp(t, "c.yml", `
personal:
  auth_key_env: PK
  auth_key: literal
communities: []
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for two auth sources")
	}
}

func TestLoad_RejectsHttpDirectoryURL(t *testing.T) {
	t.Setenv("PK", "k")
	t.Setenv("CK", "ck")
	p := writeTmp(t, "c.yml", `
personal: { auth_key_env: PK }
communities:
  - id: smith
    directory_url: http://nope.example/dir
    auth_key_env: CK
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected https-only error")
	}
}

func TestLoad_RejectsDuplicateCommunityID(t *testing.T) {
	t.Setenv("PK", "k")
	t.Setenv("CK", "ck")
	p := writeTmp(t, "c.yml", `
personal: { auth_key_env: PK }
communities:
  - id: smith
    directory_url: https://a.example/d
    auth_key_env: CK
  - id: smith
    directory_url: https://b.example/d
    auth_key_env: CK
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected duplicate-id error")
	}
}

func TestLoad_AuthKeyFile(t *testing.T) {
	tmp := t.TempDir()
	kf := filepath.Join(tmp, "k.txt")
	if err := os.WriteFile(kf, []byte("  tskey-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeTmp(t, "c.yml", `
personal:
  auth_key_file: `+kf+`
communities: []
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Personal.AuthKey != "tskey-from-file" {
		t.Errorf("auth_key_file trim: %q", cfg.Personal.AuthKey)
	}
}

func TestLoad_BridgeHostnameDefault(t *testing.T) {
	t.Setenv("PK", "k")
	p := writeTmp(t, "c.yml", `
personal:
  auth_key_env: PK
communities: []
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Personal.BridgeHostname == "" {
		t.Fatal("expected non-empty default bridge hostname")
	}
}
