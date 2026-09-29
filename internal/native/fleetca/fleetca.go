// Package fleetca is the fleet's private CA: masters (and later agents) get
// ed25519 certificates from it and accept only peers it signed.
package fleetca

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

type CA struct {
	Cert *x509.Certificate
	key  ed25519.PrivateKey
}

// New makes a CA valid for ten years.
func New(name string) (*CA, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, key: key}, nil
}

// Load reads a CA written by PEM.
func Load(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("fleetca: bad PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok || !cert.IsCA {
		return nil, errors.New("fleetca: not an ed25519 CA")
	}
	return &CA{Cert: cert, key: key}, nil
}

// PEM returns the CA certificate and key; the key never leaves the masters.
func (ca *CA) PEM() (certPEM, keyPEM []byte, err error) {
	k, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k}), nil
}

// Issue signs a certificate for a member named name, reachable at hosts
// (DNS names or IPs), usable both to serve and to dial.
func (ca *CA) Issue(name string, hosts []string, valid time.Duration) (tls.Certificate, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(valid),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, pub, ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("fleetca: issue %s: %w", name, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// MutualTLS accepts and dials only peers signed by ca.
func (ca *CA) MutualTLS(own tls.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{own},
		RootCAs: pool, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert,
	}
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}
