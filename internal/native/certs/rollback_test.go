package certs_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func pair(t *testing.T, name string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func seed(t *testing.T, h *modtest.Host, id string, fields map[string]any) {
	t.Helper()
	if _, err := h.Set(context.Background(), certs.Table, id, fields); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackSwapsThePairAndHoldsRenewal(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	h.Clock = func() time.Time { return now }
	oldC, oldK := pair(t, "old.example", now.Add(20*24*time.Hour))
	newC, newK := pair(t, "new.example", now.Add(90*24*time.Hour))
	seed(t, h, "g", map[string]any{
		"names": []string{"new.example"}, "issuedFor": []string{"new.example"}, "certPem": newC, "keyPem": newK,
		"notAfter":    now.Add(90 * 24 * time.Hour).UnixMilli(),
		"prevCertPem": oldC, "prevKeyPem": oldK, "prevIssuedFor": []string{"old.example"},
		"prevNotAfter": now.Add(20 * 24 * time.Hour).UnixMilli(),
	})
	if err := certs.Rollback(ctx, h, h, "g", now); err != nil {
		t.Fatal(err)
	}
	got := certs.Resolve(h, []string{"g"})
	if len(got) != 1 || got[0].CertPEM != oldC || got[0].Names[0] != "old.example" {
		t.Fatalf("after rollback the node would get %+v", got)
	}
	rec, _ := h.Get(certs.Table, "g")
	if string(rec["prevCertPem"]) == "" || certs.HeldUntil(rec) <= now.UnixMilli() {
		t.Fatalf("the replaced pair must be kept and renewal held: %v", rec)
	}
	// a second rollback undoes the first
	if err := certs.Rollback(ctx, h, h, "g", now); err != nil {
		t.Fatal(err)
	}
	if got := certs.Resolve(h, []string{"g"}); got[0].CertPEM != newC {
		t.Fatal("the second rollback did not restore the newer pair")
	}
}

func TestRollbackRefusesWithoutAUsableEarlierPair(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	c, k := pair(t, "a.example", time.Now().Add(90*24*time.Hour))
	seed(t, h, "none", map[string]any{"names": []string{"a.example"}, "certPem": c, "keyPem": k})
	seed(t, h, "junk", map[string]any{"names": []string{"a.example"}, "certPem": c, "keyPem": k, "prevCertPem": "not pem", "prevKeyPem": "x"})
	for _, id := range []string{"none", "junk", "missing"} {
		if err := certs.Rollback(ctx, h, h, id, time.Now()); err == nil {
			t.Errorf("%s: rollback accepted", id)
		}
	}
	if got := certs.Resolve(h, []string{"junk"}); got[0].CertPEM != c {
		t.Fatal("a refused rollback changed the certificate")
	}
}
