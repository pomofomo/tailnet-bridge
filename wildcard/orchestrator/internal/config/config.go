// Package config parses, validates, and resolves the bridge's config.yml.
// See SPEC §7 for the schema and SPEC §3.5 / §7.2 for the cert-file model.
//
// Status: STUB. Types and the public API (`Load`) are in place. YAML
// unmarshal + validation + env/file resolution are TODOs.
package config

import (
	"errors"
	"fmt"
	"time"
)

// Config is the parsed, validated, and resolved bridge configuration.
type Config struct {
	Personal              Personal      `yaml:"personal"`
	Communities           []Community   `yaml:"communities"`
	PollInterval          time.Duration `yaml:"poll_interval"`
	CertCheckInterval     time.Duration `yaml:"cert_check_interval"`
	CommunityJoinTimeout  time.Duration `yaml:"community_join_timeout"`
	StateDir              string        `yaml:"state_dir"`
	CaddyAdminAddr        string        `yaml:"caddy_admin_addr"`
	OrchestratorErrorPort int           `yaml:"orchestrator_error_port"`
}

// Personal holds the user's tailnet identity.
type Personal struct {
	// Exactly one of the three may be set after resolution.
	AuthKeyEnv  string `yaml:"auth_key_env,omitempty"`
	AuthKeyFile string `yaml:"auth_key_file,omitempty"`
	AuthKey     string `yaml:"auth_key,omitempty"`

	// Resolved auth key value. Populated by Load(); never read from YAML.
	ResolvedAuthKey string `yaml:"-"`

	BridgeHostname string `yaml:"bridge_hostname"`
}

// Community is one community-tailnet membership.
type Community struct {
	ID           string `yaml:"id"`
	Domain       string `yaml:"domain"`
	DirectoryURL string `yaml:"directory_url"`

	AuthKeyEnv      string `yaml:"auth_key_env,omitempty"`
	AuthKeyFile     string `yaml:"auth_key_file,omitempty"`
	AuthKey         string `yaml:"auth_key,omitempty"`
	ResolvedAuthKey string `yaml:"-"`

	CertPath string `yaml:"cert_path"`
	KeyPath  string `yaml:"key_path"`
}

// Defaults, applied by Load() to any field left zero in the YAML.
const (
	DefaultPollInterval          = 5 * time.Minute
	DefaultCertCheckInterval     = 1 * time.Minute
	DefaultCommunityJoinTimeout  = 60 * time.Second
	DefaultStateDir              = "/var/lib/bridge"
	DefaultCaddyAdminAddr        = "127.0.0.1:2019"
	DefaultOrchestratorErrorPort = 8081
)

// Load reads, parses, and validates the config at path. It also resolves
// every `auth_key_env` and `auth_key_file` reference into the matching
// `ResolvedAuthKey`, and stats every (cert_path, key_path) pair to fail
// fast on missing files.
//
// Validation rules (SPEC §7, §8.2):
//   - personal.auth_key{,_env,_file} exactly one set.
//   - communities[].id matches [a-z0-9][a-z0-9-]*, unique.
//   - communities[].domain looks like <id>.ts.<basedomain> AND ends in
//     a label set whose second-to-last label is exactly "ts".
//   - All communities share the same <basedomain> (SPEC §13: "Mixing
//     ts.example.com with ts.example.org in one bridge is not supported").
//   - communities[].directory_url is HTTPS.
//   - communities[].auth_key{,_env,_file} exactly one set.
//   - communities[].cert_path and key_path exist and are readable.
//   - Reject any unknown top-level or per-community YAML field.
func Load(path string) (*Config, error) {
	// TODO(impl):
	//   1. Read the file.
	//   2. yaml.UnmarshalStrict (or KnownFields(true)) into a Config.
	//   3. Apply defaults for unset duration / addr / port fields.
	//   4. Validate per the rules above.
	//   5. Resolve every auth_key_env (os.Getenv) and auth_key_file
	//      (os.ReadFile, TrimSpace) into ResolvedAuthKey. Fail if a
	//      referenced env var is empty.
	//   6. Stat cert_path and key_path for every community; on missing
	//      file, return an error mentioning the community id.
	//
	// Sample tests to write (config_test.go):
	//   - minimal valid config round-trips.
	//   - unknown field rejected.
	//   - all three auth-key forms covered; "two set" and "none set" rejected.
	//   - domain shape rules (must have "ts" label).
	//   - mixed basedomains across communities rejected.
	_ = path
	return nil, errNotImplemented
}

// Validate checks logical invariants on an already-parsed Config. Load()
// calls this internally; exported for tests of synthetic configs.
func Validate(c *Config) error {
	// TODO(impl): see the rule list in Load's doc comment.
	_ = c
	return errNotImplemented
}

// BaseDomain returns the shared `ts.<basedomain>` suffix all communities
// live under (i.e. `domain` minus its first label). Empty if there are no
// communities.
func (c *Config) BaseDomain() string {
	// TODO(impl): split community[0].Domain on the first dot.
	if len(c.Communities) == 0 {
		return ""
	}
	return ""
}

// Lookup returns the Community with the given id or fmt.Errorf if missing.
func (c *Config) Lookup(id string) (*Community, error) {
	for i := range c.Communities {
		if c.Communities[i].ID == id {
			return &c.Communities[i], nil
		}
	}
	return nil, fmt.Errorf("community %q not in config", id)
}

var errNotImplemented = errors.New("config: not yet implemented")
