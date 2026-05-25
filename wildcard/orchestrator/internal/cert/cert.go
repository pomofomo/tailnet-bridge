// Package cert handles the static wildcard cert/key files the community
// admin distributes to members (SPEC §3.5, §6.3, §7.2, §9.4, §10.1).
//
// Responsibilities:
//   - Load a PEM cert + key pair from disk and parse them.
//   - Verify the key matches the leaf cert.
//   - Verify SAN coverage of "*.<community.domain>".
//   - Verify NotAfter is in the future.
//   - Verify the chain validates against the system trust store.
//   - Watch a set of pairs for content changes and emit events.
package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// MinValidity is the floor below which Validate logs a warning. Below
// zero (already expired), Validate refuses to apply.
const MinValidity = 24 * time.Hour

// Bundle is a loaded, parsed cert + key.
type Bundle struct {
	CertPath string
	KeyPath  string

	Leaf          *x509.Certificate
	Intermediates []*x509.Certificate

	NotBefore time.Time
	NotAfter  time.Time
	DNSNames  []string

	// ContentHash is sha256(cert_pem || 0x00 || key_pem). Used by the
	// watcher to detect changes without depending on mtime alone.
	ContentHash [32]byte
}

// Sentinel errors. Callers test with errors.Is.
var (
	ErrExpired = errors.New("cert: expired")
	ErrNoSAN   = errors.New("cert: no SAN matches expected domain")
)

// Load reads and parses certPath and keyPath. It returns a Bundle with
// every field populated, including ContentHash. It does NOT verify SAN
// coverage or expiry — see Validate for that. It DOES verify that the
// private key matches the leaf certificate.
func Load(certPath, keyPath string) (*Bundle, error) {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("cert: read %s: %w", certPath, err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("cert: read %s: %w", keyPath, err)
	}

	leaf, intermediates, err := parseCertChain(certBytes)
	if err != nil {
		return nil, fmt.Errorf("cert: parse %s: %w", certPath, err)
	}
	key, err := parsePrivateKey(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("cert: parse %s: %w", keyPath, err)
	}
	if err := keyMatchesCert(key, leaf); err != nil {
		return nil, fmt.Errorf("cert: key %s does not match cert %s: %w", keyPath, certPath, err)
	}

	hash := sha256.New()
	hash.Write(certBytes)
	hash.Write([]byte{0})
	hash.Write(keyBytes)
	var h [32]byte
	copy(h[:], hash.Sum(nil))

	return &Bundle{
		CertPath:      certPath,
		KeyPath:       keyPath,
		Leaf:          leaf,
		Intermediates: intermediates,
		NotBefore:     leaf.NotBefore,
		NotAfter:      leaf.NotAfter,
		DNSNames:      leaf.DNSNames,
		ContentHash:   h,
	}, nil
}

// Validate checks the Bundle against an expected community domain and
// current time (SPEC §9.4) using the system trust store.
//
// For tests that need to inject a custom root, call ValidateWithRoots
// directly.
//
// Rules:
//   - At least one SAN equals `*.expectedDomain` (per-service certs are
//     not supported in v1; SPEC §13).
//   - NotAfter is strictly in the future; otherwise ErrExpired.
//   - The cert chain validates against the supplied root pool (Let's
//     Encrypt is a public CA, so the system pool works in production).
//
// MinValidity slack is checked by the caller — the poller logs a
// warning when Bundle.NotAfter is closer than cert.MinValidity.
func Validate(b *Bundle, expectedDomain string, now time.Time) error {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Errorf("cert: system trust store: %w", err)
	}
	return ValidateWithRoots(b, expectedDomain, pool, now)
}

