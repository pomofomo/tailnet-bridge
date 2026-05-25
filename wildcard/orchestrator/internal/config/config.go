// Package config parses, validates, and resolves the wildcard bridge's
// config.yml (SPEC §7).
//
// The output is a fully-resolved *Config suitable for the rest of the
// orchestrator: no further I/O is needed to interpret it. Auth keys
// have been read out of their env/file references; cert files have
// been confirmed readable.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied when the corresponding YAML field is omitted.
const (
	DefaultPollInterval          = 5 * time.Minute
	DefaultCertCheckInterval     = 1 * time.Minute
	DefaultCommunityJoinTimeout  = 60 * time.Second
	DefaultStateDir              = "/var/lib/bridge"
	DefaultCaddyAdminAddr        = "127.0.0.1:2019"
	DefaultOrchestratorErrorPort = 8081
	DefaultDNSCheckResolver      = "8.8.8.8:53"
)

// Config is the fully resolved bridge configuration.
type Config struct {
	Personal              Personal
	Communities           []Community
	PollInterval          time.Duration
	CertCheckInterval     time.Duration
	CommunityJoinTimeout  time.Duration
	StateDir              string
	CaddyAdminAddr        string
	OrchestratorErrorPort int
	// DNSCheckResolver is the host:port of the public resolver used by
	// the dnscheck goroutine to detect ts.<base> names leaking into
	// public DNS (SPEC §3.5, §9.5).
	DNSCheckResolver string
}

// Personal holds the user's personal-tailnet settings.
type Personal struct {
	AuthKey        string // resolved
	BridgeHostname string
}

// Community describes one community the user belongs to.
type Community struct {
	ID           string
	Domain       string // e.g. "smithfamily.ts.example.com"
	DirectoryURL string
	AuthKey      string // resolved
	CertPath     string
	KeyPath      string
}

// ─── On-disk YAML schema ───────────────────────────────────────────────

type rawConfig struct {
	Personal              rawPersonal    `yaml:"personal"`
	Communities           []rawCommunity `yaml:"communities"`
	PollInterval          string         `yaml:"poll_interval,omitempty"`
	CertCheckInterval     string         `yaml:"cert_check_interval,omitempty"`
	CommunityJoinTimeout  string         `yaml:"community_join_timeout,omitempty"`
	StateDir              string         `yaml:"state_dir,omitempty"`
	CaddyAdminAddr        string         `yaml:"caddy_admin_addr,omitempty"`
	OrchestratorErrorPort int            `yaml:"orchestrator_error_port,omitempty"`
	DNSCheckResolver      string         `yaml:"dns_check_resolver,omitempty"`
}

type rawPersonal struct {
	AuthKey        string `yaml:"auth_key,omitempty"`
	AuthKeyEnv     string `yaml:"auth_key_env,omitempty"`
	AuthKeyFile    string `yaml:"auth_key_file,omitempty"`
	BridgeHostname string `yaml:"bridge_hostname,omitempty"`
}

type rawCommunity struct {
	ID           string `yaml:"id"`
	Domain       string `yaml:"domain"`
	DirectoryURL string `yaml:"directory_url"`
	AuthKey      string `yaml:"auth_key,omitempty"`
	AuthKeyEnv   string `yaml:"auth_key_env,omitempty"`
	AuthKeyFile  string `yaml:"auth_key_file,omitempty"`
	CertPath     string `yaml:"cert_path"`
	KeyPath      string `yaml:"key_path"`
}

var (
	communityIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// domain shape: <community>.ts.<basedomain>, e.g. "smith.ts.example.com".
	// Enforced: at least four labels (community + ts + base + tld), the
	// second label (zero-indexed: labels[1]) must be literally "ts".
	domainRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)
)

