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

// A wildcard asked for through ru2 is issued once against Pebble, reaches every
// master and ru4; a name added through de2 re-issues and reaches ru4 too.
func TestMastersIssueCerts(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	env, pebbleRoots := startACME(t, f.dir)
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t, env...)

	f.putCert(t, "ru2", "ru-group", "ru.lab.firewalk.test", "*.ru.lab.firewalk.test")
	f.putSpec(t, "fi1", nodespec.Spec{
		Node: "ru4",
		Reverse: []nodespec.Reverse{
			{Kind: "http", SNI: "ru.lab.firewalk.test", Target: "http://web.lab.test"},
			{Kind: "http", SNI: "ru4.lab.firewalk.test", Target: "http://web.lab.test"},
		},
		CertIDs: []string{"ru-group"},
	})
	f.startNode(t, "ru4")

	eventually(t, 90*time.Second, "ru4 serves the group certificate from Pebble", func() (bool, string) {
		names, err := served("ru4", "ru.lab.firewalk.test", pebbleRoots)
		if err != nil {
			return false, err.Error()
		}
		return slices.Contains(names, "*.ru.lab.firewalk.test"), fmt.Sprint(names)
	})
	holders := map[string]string{}
	for m := range masterHosts {
		eventually(t, 30*time.Second, "master "+m+" holds the certificate", func() (bool, string) {
			list, err := certsOn(m)
			if err != nil || len(list) != 1 {
				return false, fmt.Sprint(list, err)
			}
			holders[m] = list[0].Holder
			return list[0].NotAfter > time.Now().UnixMilli() && len(list[0].IssuedFor) == 2, fmt.Sprint(list)
		})
	}
	if holders["fi1"] != holders["ru2"] || holders["ru2"] != holders["de2"] {
		t.Fatalf("masters disagree on who issued: %v", holders)
	}
	t.Logf("issued by %s", holders["fi1"])

	// ru4's own name is not in the group certificate yet
	if _, err := served("ru4", "ru4.lab.firewalk.test", pebbleRoots); err == nil {
		t.Fatal("ru4.lab.firewalk.test verified before it was asked for")
	}
	f.putCert(t, "de2", "ru-group", "ru.lab.firewalk.test", "*.ru.lab.firewalk.test", "ru4.lab.firewalk.test")
	eventually(t, 90*time.Second, "the re-issued certificate reaches ru4", func() (bool, string) {
		names, err := served("ru4", "ru4.lab.firewalk.test", pebbleRoots)
		if err != nil {
			return false, err.Error()
		}
		return true, fmt.Sprint(names)
	})
}