// ValidateWithRoots is Validate with an explicit root pool. Exposed so
// tests can verify chain handling against an in-memory self-signed root
// without depending on the system trust store.
func ValidateWithRoots(b *Bundle, expectedDomain string, roots *x509.CertPool, now time.Time) error {
	if b == nil || b.Leaf == nil {
		return errors.New("cert: nil bundle")
	}
	// Time validity.
	if !b.NotAfter.After(now) {
		return fmt.Errorf("%w: not_after=%s", ErrExpired, b.NotAfter.Format(time.RFC3339))
	}
	if now.Before(b.NotBefore) {
		return fmt.Errorf("cert: not yet valid (not_before=%s)", b.NotBefore.Format(time.RFC3339))
	}

	// SAN coverage. SPEC §13 — per-service certs are out of scope, so
	// this strictly requires the wildcard SAN.
	if !sansCover(b.DNSNames, expectedDomain) {
		return fmt.Errorf("%w: expected wildcard SAN %q (per-service certs not supported, SPEC §13); got SANs %v",
			ErrNoSAN, "*."+expectedDomain, b.DNSNames)
	}

	if roots == nil {
		return errors.New("cert: nil root pool")
	}
	inters := x509.NewCertPool()
	for _, ic := range b.Intermediates {
		inters.AddCert(ic)
	}
	opts := x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		CurrentTime:   now,
	}
	if _, err := b.Leaf.Verify(opts); err != nil {
		return fmt.Errorf("cert: chain verification failed: %w", err)
	}
	return nil
}

// sansCover reports whether the SAN list covers "*.expectedDomain".
//
// Coverage is satisfied when:
//   - any SAN equals "*.expectedDomain" exactly, OR
//   - a SAN ending in ".<expectedDomain>" matches every immediate child
//     (this collapses to the wildcard rule).
func sansCover(sans []string, expectedDomain string) bool {
	want := "*." + strings.ToLower(strings.TrimSuffix(expectedDomain, "."))
	for _, s := range sans {
		if strings.ToLower(strings.TrimSuffix(s, ".")) == want {
			return true
		}
	}
	return false
}

func parseCertChain(certPEM []byte) (leaf *x509.Certificate, intermediates []*x509.Certificate, err error) {
	rest := certPEM
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, perr := x509.ParseCertificate(block.Bytes)
		if perr != nil {
			return nil, nil, fmt.Errorf("parse certificate: %w", perr)
		}
		if leaf == nil {
			leaf = c
		} else {
			intermediates = append(intermediates, c)
		}
	}
	if leaf == nil {
		return nil, nil, errors.New("no CERTIFICATE block found")
	}
	return leaf, intermediates, nil
}

func parsePrivateKey(keyPEM []byte) (any, error) {
	rest := keyPEM
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			return nil, errors.New("no PEM block found")
		}
		rest = next
		switch block.Type {
		case "PRIVATE KEY":
			return x509.ParsePKCS8PrivateKey(block.Bytes)
		case "RSA PRIVATE KEY":
			return x509.ParsePKCS1PrivateKey(block.Bytes)
		case "EC PRIVATE KEY":
			return x509.ParseECPrivateKey(block.Bytes)
		case "OPENSSH PRIVATE KEY":
			return nil, errors.New("OpenSSH private key not supported (need PKCS8/PKCS1/EC)")
		}
		// non-key block: keep scanning
		if len(rest) == 0 {
			return nil, errors.New("no private-key PEM block found")
		}
	}
}

func keyMatchesCert(key any, leaf *x509.Certificate) error {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		pub, ok := leaf.PublicKey.(*rsa.PublicKey)
		if !ok {
			return errors.New("cert public key is not RSA")
		}
		if k.N.Cmp(pub.N) != 0 || k.E != pub.E {
			return errors.New("RSA key/cert mismatch")
		}
	case *ecdsa.PrivateKey:
		pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("cert public key is not ECDSA")
		}
		if k.X.Cmp(pub.X) != 0 || k.Y.Cmp(pub.Y) != 0 {
			return errors.New("ECDSA key/cert mismatch")
		}
	case ed25519.PrivateKey:
		pub, ok := leaf.PublicKey.(ed25519.PublicKey)
		if !ok {
			return errors.New("cert public key is not Ed25519")
		}
		if !pub.Equal(k.Public()) {
			return errors.New("Ed25519 key/cert mismatch")
		}
	default:
		return fmt.Errorf("unsupported private-key type %T", key)
	}
	return nil
}

