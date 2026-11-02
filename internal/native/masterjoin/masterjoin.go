// Package masterjoin lets a new master join a fleet with a one-time token: an
// existing master mints it, the new one redeems it for a gateway certificate
// signed by the fleet CA, and the token's own fingerprint pins that CA.
//
// It mirrors enroll (the node flow) so one operator habit covers both: mint a
// token on a live master, paste it on the new host.
package masterjoin

import (
	"bytes"
	"context"
	"crypto/rand"
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
)

// Table holds pending joins: key = sha256 of the secret, field name.
const Table = "masterjoin"

// TokenPrefix marks a master join token, so a node token pasted here is
// refused with a clear error.
const TokenPrefix = "mst1"

var (
	ErrBadToken = errors.New("master join token is malformed")
	ErrUnknown  = errors.New("master join token is unknown or already used")
	ErrExpired  = errors.New("master join token has expired")
	ErrName     = errors.New("master join token was issued for another master")
	ErrBadCA    = errors.New("the master's CA does not match the token's fingerprint")
	ErrBadCert  = errors.New("the issued certificate does not belong to this master")
	validName   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
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

// Pin is the fleet CA's fingerprint (hex sha256 over its DER).
func Pin(ca *fleetca.CA) string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Hash is what the op log keeps instead of the secret.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Token is what the operator copies: secret, separator, CA fingerprint. The
// fingerprint is the out-of-band channel that stops a man in the middle from
// substituting its own CA.
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

// NewSecret makes a random token secret.
func NewSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Mint records a pending join for name; 0 ttl means one hour.
func Mint(ctx context.Context, w Writer, secret, name string, ttl time.Duration, now time.Time) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("masterjoin: bad master name %q", name)
	}
	if secret == "" {
		return errors.New("masterjoin: empty secret")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return w.Set(ctx, Table, Hash(secret), map[string]any{
		"name": name, "expires": now.Add(ttl).UnixMilli(),
	})
}

// Result is what the joining master keeps.
type Result struct {
	Name string `json:"name"`
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

// RedeemSecret issues name's gateway certificate and consumes the token. The
// certificate is the master's identity for both the bus gateways and the
// client port's mTLS.
func RedeemSecret(ctx context.Context, ca *fleetca.CA, w Writer, r Reader, name, secret string, hosts []string, now time.Time) (Result, error) {
	if !validName.MatchString(name) {
		return Result{}, fmt.Errorf("masterjoin: bad master name %q", name)
	}
	rec, found := r.Get(Table, Hash(secret))
	if !found {
		return Result{}, ErrUnknown
	}
	var pending string
	var expires int64
	_ = json.Unmarshal(rec["name"], &pending)
	_ = json.Unmarshal(rec["expires"], &expires)
	if pending != name {
		return Result{}, ErrName
	}
	if expires > 0 && now.UnixMilli() > expires {
		_ = w.Delete(ctx, Table, Hash(secret))
		return Result{}, ErrExpired
	}
	cert, err := ca.Issue(name, hosts, 2*365*24*time.Hour)
	if err != nil {
		return Result{}, err
	}
	_ = w.Delete(ctx, Table, Hash(secret)) // one-time
	certPEM, keyPEM, err := keyPEMs(cert)
	if err != nil {
		return Result{}, err
	}
	caPEM, _, err := ca.PEM()
	if err != nil {
		return Result{}, err
	}
	return Result{Name: name, CA: string(caPEM), Cert: certPEM, Key: keyPEM}, nil
}

// Handler redeems a join token: POST /master/join with the token as a bearer
// and {"name": "...", "hosts": [...]} in the body.
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
		var body struct {
			Name  string   `json:"name"`
			Hosts []string `json:"hosts"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<14)).Decode(&body); err != nil {
			http.Error(wr, `a JSON body {"name": "...", "hosts": [...]} is required`, http.StatusBadRequest)
			return
		}
		res, err := RedeemSecret(req.Context(), ca, w, r, body.Name, secret, body.Hosts, now())
		switch {
		case err == nil:
		case errors.Is(err, ErrUnknown), errors.Is(err, ErrExpired):
			http.Error(wr, err.Error(), http.StatusUnauthorized)
			return
		case errors.Is(err, ErrName):
			http.Error(wr, err.Error(), http.StatusForbidden)
			return
		default:
			http.Error(wr, err.Error(), http.StatusBadRequest)
			return
		}
		wr.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(wr).Encode(res)
	})
}

// Exchange redeems the token at a live master and verifies the answer: the CA
// must match the token's fingerprint and the leaf must be this master's.
func Exchange(ctx context.Context, masterURL, token, name string, hosts []string, tlsConf *tls.Config) (Result, error) {
	secret, pin, ok := Parse(token)
	if !ok {
		return Result{}, ErrBadToken
	}
	if !validName.MatchString(name) {
		return Result{}, fmt.Errorf("masterjoin: bad master name %q", name)
	}
	body, _ := json.Marshal(map[string]any{"name": name, "hosts": hosts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(masterURL, "/")+"/master/join", bytes.NewReader(body))
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
		return Result{}, fmt.Errorf("masterjoin: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return Result{}, err
	}
	if err := verify(pin, name, out.CA, out.Cert); err != nil {
		return Result{}, err
	}
	return out, nil
}

func verify(pin, name, caPEM, certPEM string) error {
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
	if leaf.Subject.CommonName != name {
		return ErrBadCert
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		return ErrBadCert
	}
	return nil
}

func keyPEMs(c tls.Certificate) (certPEM, keyPEM string, err error) {
	k, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})), nil
}
