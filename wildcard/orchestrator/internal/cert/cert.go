// Package cert handles the static wildcard cert files the community admin
// distributes to members (SPEC §3.5, §6.3, §7.2, §9.4, §10.1).
//
// Responsibilities:
//   - Load a PEM cert+key pair from disk.
//   - Verify the key matches the cert, the cert covers
//     "*.<community.domain>", expiry is far enough away, and the chain
//     validates against the system trust store.
//   - Watch a set of (cert, key) paths for content changes and emit
//     an event when any of them rotate.
//
// Status: STUB.
package cert

import (
	"context"
	"crypto/x509"
	"errors"
	"time"
)

// Bundle is a loaded, parsed cert + key. It does NOT own file handles
// once Load returns; the file watcher re-reads on every change.
type Bundle struct {
	CertPath string
	KeyPath  string

	Leaf         *x509.Certificate
	Intermediate []*x509.Certificate
	// PrivateKey deliberately untyped here to avoid pinning crypto/* in
	// the stub; the real implementation stores the parsed key.

	NotBefore time.Time
	NotAfter  time.Time
	DNSNames  []string

	// ContentHash is sha256(cert_pem || key_pem). Used by the watcher to
	// detect changes without depending on mtime alone.
	ContentHash [32]byte
}

// MinValidity is the floor below which Validate logs a warning. Below
// zero (already expired), Validate refuses to apply.
const MinValidity = 24 * time.Hour

// Load reads and parses certPath and keyPath. It returns a Bundle with
// every field populated, including ContentHash. It does NOT verify SAN
// coverage or expiry — see Validate for that. It DOES verify that the
// private key matches the leaf certificate.
func Load(certPath, keyPath string) (*Bundle, error) {
	// TODO(impl):
	//   1. os.ReadFile both paths.
	//   2. Walk PEM blocks; first CERTIFICATE block is the leaf, any
	//      further CERTIFICATE blocks are intermediates.
	//   3. Parse the private key from the key file (try PKCS8, then
	//      PKCS1, then EC).
	//   4. Verify the key matches the leaf (public-key equality or a
	//      sign-and-verify round trip).
	//   5. Compute sha256(cert || key) into ContentHash.
	_, _ = certPath, keyPath
	return nil, errNotImplemented
}

// Validate checks the Bundle against an expected community domain.
// Rules (SPEC §9.4):
//   - At least one SAN is `*.expectedDomain` (or every concrete service
//     hostname is present, for non-wildcard certs — admin's choice).
//   - NotAfter > now + MinValidity. If NotAfter is in the past, return
//     ErrExpired so the caller can skip this community.
//   - The leaf + intermediates validate against the system trust store.
//   - The current time is within [NotBefore, NotAfter].
func Validate(b *Bundle, expectedDomain string, now time.Time) error {
	// TODO(impl): see the rule list above.
	_, _, _ = b, expectedDomain, now
	return errNotImplemented
}

// ErrExpired is returned by Validate when NotAfter is already in the past.
// The orchestrator treats this as a hard skip for the affected community
// and surfaces it on the error page.
var ErrExpired = errors.New("cert: expired")

// ErrNoSAN is returned by Validate when no SAN covers the expected domain.
var ErrNoSAN = errors.New("cert: no SAN matches expected domain")

// Pair is one (certPath, keyPath) target the Watcher monitors.
type Pair struct {
	CommunityID string
	CertPath    string
	KeyPath     string
}

// Event is emitted by Watcher.Run when a pair's content changes.
type Event struct {
	CommunityID string
	NewBundle   *Bundle
	Err         error // non-nil if reload itself failed (e.g. mid-write)
}

// Watcher polls a set of cert/key pairs for content changes on a ticker.
// It is intentionally NOT inotify/fsnotify-based: SPEC §9.3 and §10.1
// specify a polling model with `cert_check_interval`.
type Watcher struct {
	Interval time.Duration
	Pairs    []Pair
}

// Run blocks until ctx is cancelled. It re-stats each pair every
// Interval; when any pair's sha256 changes, it emits an Event on the
// returned channel. The channel is closed when ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) <-chan Event {
	// TODO(impl):
	//   - Build an initial snapshot of every pair's ContentHash (calling
	//     Load on each; emit Events for any that fail to load up front).
	//   - On every tick, re-Load each pair; emit Event when hash differs.
	//   - Coalesce simultaneous changes per pair (still one Event each).
	out := make(chan Event)
	go func() {
		defer close(out)
		<-ctx.Done()
	}()
	return out
}

var errNotImplemented = errors.New("cert: not yet implemented")
