//go:build netlab

package netlab

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/masterd"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
)

var masterHosts = map[string]string{"fi1": "fi1m", "ru2": "ru2m", "de2": "de2m"}

func publicName(m string) string { return m + ".m.lab.firewalk.test" }

// runMaster is one master daemon with its admin API on :8080.
func runMaster() int {
	name, dir := os.Getenv("NETLAB_MASTER"), os.Getenv("NETLAB_DIR")
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		fmt.Println(err)
		return 1
	}
	caPEM, _ := os.ReadFile(os.Getenv("NETLAB_FLEET_CA"))
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	mtls := &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair},
		RootCAs: pool, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert,
	}
	peers := map[string]string{}
	var masters []string
	for m, h := range masterHosts {
		masters = append(masters, m)
		if m != name {
			peers[m] = "nats://" + ipOf(h) + ":7222"
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acme, err := labACME()
	if err != nil {
		fmt.Println(err)
		return 1
	}
	d, err := masterd.Start(ctx, masterd.Config{
		ACME: acme, RenewEvery: 2 * time.Second,
		Name: name, Masters: masters, StoreDir: filepath.Join(dir, "js"),
		PublicName: publicName(name), EdgeListen: ":443", ClientPort: 4222,
		ClientTLS: mtls, GatewayTLS: mtls, GatewayHost: "0.0.0.0", GatewayPort: 7222,
		Advertise: ipOf(masterHosts[name]) + ":7222", Peers: peers,
		Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	})
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer d.Close()
	go func() { _ = http.ListenAndServe(":8080", d.Handler()) }()
	waitSignal()
	return 0
}

type fleet struct {
	*lab
	ca    *fleetca.CA
	caPEM string
	procs map[string]interface{ Kill() error }
	nodes map[string]interface{ Kill() error }
}

// newFleetCA makes the CA of the fleet's mutual TLS (masters and nodes).
func (f *fleet) newFleetCA(t *testing.T) {
	t.Helper()
	var err error
	if f.ca, err = fleetca.New("netlab fleet"); err != nil {
		t.Fatal(err)
	}
	caPEM, _, _ := f.ca.PEM()
	f.caPEM = filepath.Join(f.dir, "fleet-ca.pem")
	os.WriteFile(f.caPEM, caPEM, 0o644)
}

func pemPair(t *testing.T, c tls.Certificate) (string, string) {
	t.Helper()
	k, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k}))
}

func (f *fleet) issue(t *testing.T, dir, cn string, hosts ...string) {
	t.Helper()
	c, err := f.ca.Issue(cn, hosts, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, key := pemPair(t, c)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "cert.pem"), []byte(cert), 0o600)
	os.WriteFile(filepath.Join(dir, "key.pem"), []byte(key), 0o600)
}

