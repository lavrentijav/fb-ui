package certreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePair(t *testing.T, dir, cn string, mod time.Time) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	certPath, keyPath := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	_ = os.Chtimes(certPath, mod, mod)
	_ = os.Chtimes(keyPath, mod, mod)
}

func servedCN(t *testing.T, r *Reloader) string {
	t.Helper()
	c, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.Subject.CommonName
}

func TestReloaderPicksUpRenewedPair(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writePair(t, dir, "old", base)
	r, err := New(filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem"))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	r.now = func() time.Time { return clock }
	if got := servedCN(t, r); got != "old" {
		t.Fatalf("initial CN = %q", got)
	}

	writePair(t, dir, "new", base.Add(time.Minute))
	if got := servedCN(t, r); got != "old" {
		t.Fatalf("re-read before the check interval: CN = %q", got)
	}
	clock = clock.Add(checkEvery)
	if got := servedCN(t, r); got != "new" {
		t.Fatalf("after renewal CN = %q, want new", got)
	}

	_ = os.WriteFile(filepath.Join(dir, "privkey.pem"), []byte("garbage"), 0o600)
	clock = clock.Add(checkEvery)
	if got := servedCN(t, r); got != "new" {
		t.Fatalf("a broken key must keep the last good pair, CN = %q", got)
	}
}
