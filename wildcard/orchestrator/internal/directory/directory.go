// Package directory fetches and validates a community's service
// directory (SPEC §8). All HTTP I/O is performed through an http.Client
// supplied by the caller (in production: tsnet-backed); this package is
// unaware of tsnet itself.
//
// The schema differs from the caddy variant: no prefix, no rewrite_*,
// no upstream_scheme. The canonical service hostname is
// `<service.name>.<community.domain>` on BOTH sides of the bridge
// (SPEC §3.2, §5.1).
package directory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
)

// Directory is the validated JSON document returned by a community.
type Directory struct {
	Version   int       `json:"version"`
	Community Community `json:"community"`
	Services  []Service `json:"services"`
}

// Community holds the directory-level community metadata.
type Community struct {
	Name    string `json:"name"`
	Domain  string `json:"domain"`  // canonical: <community>.ts.<base>
	Tailnet string `json:"tailnet"` // routing: <community>.ts.net
	Contact string `json:"contact,omitempty"`
}

// Service is a single bridged service.
type Service struct {
	Name                string `json:"name"`
	UpstreamTailnetHost string `json:"upstream_tailnet_host"`
	UpstreamPort        int    `json:"upstream_port"`
	Description         string `json:"description,omitempty"`
}

// FetchResult captures one Fetch outcome including caching state.
type FetchResult struct {
	Directory   *Directory // nil when NotModified
	ETag        string
	NotModified bool
}

var (
	serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	domainShapeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)
)

// Fetch GETs url through client, sending If-None-Match: prevETag when
// non-empty. On 304 it returns &FetchResult{NotModified: true, ETag: prevETag}.
// On 200 it validates the body against SPEC §8.2 and returns the parsed
// Directory plus the new ETag.
//
// expectedDomain is the bridge's local config value for this community.
// The directory's community.domain MUST match it (SPEC §8.2 validation
// rules); mismatch is a hard error.
//
// Non-2xx (other than 304) is an error; the caller decides how to surface
// it (typically: retain the prior good copy and record in /__bridge_status).
func Fetch(ctx context.Context, client *http.Client, url, prevETag, expectedDomain string) (*FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("directory: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if prevETag != "" {
		req.Header.Set("If-None-Match", prevETag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("directory: GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return &FetchResult{NotModified: true, ETag: prevETag}, nil
	case http.StatusOK:
		// proceed
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("directory: GET %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if ctype := resp.Header.Get("Content-Type"); ctype != "" {
		mt, _, err := mime.ParseMediaType(ctype)
		if err != nil {
			return nil, fmt.Errorf("directory: parse content-type %q: %w", ctype, err)
		}
		if mt != "application/json" {
			return nil, fmt.Errorf("directory: unexpected content-type %q (want application/json)", mt)
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("directory: read body: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var d Directory
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("directory: parse JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("directory: unexpected trailing data after JSON document")
	}

	if err := Validate(&d, expectedDomain); err != nil {
		return nil, fmt.Errorf("directory: validate: %w", err)
	}

	return &FetchResult{Directory: &d, ETag: resp.Header.Get("ETag")}, nil
}

// Validate enforces SPEC §8.2 rules. Returns nil on success.
//
// Callers MUST NOT mutate the directory after Validate returns.
func Validate(d *Directory, expectedDomain string) error {
	if d == nil {
		return fmt.Errorf("nil directory")
	}
	if d.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", d.Version)
	}
	if d.Community.Name == "" {
		return fmt.Errorf("community.name is required")
	}
	if d.Community.Domain == "" {
		return fmt.Errorf("community.domain is required")
	}
	dom := strings.ToLower(d.Community.Domain)
	if !domainShapeRE.MatchString(dom) {
		return fmt.Errorf("community.domain %q is not a valid DNS name", d.Community.Domain)
	}
	labels := strings.Split(dom, ".")
	if len(labels) < 4 || labels[1] != "ts" {
		return fmt.Errorf("community.domain %q must be <community>.ts.<basedomain> (SPEC §3.6)", d.Community.Domain)
	}
	if expectedDomain != "" && dom != strings.ToLower(expectedDomain) {
		return fmt.Errorf("community.domain %q does not match local config %q", d.Community.Domain, expectedDomain)
	}
	if d.Community.Tailnet == "" {
		return fmt.Errorf("community.tailnet is required")
	}
	tailnet := strings.ToLower(d.Community.Tailnet)
	if !strings.HasSuffix(tailnet, ".ts.net") {
		return fmt.Errorf("community.tailnet %q must end in .ts.net", d.Community.Tailnet)
	}

	seenNames := make(map[string]struct{}, len(d.Services))
	for i, s := range d.Services {
		ctxStr := fmt.Sprintf("services[%d]", i)
		if s.Name != "" {
			ctxStr = fmt.Sprintf("services[%q]", s.Name)
		}
		if !serviceNameRE.MatchString(s.Name) {
			return fmt.Errorf("%s.name %q must match [a-z0-9][a-z0-9-]*", ctxStr, s.Name)
		}
		if strings.ContainsRune(s.Name, '.') {
			return fmt.Errorf("%s.name %q must not contain dots (one label only)", ctxStr, s.Name)
		}
		if _, dup := seenNames[s.Name]; dup {
			return fmt.Errorf("services: duplicate name %q", s.Name)
		}
		seenNames[s.Name] = struct{}{}

		// Canonical hostname length / per-label limits.
		canonical := s.Name + "." + dom
		if len(canonical) > 253 {
			return fmt.Errorf("%s: canonical hostname %q exceeds 253 chars", ctxStr, canonical)
		}
		if len(s.Name) > 63 {
			return fmt.Errorf("%s.name exceeds 63-char DNS label limit", ctxStr)
		}

		if s.UpstreamTailnetHost == "" {
			return fmt.Errorf("%s.upstream_tailnet_host is required", ctxStr)
		}
		if !isSubdomainOf(s.UpstreamTailnetHost, d.Community.Tailnet) {
			return fmt.Errorf("%s.upstream_tailnet_host %q is not a subdomain of community.tailnet %q",
				ctxStr, s.UpstreamTailnetHost, d.Community.Tailnet)
		}
		if s.UpstreamPort < 1 || s.UpstreamPort > 65535 {
			return fmt.Errorf("%s.upstream_port %d out of range", ctxStr, s.UpstreamPort)
		}
	}
	return nil
}

// CanonicalHostname returns "<service.name>.<community.domain>" — the
// name the user types AND the upstream sees.
func (d *Directory) CanonicalHostname(svc Service) string {
	return svc.Name + "." + strings.ToLower(d.Community.Domain)
}

// isSubdomainOf reports whether host is a strict subdomain of parent.
// Equality is rejected; case is normalized to lower.
func isSubdomainOf(host, parent string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	p := strings.ToLower(strings.TrimSuffix(parent, "."))
	if h == "" || p == "" || h == p {
		return false
	}
	return strings.HasSuffix(h, "."+p)
}
