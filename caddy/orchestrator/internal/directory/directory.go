// Package directory fetches and validates a community's service directory
// (SPEC §6). All HTTP I/O is performed through an http.Client supplied by
// the caller (in production: tsnet-backed); this package is unaware of
// tsnet itself.
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
	Directory   *Directory // nil when NotModified
	ETag        string
	NotModified bool
}

var (
	prefixRE      = regexp.MustCompile(`^[a-z0-9]+-$`)
	serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Fetch GETs url through client, sending If-None-Match: prevETag when
// non-empty. On 304 it returns &FetchResult{NotModified: true, ETag: prevETag}.
// On 200 it validates the body against SPEC §6.2 and returns the parsed
// Directory plus the new ETag.
//
// Non-2xx (other than 304) is an error; the caller decides how to surface
// it (typically: retain prior good copy and record in /__bridge_status).
func Fetch(ctx context.Context, client *http.Client, url, prevETag string) (*FetchResult, error) {
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

	ctype := resp.Header.Get("Content-Type")
	if ctype != "" {
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
	// Reject trailing data so a directory can't sneak in junk after the doc.
	if dec.More() {
		return nil, fmt.Errorf("directory: unexpected trailing data after JSON document")
	}

	if err := Validate(&d); err != nil {
		return nil, fmt.Errorf("directory: validate: %w", err)
	}

	return &FetchResult{Directory: &d, ETag: resp.Header.Get("ETag")}, nil
}

// Validate enforces SPEC §6.2 rules. Returns nil on success.
//
// Callers MUST NOT mutate the directory after Validate returns; this
// package treats *Directory as effectively immutable post-validation.
func Validate(d *Directory) error {
	if d == nil {
		return fmt.Errorf("nil directory")
	}
	if d.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", d.Version)
	}
	if d.Community.Name == "" {
		return fmt.Errorf("community.name is required")
	}
	if d.Community.Tailnet == "" {
		return fmt.Errorf("community.tailnet is required")
	}
	if !prefixRE.MatchString(d.Community.Prefix) {
		return fmt.Errorf("community.prefix %q must match [a-z0-9]+-", d.Community.Prefix)
	}
	seenNames := make(map[string]struct{}, len(d.Services))
	for i, s := range d.Services {
		ctx := fmt.Sprintf("services[%d]", i)
		if s.Name != "" {
			ctx = fmt.Sprintf("services[%q]", s.Name)
		}
		if !serviceNameRE.MatchString(s.Name) {
			return fmt.Errorf("%s.name %q must match [a-z0-9][a-z0-9-]*", ctx, s.Name)
		}
		if _, dup := seenNames[s.Name]; dup {
			return fmt.Errorf("services: duplicate name %q", s.Name)
		}
		seenNames[s.Name] = struct{}{}

		if len(d.Community.Prefix)+len(s.Name) > 63 {
			return fmt.Errorf("%s: prefix+name exceeds DNS label limit of 63 characters", ctx)
		}

		if s.UpstreamScheme != "https" {
			return fmt.Errorf("%s.upstream_scheme %q: only \"https\" is supported", ctx, s.UpstreamScheme)
		}
		if s.UpstreamPort < 1 || s.UpstreamPort > 65535 {
			return fmt.Errorf("%s.upstream_port %d out of range", ctx, s.UpstreamPort)
		}
		if s.UpstreamHost == "" {
			return fmt.Errorf("%s.upstream_host is required", ctx)
		}
		if !isSubdomainOf(s.UpstreamHost, d.Community.Tailnet) {
			return fmt.Errorf("%s.upstream_host %q is not a subdomain of community.tailnet %q",
				ctx, s.UpstreamHost, d.Community.Tailnet)
		}
		// rewrite_extra_hosts entries MUST be subdomains of community.tailnet
		// regardless of rewrite_body — the data validity is independent of
		// the runtime flag, so a directory that publishes invalid entries is
		// rejected even when they are currently ignored. (SPEC §6.2.)
		for j, h := range s.RewriteExtraHosts {
			if !isSubdomainOf(h, d.Community.Tailnet) {
				return fmt.Errorf("%s.rewrite_extra_hosts[%d] %q is not a subdomain of community.tailnet %q",
					ctx, j, h, d.Community.Tailnet)
			}
		}
	}
	return nil
}

// isSubdomainOf reports whether host is a strict subdomain of parent.
// Equality is rejected; case is normalized to lower.
func isSubdomainOf(host, parent string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	p := strings.ToLower(strings.TrimSuffix(parent, "."))
	if h == "" || p == "" {
		return false
	}
	if h == p {
		return false
	}
	return strings.HasSuffix(h, "."+p)
}
