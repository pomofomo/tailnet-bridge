// Package config parses and validates the user's bridge config file
// (SPEC §7) and resolves auth-key references (env / file / direct).
//
// The output is a fully-resolved *Config suitable for the rest of the
// orchestrator: no further I/O is needed to interpret it.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Default values applied when the corresponding YAML field is omitted.
const (
	DefaultPollInterval           = 5 * time.Minute
	DefaultCommunityJoinTimeout   = 60 * time.Second
	DefaultStateDir               = "/var/lib/bridge"
	DefaultCaddyAdminAddr         = "127.0.0.1:2019"
	DefaultOrchestratorErrorPort  = 8081
)

// Config is the fully resolved bridge configuration.
type Config struct {
	Personal              Personal
	Communities           []Community
	PollInterval          time.Duration
	CommunityJoinTimeout  time.Duration
	StateDir              string
	CaddyAdminAddr        string
	OrchestratorErrorPort int
}

// Personal holds the user's personal-tailnet settings.
type Personal struct {
	// AuthKey is the resolved auth key (after env/file lookup).
	AuthKey        string
	BridgeHostname string
}

// Community describes one community the user belongs to.
type Community struct {
	ID           string
	DirectoryURL string
	// AuthKey is the resolved auth key for joining this community.
	AuthKey string
}

// ─── On-disk YAML schema ───────────────────────────────────────────────

type rawConfig struct {
	Personal              rawPersonal     `yaml:"personal"`
	Communities           []rawCommunity  `yaml:"communities"`
	PollInterval          string          `yaml:"poll_interval,omitempty"`
	CommunityJoinTimeout  string          `yaml:"community_join_timeout,omitempty"`
	StateDir              string          `yaml:"state_dir,omitempty"`
	CaddyAdminAddr        string          `yaml:"caddy_admin_addr,omitempty"`
	OrchestratorErrorPort int             `yaml:"orchestrator_error_port,omitempty"`
}

type rawPersonal struct {
	AuthKey        string `yaml:"auth_key,omitempty"`
	AuthKeyEnv     string `yaml:"auth_key_env,omitempty"`
	AuthKeyFile    string `yaml:"auth_key_file,omitempty"`
	BridgeHostname string `yaml:"bridge_hostname,omitempty"`
}

type rawCommunity struct {
	ID           string `yaml:"id"`
	DirectoryURL string `yaml:"directory_url"`
	AuthKey      string `yaml:"auth_key,omitempty"`
	AuthKeyEnv   string `yaml:"auth_key_env,omitempty"`
	AuthKeyFile  string `yaml:"auth_key_file,omitempty"`
}

var communityIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Load reads, parses, and validates the YAML config at path.
//
// On success the returned Config has all auth keys resolved and all
// defaults applied. Unknown fields cause an error (the user almost
// certainly meant something).
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
		CommunityJoinTimeout:  DefaultCommunityJoinTimeout,
		StateDir:              DefaultStateDir,
		CaddyAdminAddr:        DefaultCaddyAdminAddr,
		OrchestratorErrorPort: DefaultOrchestratorErrorPort,
	}

	if raw.PollInterval != "" {
		d, err := time.ParseDuration(raw.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("config: poll_interval: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: poll_interval must be positive, got %s", raw.PollInterval)
		}
		cfg.PollInterval = d
	}
	if raw.CommunityJoinTimeout != "" {
		d, err := time.ParseDuration(raw.CommunityJoinTimeout)
		if err != nil {
			return nil, fmt.Errorf("config: community_join_timeout: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: community_join_timeout must be positive, got %s", raw.CommunityJoinTimeout)
		}
		cfg.CommunityJoinTimeout = d
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
	for i, c := range raw.Communities {
		if c.ID == "" {
			return nil, fmt.Errorf("config: communities[%d]: id is required", i)
		}
		if !communityIDRE.MatchString(c.ID) {
			return nil, fmt.Errorf("config: communities[%d].id %q must match [a-z0-9][a-z0-9-]*", i, c.ID)
		}
		if _, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("config: communities: duplicate id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if c.DirectoryURL == "" {
			return nil, fmt.Errorf("config: communities[%s]: directory_url is required", c.ID)
		}
		if !strings.HasPrefix(c.DirectoryURL, "https://") {
			return nil, fmt.Errorf("config: communities[%s].directory_url must use https://", c.ID)
		}
		key, err := resolveAuthKey("communities."+c.ID, c.AuthKey, c.AuthKeyEnv, c.AuthKeyFile)
		if err != nil {
			return nil, err
		}
		cfg.Communities = append(cfg.Communities, Community{
			ID:           c.ID,
			DirectoryURL: c.DirectoryURL,
			AuthKey:      key,
		})
	}

	return cfg, nil
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
