// Package directory fetches and validates a community's service directory
// (SPEC §6). All HTTP I/O is performed through a tsnet-backed http.Client
// supplied by the caller, so this package is unaware of tsnet itself.
package directory

import (
	"context"
	"net/http"
)

// Directory is the validated JSON document returned by a community.
type Directory struct {
	Version   int         `json:"version"`
	Community Community   `json:"community"`
	Services  []Service   `json:"services"`
}

// Community holds the directory-level community metadata.
type Community struct {
	Name    string `json:"name"`
	Tailnet string `json:"tailnet"`
	Prefix  string `json:"prefix"`
	Contact string `json:"contact,omitempty"`
}

// Service is a single bridged service.
type Service struct {
	Name              string   `json:"name"`
	UpstreamHost      string   `json:"upstream_host"`
	UpstreamPort      int      `json:"upstream_port"`
	UpstreamScheme    string   `json:"upstream_scheme"`
	Description       string   `json:"description,omitempty"`
	RewriteBody       bool     `json:"rewrite_body,omitempty"`
	RewriteExtraHosts []string `json:"rewrite_extra_hosts,omitempty"`
}

// FetchResult captures one Fetch outcome including caching state.
type FetchResult struct {
	Directory *Directory // nil when NotModified or error
	ETag      string
	NotModified bool
}

// Fetch GETs url through client, sending If-None-Match: prevETag when
// non-empty. On 304 it returns FetchResult{NotModified: true, ETag: prevETag}.
// On 200 it validates the body against SPEC §6.2 and returns the parsed
// Directory plus the new ETag.
//
// TODO:
//   - send If-None-Match when prevETag != ""
//   - require Content-Type application/json
//   - decode strictly (DisallowUnknownFields)
//   - call Validate before returning
func Fetch(ctx context.Context, client *http.Client, url, prevETag string) (*FetchResult, error) {
	return nil, nil
}

// Validate enforces SPEC §6.2 rules:
//   - version == 1
//   - community.prefix matches [a-z0-9]+-
//   - service names match [a-z0-9][a-z0-9-]*, are unique
//   - upstream_scheme == "https"
//   - every upstream_host and rewrite_extra_hosts entry is a strict
//     subdomain of community.tailnet
//   - len(prefix + name) <= 63
func Validate(d *Directory) error {
	return nil
}