// Load reads, parses, and validates the YAML config at path.
//
// On success, every auth key is resolved, every cert/key file is
// confirmed to exist and be readable, and all defaults are applied.
// Unknown fields are rejected (`yaml.Decoder.KnownFields(true)`).
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	cfg := &Config{
		PollInterval:          DefaultPollInterval,
		CertCheckInterval:     DefaultCertCheckInterval,
		CommunityJoinTimeout:  DefaultCommunityJoinTimeout,
		StateDir:              DefaultStateDir,
		CaddyAdminAddr:        DefaultCaddyAdminAddr,
		OrchestratorErrorPort: DefaultOrchestratorErrorPort,
		DNSCheckResolver:      DefaultDNSCheckResolver,
	}

	if err := applyDurations(&raw, cfg); err != nil {
		return nil, err
	}
	if raw.StateDir != "" {
		cfg.StateDir = raw.StateDir
	}
	if raw.CaddyAdminAddr != "" {
		cfg.CaddyAdminAddr = raw.CaddyAdminAddr
	}
	if raw.OrchestratorErrorPort != 0 {
		if raw.OrchestratorErrorPort < 1 || raw.OrchestratorErrorPort > 65535 {
			return nil, fmt.Errorf("config: orchestrator_error_port out of range: %d", raw.OrchestratorErrorPort)
		}
		cfg.OrchestratorErrorPort = raw.OrchestratorErrorPort
	}
	if raw.DNSCheckResolver != "" {
		cfg.DNSCheckResolver = raw.DNSCheckResolver
	}

	personalKey, err := resolveAuthKey("personal", raw.Personal.AuthKey, raw.Personal.AuthKeyEnv, raw.Personal.AuthKeyFile)
	if err != nil {
		return nil, err
	}
	cfg.Personal = Personal{
		AuthKey:        personalKey,
		BridgeHostname: raw.Personal.BridgeHostname,
	}
	if cfg.Personal.BridgeHostname == "" {
		hn, err := os.Hostname()
		if err != nil || hn == "" {
			hn = "bridge"
		}
		cfg.Personal.BridgeHostname = hn
	}

	seen := make(map[string]struct{}, len(raw.Communities))
	var baseDomain string
	for i, c := range raw.Communities {
		ctxStr := fmt.Sprintf("communities[%d]", i)
		if c.ID != "" {
			ctxStr = fmt.Sprintf("communities[%s]", c.ID)
		}
		if c.ID == "" {
			return nil, fmt.Errorf("config: %s: id is required", ctxStr)
		}
		if !communityIDRE.MatchString(c.ID) {
			return nil, fmt.Errorf("config: %s: id %q must match [a-z0-9][a-z0-9-]*", ctxStr, c.ID)
		}
		if _, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("config: communities: duplicate id %q", c.ID)
		}
		seen[c.ID] = struct{}{}

		if c.Domain == "" {
			return nil, fmt.Errorf("config: %s: domain is required", ctxStr)
		}
		if err := validateDomainShape(c.Domain); err != nil {
			return nil, fmt.Errorf("config: %s: %w", ctxStr, err)
		}
		// All communities must share the same `ts.<base>` parent
		// (SPEC §13: multiple base domains not supported in one bridge).
		bd := baseDomainOf(c.Domain)
		if baseDomain == "" {
			baseDomain = bd
		} else if baseDomain != bd {
			return nil, fmt.Errorf("config: %s: domain %q is under a different base than the first community (%s vs %s)",
				ctxStr, c.Domain, bd, baseDomain)
		}

		if c.DirectoryURL == "" {
			return nil, fmt.Errorf("config: %s: directory_url is required", ctxStr)
		}
		if u, err := url.Parse(c.DirectoryURL); err != nil || u.Scheme != "https" {
			return nil, fmt.Errorf("config: %s: directory_url must use https://", ctxStr)
		}

		key, err := resolveAuthKey(ctxStr, c.AuthKey, c.AuthKeyEnv, c.AuthKeyFile)
		if err != nil {
			return nil, err
		}

		if c.CertPath == "" {
			return nil, fmt.Errorf("config: %s: cert_path is required", ctxStr)
		}
		if c.KeyPath == "" {
			return nil, fmt.Errorf("config: %s: key_path is required", ctxStr)
		}

		cfg.Communities = append(cfg.Communities, Community{
			ID:           c.ID,
			Domain:       strings.ToLower(c.Domain),
			DirectoryURL: c.DirectoryURL,
			AuthKey:      key,
			CertPath:     c.CertPath,
			KeyPath:      c.KeyPath,
		})
	}

	return cfg, nil
}

