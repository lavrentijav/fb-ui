package cluster

import (
	"errors"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	seed, public, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	body := []byte("dmxlc3M6Ly9leGFtcGxl\n")

	sig, err := Sign(seed, body)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(public, sig, body); err != nil {
		t.Fatalf("Verify on untouched body: %v", err)
	}

	tampered := append([]byte(nil), body...)
	tampered[0] ^= 0x01
	if err := Verify(public, sig, tampered); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify on tampered body = %v, want ErrBadSignature", err)
	}
}

func TestSignRejectsMalformedSeed(t *testing.T) {
	tests := []struct {
		name string
		seed string
	}{
		{"empty", ""},
		{"not hex", "zzzz"},
		{"too short", "00112233"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Sign(tt.seed, []byte("x")); !errors.Is(err, ErrBadSeed) {
				t.Fatalf("Sign(%q) error = %v, want ErrBadSeed", tt.seed, err)
			}
		})
	}
}

func TestVerifyRejectsMalformedInputs(t *testing.T) {
	seed, public, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig, err := Sign(seed, []byte("body"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if err := Verify("beef", sig, []byte("body")); !errors.Is(err, ErrBadPublicKey) {
		t.Fatalf("Verify with short key = %v, want ErrBadPublicKey", err)
	}
	if err := Verify(public, "nothex", []byte("body")); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify with non-hex signature = %v, want ErrBadSignature", err)
	}
}

// A second key must never validate the first key's signature — the guard that
// makes the header meaningful when several panels serve the same subscription.
func TestVerifyRejectsForeignKey(t *testing.T) {
	seedA, _, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey A: %v", err)
	}
	_, publicB, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey B: %v", err)
	}
	sig, err := Sign(seedA, []byte("body"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(publicB, sig, []byte("body")); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify with foreign key = %v, want ErrBadSignature", err)
	}
}