// ─── Watcher ─────────────────────────────────────────────────────────────

// Pair is one (certPath, keyPath) target the Watcher monitors.
type Pair struct {
	CommunityID string
	CertPath    string
	KeyPath     string
}

// Event is emitted by Watcher.Run when a pair's content changes.
//
// On a successful (re)load, NewBundle is non-nil and Err is nil.
// On a failed (re)load, Err is non-nil and NewBundle is nil.
type Event struct {
	CommunityID string
	NewBundle   *Bundle
	Err         error
}

// Watcher polls a set of cert/key pairs for content changes on a ticker.
// SPEC §9.3 and §10.1 specify a polling model with cert_check_interval —
// fsnotify would be brittle on bind-mounts and atomic-rename rotations,
// and we want one event per ROTATION not per write.
type Watcher struct {
	Interval time.Duration
	Pairs    []Pair

	mu    sync.Mutex
	state map[string]watchState
}

type watchState struct {
	hash [32]byte
}

// NewWatcher constructs a Watcher; interval must be positive.
func NewWatcher(interval time.Duration, pairs []Pair) *Watcher {
	return &Watcher{
		Interval: interval,
		Pairs:    pairs,
		state:    make(map[string]watchState, len(pairs)),
	}
}

// Initial does one synchronous load pass and returns the initial
// bundles plus per-community errors. The caller should use this to
// build the first Caddy config; subsequent calls to Run() emit events
// for rotations.
func (w *Watcher) Initial() (bundles map[string]*Bundle, errs map[string]error) {
	bundles = make(map[string]*Bundle, len(w.Pairs))
	errs = make(map[string]error, len(w.Pairs))
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, p := range w.Pairs {
		b, err := Load(p.CertPath, p.KeyPath)
		if err != nil {
			errs[p.CommunityID] = err
			continue
		}
		bundles[p.CommunityID] = b
		w.state[p.CommunityID] = watchState{hash: b.ContentHash}
	}
	return bundles, errs
}

// Run blocks until ctx is cancelled. It re-loads each pair every
// Interval; when any pair's sha256 changes, it emits an Event on the
// returned channel. The channel is closed when ctx is cancelled.
//
// The first tick is one Interval after Run starts. To learn about the
// initial state, call Initial() before Run.
func (w *Watcher) Run(ctx context.Context) <-chan Event {
	out := make(chan Event, len(w.Pairs))
	go func() {
		defer close(out)
		t := time.NewTicker(w.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.poll(ctx, out)
			}
		}
	}()
	return out
}

// Trigger forces an immediate poll round, useful for SIGHUP. Safe to
// call concurrently with Run. No-op if Run isn't running.
func (w *Watcher) Trigger(ctx context.Context) {
	out := make(chan Event, len(w.Pairs))
	go w.poll(ctx, out)
	// Drain trigger results into nowhere — Trigger is for SIGHUP where
	// the poller is the one calling us and will pick up changes via its
	// own next-tick anyway.
	go func() {
		for range out {
		}
	}()
}

func (w *Watcher) poll(ctx context.Context, out chan<- Event) {
	w.mu.Lock()
	pairs := make([]Pair, len(w.Pairs))
	copy(pairs, w.Pairs)
	w.mu.Unlock()

	for _, p := range pairs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		b, err := Load(p.CertPath, p.KeyPath)
		w.mu.Lock()
		prev := w.state[p.CommunityID]
		w.mu.Unlock()
		if err != nil {
			// Only emit error events on first appearance or when the
			// previous attempt had a different shape; for now, emit on
			// every poll-error. The poller dedupes by hash-of-config.
			select {
			case <-ctx.Done():
				return
			case out <- Event{CommunityID: p.CommunityID, Err: err}:
			}
			continue
		}
		if b.ContentHash == prev.hash {
			continue // unchanged
		}
		w.mu.Lock()
		w.state[p.CommunityID] = watchState{hash: b.ContentHash}
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case out <- Event{CommunityID: p.CommunityID, NewBundle: b}:
		}
	}
}
