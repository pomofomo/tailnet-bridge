// Package fallbackcert generates and persists a self-signed TLS cert
// used when a community's wildcard cert is missing or invalid.
//
// Without a usable cert, Caddy could not complete the TLS handshake at
// all and the user would see an opaque browser/network error. With this
// fallback, the bridge still binds the listener for the community, the
// browser shows a "not trusted" cert warning, and after the user clicks
// through they reach the friendly `/__bridge_error` page that explains
// what to do (SPEC §12.1).
//
// The fallback cert is generated once per state directory and reused
// across restarts. It carries a SAN that makes its purpose obvious:
// "tailnet-bridge-fallback".
package fallbackcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	certName   = "fallback-cert.pem"
	keyName    = "fallback-key.pem"
	subjectCN  = "tailnet-bridge-fallback"
	validYears = 10
)

// Paths is the on-disk pair the orchestrator passes to Caddy.
type Paths struct {
	CertPath string
	KeyPath  string
}

// Ensure makes sure a self-signed fallback cert/key pair exists under
// stateDir. If both files exist and parse as a still-valid cert with a
// matching private key, the existing paths are returned. Otherwise a
// new pair is generated and written.
func Ensure(stateDir string) (Paths, error) {
	if stateDir == "" {
		return Paths{}, errors.New("fallbackcert: empty stateDir")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("fallbackcert: mkdir %s: %w", stateDir, err)
	}
	p := Paths{
		CertPath: filepath.Join(stateDir, certName),
		KeyPath:  filepath.Join(stateDir, keyName),
	}
	if reusable(p) {
		return p, nil
	}
	if err := generate(p); err != nil {
		return Paths{}, err
	}
	return p, nil
}

// reusable reports whether the on-disk pair is well-formed and the cert
// has not expired. Anything else (missing files, parse errors, expired
// cert) → regenerate.
func reusable(p Paths) bool {
	cb, err := os.ReadFile(p.CertPath)
	if err != nil {
		return false
	}
	kb, err := os.ReadFile(p.KeyPath)
	if err != nil {
		return false
	}
	cBlk, _ := pem.Decode(cb)
	if cBlk == nil || cBlk.Type != "CERTIFICATE" {
		return false
	}
	c, err := x509.ParseCertificate(cBlk.Bytes)
	if err != nil {
		return false
	}
	if time.Now().After(c.NotAfter) {
		return false
	}
	kBlk, _ := pem.Decode(kb)
	if kBlk == nil {
		return false
	}
	switch kBlk.Type {
	case "PRIVATE KEY", "EC PRIVATE KEY", "RSA PRIVATE KEY":
		return true
	default:
		return false
	}
}

func generate(p Paths) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("fallbackcert: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("fallbackcert: serial: %w", err)
	}
	notBefore := time.Now().Add(-time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: subjectCN},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(validYears * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{subjectCN},
		IsCA:                  false,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("fallbackcert: sign: %w", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("fallbackcert: marshal key: %w", err)
	}
	if err := writeFile(p.CertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	if err := writeFile(p.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		return err
	}
	return nil
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return fmt.Errorf("fallbackcert: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("fallbackcert: rename %s: %w", path, err)
	}
	return nil
}
