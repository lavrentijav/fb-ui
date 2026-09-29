package fleetca

import (
	"crypto/x509"
	"testing"
	"time"
)

// A CA saved to PEM and loaded back still signs certificates that verify
// against the original, for both serving and dialing.
func TestPEMRoundTripKeepsSigning(t *testing.T) {
	ca, err := New("fleet")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.PEM()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := loaded.Issue("ru2", []string{"ru2.firewalk.top", "144.31.61.39"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	for _, use := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "ru2.firewalk.top", KeyUsages: []x509.ExtKeyUsage{use}}); err != nil {
			t.Fatalf("usage %v: %v", use, err)
		}
	}
	if err := leaf.Leaf.VerifyHostname("144.31.61.39"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(keyPEM, certPEM); err == nil {
		t.Fatal("swapped PEM accepted")
	}
}
