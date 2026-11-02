// Package enroll turns a one-time token into a node's fleet certificate: the
// master mints the token, the node redeems it once and verifies the CA
// against a fingerprint carried in the token itself.
package enroll

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
)

// Table holds pending enrollments: key = sha256 of the secret, field node.
const Table = "enroll"

// TokenPrefix marks an enrollment token, so a pasted subscription token is
// refused with a clear error.
const TokenPrefix = "enr1"

var (
	ErrBadToken   = errors.New("enrollment token is malformed")
	ErrUnknown    = errors.New("enrollment token is unknown or already used")
	ErrExpired    = errors.New("enrollment token has expired")
	ErrNode       = errors.New("enrollment token was issued for another node")
	ErrBadCA      = errors.New("the master's CA does not match the token's fingerprint")
	ErrBadCert    = errors.New("the issued certificate does not belong to this node")
	validNodeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// Reader is the projection.
type Reader interface {
	Get(table, key string) (map[string]json.RawMessage, bool)
}

// Writer records and consumes tokens.
type Writer interface {
	Set(ctx context.Context, table, key string, fields map[string]any) error
	Delete(ctx context.Context, table, key string) error
}

// Pin is the CA's fingerprint, hex sha256 over its DER.
func Pin(ca *fleetca.CA) string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Hash is what the op log keeps instead of the secret.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Token is what an operator pastes on a node: the secret, a separator and
// the CA's fingerprint. The fingerprint is the out-of-band channel that
// keeps a man in the middle from substituting its own CA.
func Token(secret string, ca *fleetca.CA) string {
	return TokenPrefix + "_" + secret + "_" + Pin(ca)
}

// Parse splits a token into its secret and fingerprint.
func Parse(token string) (secret, pin string, ok bool) {
	parts := strings.Split(strings.TrimSpace(token), "_")
	if len(parts) != 3 || parts[0] != TokenPrefix || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// Mint records a pending token for node; TTL bounds how long it can be
// redeemed (default 1 h).
func Mint(ctx context.Context, w Writer, secret, node string, ttl time.Duration, now time.Time) error {
	if !validNodeName.MatchString(node) {
		return fmt.Errorf("enroll: bad node name %q", node)
	}
	if secret == "" {
		return errors.New("enroll: empty secret")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return w.Set(ctx, Table, Hash(secret), map[string]any{
		"node": node, "expires": now.Add(ttl).UnixMilli(),
	})
}

// Handler redeems tokens: POST /enroll with the token as a bearer and
// {"node": "..."} in the body. A token works once.
func Handler(ca *fleetca.CA, w Writer, r Reader, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(wr, "POST only", http.StatusMethodNotAllowed)
			return
		}
		secret, _, ok := Parse(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		if !ok {
			http.Error(wr, ErrBadToken.Error(), http.StatusUnauthorized)
			return
		}
		var body struct{ Node string }
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<12)).Decode(&body); err != nil {
			http.Error(wr, "a JSON body {\"node\": \"...\"} is required", http.StatusBadRequest)
			return
		}
		if !validNodeName.MatchString(body.Node) {
			http.Error(wr, "bad node name", http.StatusBadRequest)
			return
		}
		rec, found := r.Get(Table, Hash(secret))
		if !found {
			http.Error(wr, ErrUnknown.Error(), http.StatusUnauthorized)
			return
		}
		var node string
		var expires int64
		_ = json.Unmarshal(rec["node"], &node)
		_ = json.Unmarshal(rec["expires"], &expires)
		if node != body.Node {
			http.Error(wr, ErrNode.Error(), http.StatusForbidden)
			return
		}
		if expires > 0 && now().UnixMilli() > expires {
			_ = w.Delete(req.Context(), Table, Hash(secret))
			http.Error(wr, ErrExpired.Error(), http.StatusUnauthorized)
			return
		}
		cert, err := ca.Issue(wire.NodeCN(body.Node), nil, 365*24*time.Hour)
		if err != nil {
			http.Error(wr, err.Error(), http.StatusInternalServerError)
			return
		}
		// one-time: whoever redeemed it first wins; a loser gets 401 next time
		_ = w.Delete(req.Context(), Table, Hash(secret))
		certPEM, keyPEM, err := keyPEMs(cert)
		if err != nil {
			http.Error(wr, err.Error(), http.StatusInternalServerError)
			return
		}
		caPEM, _, err := ca.PEM()
		if err != nil {
			http.Error(wr, err.Error(), http.StatusInternalServerError)
			return
		}
		wr.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(wr).Encode(map[string]string{
			"node": body.Node, "ca": string(caPEM), "cert": certPEM, "key": keyPEM,
		})
	})
}

func keyPEMs(c tls.Certificate) (certPEM, keyPEM string, err error) {
	k, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})), nil
}

// Result is what the node keeps: the CA, its certificate and key, all PEM.
type Result struct {
	Node          string
	CA, Cert, Key string
}

// Exchange redeems the token at the master and verifies the answer: the CA
// must match the token's fingerprint and the leaf must be this node's.
func Exchange(ctx context.Context, masterURL, token, node string, tlsConf *tls.Config) (Result, error) {
	secret, pin, ok := Parse(token)
	if !ok {
		return Result{}, ErrBadToken
	}
	if !validNodeName.MatchString(node) {
		return Result{}, fmt.Errorf("enroll: bad node name %q", node)
	}
	body, _ := json.Marshal(map[string]string{"node": node})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(masterURL, "/")+"/enroll", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+TokenPrefix+"_"+secret+"_"+pin)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	if tlsConf != nil {
		client.Transport = &http.Transport{TLSClientConfig: tlsConf}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("enroll: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Node string `json:"node"`
		CA   string `json:"ca"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, err
	}
	if err := verify(pin, node, out.CA, out.Cert); err != nil {
		return Result{}, err
	}
	return Result{Node: node, CA: out.CA, Cert: out.Cert, Key: out.Key}, nil
}

func verify(pin, node, caPEM, certPEM string) error {
	cb, _ := pem.Decode([]byte(caPEM))
	if cb == nil {
		return ErrBadCA
	}
	caCert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return ErrBadCA
	}
	sum := sha256.Sum256(caCert.Raw)
	if hex.EncodeToString(sum[:]) != pin {
		return ErrBadCA
	}
	lb, _ := pem.Decode([]byte(certPEM))
	if lb == nil {
		return ErrBadCert
	}
	leaf, err := x509.ParseCertificate(lb.Bytes)
	if err != nil {
		return ErrBadCert
	}
	if leaf.Subject.CommonName != wire.NodeCN(node) {
		return ErrBadCert
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return ErrBadCert
	}
	return nil
}
