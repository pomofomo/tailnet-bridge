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
    domain: smithfamily.ts.example.com
    directory_url: https://directory.smithfamily.ts.example.com/services
    auth_key_env: CK
    cert_path: /etc/bridge/certs/smithfamily/cert.pem
    key_path:  /etc/bridge/certs/smithfamily/key.pem
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Errorf("poll_interval default: got %v want %v", cfg.PollInterval, DefaultPollInterval)
	}
	if cfg.CertCheckInterval != DefaultCertCheckInterval {
		t.Errorf("cert_check_interval default: %v", cfg.CertCheckInterval)
	}
	if cfg.Personal.AuthKey != "tskey-personal" {
		t.Errorf("personal key: %q", cfg.Personal.AuthKey)
	}
	if cfg.Personal.BridgeHostname != "alice-bridge" {
		t.Errorf("bridge_hostname: %q", cfg.Personal.BridgeHostname)
	}
	if got := cfg.Communities; len(got) != 1 || got[0].ID != "smithfamily" ||
		got[0].Domain != "smithfamily.ts.example.com" ||
		got[0].AuthKey != "tskey-community" ||
		got[0].CertPath != "/etc/bridge/certs/smithfamily/cert.pem" {
		t.Errorf("communities: %+v", got)
	}
	if cfg.BaseDomain() != "ts.example.com" {
		t.Errorf("BaseDomain: %q", cfg.BaseDomain())
	}
}

func TestLoad_OverrideDurations(t *testing.T) {
	t.Setenv("PK", "k")
	p := writeTmp(t, "c.yml", `
personal: { auth_key_env: PK }
communities: []
poll_interval: 30s
cert_check_interval: 15s
community_join_timeout: 90s
orchestrator_error_port: 9090
caddy_admin_addr: 127.0.0.1:3000
state_dir: /tmp/bridge
dns_check_resolver: 1.1.1.1:53
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("poll_interval: %v", cfg.PollInterval)
	}
	if cfg.CertCheckInterval != 15*time.Second {
		t.Errorf("cert_check_interval: %v", cfg.CertCheckInterval)
	}
	if cfg.CommunityJoinTimeout != 90*time.Second {
		t.Errorf("community_join_timeout: %v", cfg.CommunityJoinTimeout)
	}
	if cfg.OrchestratorErrorPort != 9090 {
		t.Errorf("port: %d", cfg.OrchestratorErrorPort)
	}
	if cfg.DNSCheckResolver != "1.1.1.1:53" {
		t.Errorf("dns_check_resolver: %s", cfg.DNSCheckResolver)
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
	if _, err := Load(p); err == nil {
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
    domain: smith.ts.example.com
    directory_url: http://nope.example/dir
    auth_key_env: CK
    cert_path: /a
    key_path: /b
`)
	if _, err := Load(p); err == nil {
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
    domain: smith.ts.example.com
    directory_url: https://a.example/d
    auth_key_env: CK
    cert_path: /a
    key_path: /b
  - id: smith
    domain: smith.ts.example.com
    directory_url: https://b.example/d
    auth_key_env: CK
    cert_path: /c
    key_path: /d
`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected duplicate-id error")
	}
}

func TestLoad_DomainShape(t *testing.T) {
	t.Setenv("PK", "k")
	t.Setenv("CK", "ck")
	cases := []struct {
		name      string
		domain    string
		wantErr   string
	}{
		{"missing ts label", "smith.example.com", "ts"},
		{"three labels", "smith.ts.example", "4 labels"},
		{"too few", "ts.example.com", "4 labels"},
		{"uppercase", "Smith.ts.example.com", ""}, // lowercased, valid
		{"double dot", "smith..example.com", "valid DNS name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `
personal: { auth_key_env: PK }
communities:
  - id: smith
    domain: ` + tc.domain + `
    directory_url: https://x.example/d
    auth_key_env: CK
    cert_path: /a
    key_path: /b
`
			p := writeTmp(t, "c.yml", body)
			_, err := Load(p)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoad_MixedBaseDomainsRejected(t *testing.T) {
	t.Setenv("PK", "k")
	t.Setenv("CK", "ck")
	p := writeTmp(t, "c.yml", `
personal: { auth_key_env: PK }
communities:
  - id: a
    domain: a.ts.example.com
    directory_url: https://x.example/d
    auth_key_env: CK
    cert_path: /a
    key_path: /b
  - id: b
    domain: b.ts.example.org
    directory_url: https://y.example/d
    auth_key_env: CK
    cert_path: /c
    key_path: /d
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "different base") {
		t.Fatalf("expected mixed-base rejection, got %v", err)
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

func TestLoad_CertPathRequired(t *testing.T) {
	t.Setenv("PK", "k")
	t.Setenv("CK", "ck")
	p := writeTmp(t, "c.yml", `
personal: { auth_key_env: PK }
communities:
  - id: smith
    domain: smith.ts.example.com
    directory_url: https://x.example/d
    auth_key_env: CK
    key_path: /b
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "cert_path") {
		t.Fatalf("expected cert_path error: %v", err)
	}
}

func TestConfig_Lookup(t *testing.T) {
	cfg := &Config{Communities: []Community{
		{ID: "smith"},
		{ID: "austin"},
	}}
	if c, err := cfg.Lookup("smith"); err != nil || c.ID != "smith" {
		t.Errorf("lookup smith: %v %v", c, err)
	}
	if _, err := cfg.Lookup("nope"); err == nil {
		t.Error("expected lookup miss")
	}
}
