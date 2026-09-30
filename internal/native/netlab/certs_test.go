//go:build netlab

package netlab

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/dnsprovider"
	"github.com/mhsanaei/3x-ui/v3/internal/native/certs"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const labZone = "lab.firewalk.test"

// challtest writes TXT records into challtestsrv, the DNS Pebble asks.
type challtest struct {
	base string
	mu   sync.Mutex
}

func (p *challtest) post(path string, body map[string]string) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(p.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv %s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

func (p *challtest) SetRecords(_ context.Context, name, _ string, values []string, _ int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	host := strings.TrimSuffix(name, ".") + "."
	if err := p.post("/clear-txt", map[string]string{"host": host}); err != nil {
		return err
	}
	for _, v := range values {
		if err := p.post("/set-txt", map[string]string{"host": host, "value": v}); err != nil {
			return err
		}
	}
	return nil
}

func (p *challtest) DeleteRecords(ctx context.Context, name, rtype string) error {
	return p.SetRecords(ctx, name, rtype, nil, 0)
}

func (p *challtest) Check(context.Context) error { return nil }

// labACME is the masters' ACME in the lab: Pebble with challtestsrv as the
// zone's DNS; nil when the test did not start them.
func labACME() (*certs.ACME, error) {
	dir := os.Getenv("NETLAB_ACME")
	if dir == "" {
		return nil, nil
	}
	caPEM, err := os.ReadFile(os.Getenv("NETLAB_ACME_CA"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	dns := &challtest{base: os.Getenv("NETLAB_CHALLTEST")}
	return &certs.ACME{
		Directory: dir, Email: "ops@firewalk.test",
		DNSFor: func(name string) (dnsprovider.Provider, string, error) {
			if !dnsprovider.InZone(name, labZone) {
				return nil, "", fmt.Errorf("no DNS account holds %s", name)
			}
			return dns, labZone, nil
		},
		// challtestsrv answers from memory; nothing to wait for
		Propagated: func(context.Context, string, string, []string) error { return nil },
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}},
		Logf:       func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}, nil
}

// startACME runs Pebble and challtestsrv on the acme host and returns the
// masters' environment and the pool that trusts what Pebble issues.
func startACME(t *testing.T, dir string) ([]string, *x509.CertPool) {
	t.Helper()
	pebble, chall, certsDir := os.Getenv("NETLAB_PEBBLE"), os.Getenv("NETLAB_CHALLTESTSRV"), os.Getenv("NETLAB_PEBBLE_CERTS")
	if pebble == "" || chall == "" || certsDir == "" {
		t.Skip("NETLAB_PEBBLE, NETLAB_CHALLTESTSRV and NETLAB_PEBBLE_CERTS not set")
	}
	ip := ipOf("acme")
	spawnCmd(t, "acme", nil, chall, "-dnsserver", ip+":8053", "-management", ip+":8055",
		"-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")
	cfg := filepath.Join(dir, "pebble.json")
	writeJSON(t, cfg, map[string]any{"pebble": map[string]any{
		"listenAddress": ip + ":14000", "managementListenAddress": ip + ":15000",
		"certificate": filepath.Join(certsDir, "localhost/cert.pem"), "privateKey": filepath.Join(certsDir, "localhost/key.pem"),
		"httpPort": 5002, "tlsPort": 5001, "keyAlgorithm": "ecdsa",
		"profiles": map[string]any{"default": map[string]any{"validityPeriod": 7776000}},
	}})
	spawnCmd(t, "acme", []string{"PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0"},
		pebble, "-config", cfg, "-dnsserver", ip+":8053")
	waitTCP(t, ip+":8055")
	waitTCP(t, ip+":14000")

	minica := filepath.Join(certsDir, "pebble.minica.pem")
	caPEM, err := os.ReadFile(minica)
	if err != nil {
		t.Fatal(err)
	}
	api := x509.NewCertPool()
	api.AppendCertsFromPEM(caPEM)
	// Pebble makes a new root every run; ask it which
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: api}}}
	resp, err := cl.Get("https://pebble:15000/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	issued := x509.NewCertPool()
	if !issued.AppendCertsFromPEM(root) {
		t.Fatalf("pebble root: %q", root)
	}
	return []string{
		"NETLAB_ACME=https://pebble:14000/dir", "NETLAB_ACME_CA=" + minica,
		"NETLAB_CHALLTEST=http://" + ip + ":8055",
	}, issued
}

