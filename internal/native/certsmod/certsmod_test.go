package certsmod_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certsmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module/modtest"
)

func pair(t *testing.T, name string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func do(api http.Handler, method, path, token string) (*httptest.ResponseRecorder, any) {
	req := httptest.NewRequest(method, path, &bytes.Buffer{})
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	var out any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestListNeverShowsKeysAndRollbackWorksThroughTheAPI(t *testing.T) {
	ctx := context.Background()
	h := modtest.NewHost("m1")
	oldC, oldK := pair(t, "old.example")
	newC, newK := pair(t, "new.example")
	_, _ = h.Set(ctx, certs.Table, "g", map[string]any{
		"names": []string{"new.example"}, "issuedFor": []string{"new.example"}, "certPem": newC, "keyPem": newK,
		"prevCertPem": oldC, "prevKeyPem": oldK, "prevIssuedFor": []string{"old.example"},
	})
	admin, _ := apiauth.Create(ctx, h, "root", module.ScopeAdmin, 0)
	api := apiserver.New(h, []module.Module{certsmod.New()})

	rec, _ := do(api, "GET", "/api/v1/certs", admin)
	if rec.Code != 200 || bytes.Contains(rec.Body.Bytes(), []byte("PRIVATE KEY")) || bytes.Contains(rec.Body.Bytes(), []byte("keyPem")) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec, _ := do(api, "POST", "/api/v1/certs/g/rollback", admin); rec.Code != 200 {
		t.Fatalf("rollback = %d %s", rec.Code, rec.Body)
	}
	if got := certs.Resolve(h, []string{"g"}); got[0].CertPEM != oldC {
		t.Fatal("the node would still get the newer pair")
	}
	if rec, _ := do(api, "POST", "/api/v1/certs/nope/rollback", admin); rec.Code != 409 {
		t.Fatalf("rollback of a missing certificate = %d", rec.Code)
	}
}
