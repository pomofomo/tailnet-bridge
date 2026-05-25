// Package config parses and validates the user's bridge config file
// (SPEC §7) and resolves auth-key references (env / file / direct).
//
// The output is a fully-resolved *Config suitable for the rest of the
// orchestrator: no further I/O is needed to interpret it.
package config

import "time"

// Config is the parsed bridge configuration.
type Config struct {
	Personal             Personal    `yaml:"personal"`
	Communities          []Community `yaml:"communities"`
	PollInterval         time.Duration
	CommunityJoinTimeout time.Duration
	StateDir             string
	CaddyAdminAddr       string
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

// Load reads, parses, and validates the YAML config at path.
//
// TODO:
//   - reject unknown fields (yaml.KnownFields / strict decoder)
//   - validate community IDs are unique and match [a-z0-9][a-z0-9-]*
//   - resolve auth_key_env / auth_key_file / auth_key (exactly one)
//   - apply defaults: poll_interval=5m, community_join_timeout=60s,
//     state_dir=/var/lib/bridge, caddy_admin_addr=127.0.0.1:2019,
//     orchestrator_error_port=8081
func Load(path string) (*Config, error) {
	return nil, nil
}