// served dials node:443 with sni and checks the certificate chains to
// roots; it returns the names the certificate holds.
func served(node, sni string, roots *x509.CertPool) ([]string, error) {
	d := &net.Dialer{Timeout: 2 * time.Second}
	c, err := tls.DialWithDialer(d, "tcp", ipOf(node)+":443", &tls.Config{ServerName: sni, RootCAs: roots})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].DNSNames, nil
}

func (f *fleet) putCert(t *testing.T, via, id string, names ...string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"names": names})
	req, _ := http.NewRequest(http.MethodPut, "http://"+ipOf(masterHosts[via])+":8080/certs/"+id, bytes.NewReader(raw))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("put cert %s via %s: %d %s", id, via, resp.StatusCode, b)
	}
}

func certsOn(via string) ([]certs.Status, error) {
	resp, err := http.Get("http://" + ipOf(masterHosts[via]) + ":8080/certs")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []certs.Status
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// A group wildcard and ru4's own names are issued against Pebble and reach
// every master and ru4; a new route re-issues ru4's certificate.
func TestMastersIssueCerts(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	env, pebbleRoots := startACME(t, f.dir)
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t, env...)

	site := func(names ...string) (out []nodespec.Reverse) {
		for _, n := range names {
			out = append(out, nodespec.Reverse{Kind: "http", SNI: n, Target: "http://web.lab.test"})
		}
		return out
	}
	servedAs := func(sni, wantName string) func() (bool, string) {
		return func() (bool, string) {
			names, err := served("ru4", sni, pebbleRoots)
			if err != nil {
				return false, err.Error()
			}
			return slices.Contains(names, wantName), fmt.Sprint(names)
		}
	}
	ru4 := nodespec.Spec{
		Node:    "ru4",
		Reverse: site("ru.lab.firewalk.test", "panel.ru.lab.firewalk.test", "ru4.lab.firewalk.test"),
		CertIDs: []string{"ru-group"},
	}
	// the spec before the group certificate exists: its names first go to
	// ru4's own certificate, the next plan hands them back to the group
	f.putSpec(t, "fi1", ru4)
	f.putCert(t, "ru2", "ru-group", "ru.lab.firewalk.test", "*.ru.lab.firewalk.test")
	f.startNode(t, "ru4")

	eventually(t, 90*time.Second, "ru4 serves the group wildcard", servedAs("panel.ru.lab.firewalk.test", "*.ru.lab.firewalk.test"))
	eventually(t, 90*time.Second, "ru4 serves its own certificate", servedAs("ru4.lab.firewalk.test", "ru4.lab.firewalk.test"))
	holders := map[string]string{}
	for m := range masterHosts {
		eventually(t, 30*time.Second, "master "+m+" holds both certificates", func() (bool, string) {
			list, err := certsOn(m)
			if err != nil || len(list) != 2 {
				return false, fmt.Sprint(list, err)
			}
			seen := ""
			for _, c := range list {
				if c.NotAfter < time.Now().UnixMilli() || !slices.Equal(c.Names, c.IssuedFor) {
					return false, fmt.Sprint(list)
				}
				seen += c.ID + "=" + c.Holder + " "
			}
			holders[m] = seen
			return true, ""
		})
	}
	if holders["fi1"] != holders["ru2"] || holders["ru2"] != holders["de2"] {
		t.Fatalf("masters disagree on who issued: %v", holders)
	}
	t.Logf("issued: %s", holders["fi1"])

	if _, err := served("ru4", "shop.lab.firewalk.test", pebbleRoots); err == nil {
		t.Fatal("a name no route has verified")
	}
	ru4.Reverse = append(ru4.Reverse, site("shop.lab.firewalk.test")...)
	f.putSpec(t, "de2", ru4)
	eventually(t, 90*time.Second, "the re-issued node certificate reaches ru4", servedAs("shop.lab.firewalk.test", "shop.lab.firewalk.test"))
	eventually(t, 10*time.Second, "ru4's name stays in it", servedAs("ru4.lab.firewalk.test", "shop.lab.firewalk.test"))
}
