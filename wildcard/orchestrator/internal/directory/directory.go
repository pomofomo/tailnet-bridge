// Package directory fetches and validates a community's service
// directory document (SPEC §8).
//
// Status: STUB. Types match the SPEC §8.2 schema; Fetch and Validate
// are TODOs.
package directory

import (
	"context"
	"errors"
	"net/http"
)

// Directory is the parsed and validated body of a directory response.
type Directory struct {
	Version   int       `json:"version"`
	Community Community `json:"community"`
	Services  []Service `json:"services"`
}

// Community describes the community publishing this directory.
type Community struct {
	Name    string `json:"name"`
	Domain  string `json:"domain"`  // <community>.ts.<basedomain>
	Tailnet string `json:"tailnet"` // <community>.ts.net
	Contact string `json:"contact,omitempty"`
}

// Service is one routable upstream within a community.
type Service struct {
	Name                string `json:"name"`                  // <name>.<community.domain>
	UpstreamTailnetHost string `json:"upstream_tailnet_host"` // dial target on the tailnet
	UpstreamPort        int    `json:"upstream_port"`         // typically 443
	Description         string `json:"description,omitempty"`
}

// Status is a tri-state outcome describing the HTTP result.
type Status int

const (
	StatusOK           Status = 200
	StatusNotModified  Status = 304
	StatusOther        Status = 500
)

// Fetch issues a GET against url with `If-None-Match: prevETag`. On HTTP
// 200 it parses + validates the body. On 304 it returns (nil, etag, 304,
// nil); the caller MUST keep its previously cached Directory.
//
// Validation rules (SPEC §8.2):
//   - version == 1.
//   - community.domain matches the bridge config's local `domain`.
//   - community.domain is exactly <community>.ts.<basedomain> (two labels
//     plus the `ts` label plus the base).
//   - community.tailnet ends in `.ts.net`.
//   - services[].name matches [a-z0-9][a-z0-9-]* and contains no dots
//     (services live exactly one label below community.domain).
//   - services[].name values are unique within the directory.
//   - services[].upstream_tailnet_host is a subdomain of community.tailnet.
//   - services[].upstream_port is 1..65535.
//   - combined "<name>.<community.domain>" is a valid DNS name
//     (≤253 chars; each label ≤63 chars).
//
// Implementation MUST:
//   - use a caller-provided *http.Client (already configured to dial via
//     the community-specific tsnet ephemeral node).
//   - cap response body at, say, 1 MiB.
//   - reject HTTPS-without-trust failures verbatim.
//   - return the response's ETag verbatim so the caller can pass it as
//     `prevETag` on the next call.
func Fetch(
	ctx context.Context,
	client *http.Client,
	url string,
	prevETag string,
	expectedDomain string,
) (*Directory, string, Status, error) {
	// TODO(impl): HTTP GET, parse JSON, call Validate, return tri-state.
	_, _, _, _, _ = ctx, client, url, prevETag, expectedDomain
	return nil, "", StatusOther, errNotImplemented
}

// Validate runs the schema rules from SPEC §8.2 on a parsed Directory.
// Exported for unit tests of synthetic directories.
func Validate(d *Directory, expectedDomain string) error {
	// TODO(impl): see the rule list above.
	_, _ = d, expectedDomain
	return errNotImplemented
}

// CanonicalHostname returns "<service>.<community.domain>" — the name the
// user types AND the upstream sees.
func (d *Directory) CanonicalHostname(svc Service) string {
	return svc.Name + "." + d.Community.Domain
}

var errNotImplemented = errors.New("directory: not yet implemented")