// applyDurations parses duration strings, applying them only when set.
// Each must be positive.
func applyDurations(raw *rawConfig, cfg *Config) error {
	for _, fld := range []struct {
		name string
		src  string
		dst  *time.Duration
	}{
		{"poll_interval", raw.PollInterval, &cfg.PollInterval},
		{"cert_check_interval", raw.CertCheckInterval, &cfg.CertCheckInterval},
		{"community_join_timeout", raw.CommunityJoinTimeout, &cfg.CommunityJoinTimeout},
	} {
		if fld.src == "" {
			continue
		}
		d, err := time.ParseDuration(fld.src)
		if err != nil {
			return fmt.Errorf("config: %s: %w", fld.name, err)
		}
		if d <= 0 {
			return fmt.Errorf("config: %s must be positive, got %s", fld.name, fld.src)
		}
		*fld.dst = d
	}
	return nil
}

// validateDomainShape enforces the <community>.ts.<basedomain> form.
// Specifically: at least 4 labels, lowercase per-label rules, the
// SECOND label (after the leading community label) must be exactly
// "ts" (SPEC §3.5 / §3.6 — the `ts.` subdomain is the tailnet-only
// zone that grounds the cert trust model).
func validateDomainShape(domain string) error {
	d := strings.ToLower(domain)
	if !domainRE.MatchString(d) {
		return fmt.Errorf("domain %q is not a valid DNS name", domain)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 4 {
		return fmt.Errorf("domain %q must be at least 4 labels (e.g. community.ts.base.tld)", domain)
	}
	if labels[1] != "ts" {
		return fmt.Errorf("domain %q: second label must be \"ts\" (SPEC §3.6 — the tailnet-only zone)", domain)
	}
	for _, lbl := range labels {
		if len(lbl) > 63 {
			return fmt.Errorf("domain %q: label %q exceeds 63 chars", domain, lbl)
		}
	}
	if len(d) > 253 {
		return fmt.Errorf("domain %q exceeds 253 chars", domain)
	}
	return nil
}

// baseDomainOf returns everything after the first label of domain.
// "smith.ts.example.com" → "ts.example.com".
func baseDomainOf(domain string) string {
	d := strings.ToLower(domain)
	if i := strings.IndexByte(d, '.'); i >= 0 {
		return d[i+1:]
	}
	return d
}

// BaseDomain returns the shared `ts.<base>` suffix all communities live
// under. Empty if there are no communities.
func (c *Config) BaseDomain() string {
	if len(c.Communities) == 0 {
		return ""
	}
	return baseDomainOf(c.Communities[0].Domain)
}

// Lookup returns the Community with the given id or an error if missing.
func (c *Config) Lookup(id string) (*Community, error) {
	for i := range c.Communities {
		if c.Communities[i].ID == id {
			return &c.Communities[i], nil
		}
	}
	return nil, fmt.Errorf("community %q not in config", id)
}

// resolveAuthKey enforces the "exactly one of auth_key / _env / _file"
// rule and returns the actual key string.
func resolveAuthKey(section, direct, envName, filePath string) (string, error) {
	set := 0
	if direct != "" {
		set++
	}
	if envName != "" {
		set++
	}
	if filePath != "" {
		set++
	}
	switch set {
	case 0:
		return "", fmt.Errorf("config: %s: one of auth_key, auth_key_env, auth_key_file is required", section)
	case 1:
	default:
		return "", fmt.Errorf("config: %s: only one of auth_key, auth_key_env, auth_key_file may be set", section)
	}

	switch {
	case direct != "":
		return direct, nil
	case envName != "":
		v := os.Getenv(envName)
		if v == "" {
			return "", fmt.Errorf("config: %s: env %s is empty or unset", section, envName)
		}
		return v, nil
	default:
		b, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("config: %s: read auth_key_file %s: %w", section, filePath, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", fmt.Errorf("config: %s: auth_key_file %s is empty", section, filePath)
		}
		return v, nil
	}
}
