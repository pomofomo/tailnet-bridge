package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// genWildcardCert creates a self-signed wildcard cert+key valid for the
// given domain. The cert chain won't verify against the system trust
// store (it's self-signed) — Validate tests use a custom verify path.
func genWildcardCert(t *testing.T, domain string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*." + domain},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{"*." + domain},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	return
}

func writePair(t *testing.T, certPEM, keyPEM []byte) (cp, kp string) {
	t.Helper()
	d := t.TempDir()
	cp = filepath.Join(d, "cert.pem")
	kp = filepath.Join(d, "key.pem")
	if err := os.WriteFile(cp, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

func TestLoad_RSA(t *testing.T) {
	c, k := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	cp, kp := writePair(t, c, k)
	b, err := Load(cp, kp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.DNSNames) != 1 || b.DNSNames[0] != "*.smith.ts.example.com" {
		t.Errorf("DNSNames: %v", b.DNSNames)
	}
	if b.CertPath != cp || b.KeyPath != kp {
		t.Errorf("paths: %+v", b)
	}
	if b.ContentHash == [32]byte{} {
		t.Error("ContentHash zero")
	}
}

func TestLoad_ECDSA(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.smith.ts.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(60 * 24 * time.Hour),
		DNSNames:     []string{"*.smith.ts.example.com"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	k := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	cp, kp := writePair(t, c, k)
	if _, err := Load(cp, kp); err != nil {
		t.Fatalf("Load ECDSA: %v", err)
	}
}

func TestLoad_KeyMismatch(t *testing.T) {
	c, _ := genWildcardCert(t, "smith.ts.example.com",
		time.Now(), time.Now().Add(24*time.Hour))
	_, kOther := genWildcardCert(t, "other.ts.example.com",
		time.Now(), time.Now().Add(24*time.Hour))
	cp, kp := writePair(t, c, kOther)
	_, err := Load(cp, kp)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/cert.pem", "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_Expired(t *testing.T) {
	c, k := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	cp, kp := writePair(t, c, k)
	b, err := Load(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(b, "smith.ts.example.com", time.Now())
	if !errors.Is(err, ErrExpired) {
		t.Errorf("expected ErrExpired, got %v", err)
	}
}

func TestValidate_NoSAN(t *testing.T) {
	c, k := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	cp, kp := writePair(t, c, k)
	b, err := Load(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(b, "wrongdomain.ts.example.com", time.Now())
	if !errors.Is(err, ErrNoSAN) {
		t.Errorf("expected ErrNoSAN, got %v", err)
	}
}

// ValidateWithRoots exposes the chain-verification step with a custom
// root pool; tests below use the self-signed leaf as its own root.
func TestValidateWithRoots_ChainOK(t *testing.T) {
	cPEM, kPEM := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	cp, kp := writePair(t, cPEM, kPEM)
	b, err := Load(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(b.Leaf)
	if err := ValidateWithRoots(b, "smith.ts.example.com", roots, time.Now()); err != nil {
		t.Errorf("ValidateWithRoots with self-as-root should succeed: %v", err)
	}
}

func TestValidateWithRoots_ChainRejected(t *testing.T) {
	cPEM, kPEM := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	cp, kp := writePair(t, cPEM, kPEM)
	b, err := Load(cp, kp)
	if err != nil {
		t.Fatal(err)
	}
	// Empty root pool → chain verification fails.
	if err := ValidateWithRoots(b, "smith.ts.example.com", x509.NewCertPool(), time.Now()); err == nil {
		t.Error("expected chain-verification failure with empty roots")
	}
}

func TestSansCover(t *testing.T) {
	cases := []struct {
		sans []string
		dom  string
		want bool
	}{
		{[]string{"*.smith.ts.example.com"}, "smith.ts.example.com", true},
		{[]string{"*.SMITH.ts.example.com"}, "smith.ts.example.com", true},
		{[]string{"wiki.smith.ts.example.com"}, "smith.ts.example.com", false}, // not the wildcard
		{[]string{}, "smith.ts.example.com", false},
		{[]string{"*.other.example.com"}, "smith.ts.example.com", false},
	}
	for _, tc := range cases {
		if got := sansCover(tc.sans, tc.dom); got != tc.want {
			t.Errorf("sansCover(%v, %q)=%v want %v", tc.sans, tc.dom, got, tc.want)
		}
	}
}

func TestWatcher_DetectsRotation(t *testing.T) {
	c1, k1 := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	cp, kp := writePair(t, c1, k1)

	w := NewWatcher(20*time.Millisecond, []Pair{{CommunityID: "smith", CertPath: cp, KeyPath: kp}})
	bundles, errs := w.Initial()
	if len(errs) != 0 {
		t.Fatalf("Initial errs: %v", errs)
	}
	if bundles["smith"] == nil {
		t.Fatal("missing initial bundle")
	}
	initHash := bundles["smith"].ContentHash

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch := w.Run(ctx)

	// No change yet — first poll after one Interval shouldn't emit.
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event before rotation: %+v", ev)
	case <-time.After(80 * time.Millisecond):
	}

	// Rotate (atomic-ish: write new bytes).
	c2, k2 := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour))
	if err := os.WriteFile(cp, c2, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kp, k2, 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Err != nil {
			t.Fatalf("event err: %v", ev.Err)
		}
		if ev.NewBundle == nil {
			t.Fatal("event missing bundle")
		}
		if ev.NewBundle.ContentHash == initHash {
			t.Error("hash unchanged after rotation")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("watcher did not detect rotation")
	}
}

func TestWatcher_EmitsLoadError(t *testing.T) {
	// Delete the cert files between Initial() and first poll to force a
	// Load error event.
	c, k := genWildcardCert(t, "smith.ts.example.com",
		time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	cp, kp := writePair(t, c, k)
	w := NewWatcher(20*time.Millisecond, []Pair{{CommunityID: "smith", CertPath: cp, KeyPath: kp}})
	_, _ = w.Initial()
	if err := os.Remove(cp); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	ch := w.Run(ctx)
	select {
	case ev := <-ch:
		if ev.Err == nil {
			t.Fatal("expected error event")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no error event")
	}
}