func (f *fleet) startMasters(t *testing.T, env ...string) {
	t.Helper()
	f.procs = map[string]interface{ Kill() error }{}
	f.nodes = map[string]interface{ Kill() error }{}
	for m, h := range masterHosts {
		dir := filepath.Join(f.dir, "master-"+m)
		f.issue(t, dir, m, publicName(m), ipOf(h))
		cmd := spawn(t, h, "master", append([]string{"NETLAB_MASTER=" + m, "NETLAB_DIR=" + dir, "NETLAB_FLEET_CA=" + f.caPEM}, env...)...)
		f.procs[m] = cmd.Process
	}
	deadline := time.Now().Add(90 * time.Second)
	for m, h := range masterHosts {
		for {
			resp, err := http.Get("http://" + ipOf(h) + ":8080/ready")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("master %s never became ready", m)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
}

func (f *fleet) putSpec(t *testing.T, via string, spec nodespec.Spec) {
	t.Helper()
	raw, _ := json.Marshal(spec)
	req, _ := http.NewRequest(http.MethodPut, "http://"+ipOf(masterHosts[via])+":8080/nodes/"+spec.Node+"/spec", bytes.NewReader(raw))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		t.Fatalf("put %s via %s: %d %s", spec.Node, via, resp.StatusCode, b.String())
	}
}

// startNode runs the real fbnode binary for node in its namespace.
func (f *fleet) startNode(t *testing.T, node string) {
	t.Helper()
	dir := filepath.Join(f.dir, "node-"+node)
	f.issue(t, dir, wire.NodeCN(node))
	masters := map[string]string{}
	for m := range masterHosts {
		masters[m] = "tls://" + publicName(m) + ":443"
	}
	writeJSON(t, filepath.Join(dir, "config.json"), map[string]any{
		"node": node, "masters": masters, "ca": f.caPEM,
		"cert": filepath.Join(dir, "cert.pem"), "key": filepath.Join(dir, "key.pem"),
		"dataDir": filepath.Join(dir, "data"), "xray": map[string]string{"bin": f.xray},
		"reportEvery": "1s", "pullEvery": "1s",
	})
	cmd := spawnCmd(t, node, nil, os.Getenv("NETLAB_FBNODE"), "-c", filepath.Join(dir, "config.json"))
	if f.nodes == nil {
		f.nodes = map[string]interface{ Kill() error }{}
	}
	f.nodes[node] = cmd.Process
}

func eventually(t *testing.T, within time.Duration, what string, ok func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		good, last := ok()
		if good {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: still %s after %s", what, last, within)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func exitsVia(socks int) func() (bool, string) {
	return func() (bool, string) {
		got, err := getVia(socks, "http://web.lab.test/ip")
		if err != nil {
			return false, err.Error()
		}
		return true, got
	}
}

// Three masters on their own addresses, nodes running real fbnode that
// reach masters on 443 by name. Specs go in through different masters;
// with two masters killed, a change on the last one still reaches ru4.
func TestMastersDriveNodes(t *testing.T) {
	f := &fleet{lab: newLab(t)}
	f.newFleetCA(t)
	spawn(t, "web", "web")
	waitTCP(t, ipOf("web")+":80")
	f.startMasters(t)

	de2 := nodespec.Spec{
		Node: "de2",
		Inbounds: []nodespec.Inbound{{
			Tag: "cascade", Port: 12443, Network: "ws", Path: "/ws-update", Host: "de2.lab.firewalk.test",
			Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "servers", ID: serversID}},
		}},
		Certs: []nodespec.Cert{f.cert(t, "de2.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	ru4 := nodespec.Spec{
		Node: "ru4",
		Inbounds: []nodespec.Inbound{
			{Tag: "de", Port: 12053, Network: "ws", Path: "/ws-de", Host: "ru.lab.firewalk.test", Route: nodespec.ProxyTag("de"), Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
			{Tag: "ru", Port: 12054, Network: "ws", Path: "/ws-ru", Host: "ru.lab.firewalk.test", Route: nodespec.Direct, Clients: []nodespec.Client{{Email: "alice", ID: aliceID}}},
		},
		Exits: []nodespec.Exit{{Country: "de", Hops: []nodespec.Hop{{
			Address: "de2.lab.firewalk.test", Port: 443, ID: serversID, Network: "ws", Path: "/ws-update",
			SNI: "de2.lab.firewalk.test", Host: "de2.lab.firewalk.test",
		}}}},
		Certs: []nodespec.Cert{f.cert(t, "ru4.lab.firewalk.test", "ru.lab.firewalk.test")}, DirectAllow: []string{"10.0.0.0/16"},
	}
	f.putSpec(t, "ru2", de2) // any master manages any node
	f.putSpec(t, "fi1", ru4)
	f.startNode(t, "de2")
	f.startNode(t, "ru4")
	f.client(t, map[int]string{1080: "/ws-de", 1081: "/ws-ru"})

	eventually(t, 60*time.Second, "proxy_de through de2", func() (bool, string) {
		ok, got := exitsVia(1080)()
		return ok && got == "/ip "+ipOf("de2"), got
	})
	if _, got := exitsVia(1081)(); got != "/ip "+ipOf("ru4") {
		t.Fatalf("direct on ru4 exits as %q", got)
	}
	eventually(t, 30*time.Second, "node reports reach the masters", func() (bool, string) {
		resp, err := http.Get("http://" + ipOf("de2m") + ":8080/reports")
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		var heads map[string]uint64
		_ = json.NewDecoder(resp.Body).Decode(&heads)
		var sum uint64
		for _, n := range heads {
			sum += n
		}
		return sum > 0, fmt.Sprint(heads)
	})

	t.Log("killing fi1 and ru2; de2m is the last master")
	_ = f.procs["fi1"].Kill()
	_ = f.procs["ru2"].Kill()
	ru4.Inbounds[1].Route = nodespec.ProxyTag("de")
	f.putSpec(t, "de2", ru4)
	eventually(t, 90*time.Second, "ru4 applies the change made on the last master", func() (bool, string) {
		ok, got := exitsVia(1081)()
		return ok && got == "/ip "+ipOf("de2"), got
	})
}
