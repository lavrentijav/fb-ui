// Package cluster holds the pure building blocks a panel needs to cooperate
// with sibling panels: subscription signing, fallback header assembly and peer
// probing. It is a leaf package — no service, controller or database imports.
package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// SignatureAlgorithm is the value advertised by the peer identity endpoint.
const SignatureAlgorithm = "ed25519"

var (
	ErrBadSeed      = errors.New("cluster: subscription signing seed is not a 32-byte hex string")
	ErrBadPublicKey = errors.New("cluster: subscription public key is not a 32-byte hex string")
	ErrBadSignature = errors.New("cluster: signature is not valid hex")
)

// GenerateKey returns a fresh ed25519 keypair as hex: the 32-byte seed to
// persist privately and the 32-byte public key to publish.
func GenerateKey() (seed string, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(priv.Seed()), hex.EncodeToString(pub), nil
}

// Sign returns the hex ed25519 signature over body for the given hex seed.
func Sign(seed string, body []byte) (string, error) {
	raw, err := hex.DecodeString(seed)
	if err != nil || len(raw) != ed25519.SeedSize {
		return "", ErrBadSeed
	}
	return hex.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(raw), body)), nil
}

// Verify reports whether signature covers body under the hex public key. It is
// the counterpart clients implement, kept here so the tests exercise the real
// pair rather than a re-implementation.
func Verify(public string, signature string, body []byte) error {
	pub, err := hex.DecodeString(public)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return ErrBadPublicKey
	}
	sig, err := hex.DecodeString(signature)
	if err != nil {
		return ErrBadSignature
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), body, sig) {
		return ErrBadSignature
	}
	return nil
}
