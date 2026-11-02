// Command fbmaster runs one native master: its bus, the op projection, the
// module API and the administrative surface. It is the production entrypoint;
// the lab and tests call masterd directly.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/fleetca"
	"github.com/mhsanaei/3x-ui/v3/internal/native/masterd"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// Config is the master's file. Every path is absolute; nothing secret is
// logged.
type Config struct {
	Name      string   `json:"name"`
	Masters   []string `json:"masters"`
	StoreDir  string   `json:"storeDir"`
	DataDir   string   `json:"dataDir"`
	PublicIPs []string `json:"publicIPs,omitempty"`

	// Edge serves the nodes' tunnels by SNI on PublicName.
	PublicName string `json:"publicName"`
	EdgeListen string `json:"edgeListen"`
	ClientPort int    `json:"clientPort"`
	// Gateway connects the masters.
	GatewayHost string            `json:"gatewayHost"`
	GatewayPort int               `json:"gatewayPort"`
	Advertise   string            `json:"advertise"`
	Peers       map[string]string `json:"peers"`

	// PoolName resolves to every live master (panel and subscriptions).
	PoolName string `json:"poolName"`

	// APIListen serves /api/v1; empty disables it (modules still run).
	APIListen string `json:"apiListen"`
	// AdminListen is the administrative surface; default loopback:0.
	AdminListen      string `json:"adminListen"`
	AdminAllowPublic bool   `json:"adminAllowPublic"`
	// UIDir is a built interface (dist/).
	UIDir string `json:"uiDir"`

	// Fleets CA and this master's certificate.
	CACert string   `json:"caCert"`
	CAKey  string   `json:"caKey"`
	Cert   string   `json:"cert"`
	Key    string   `json:"key"`
	Hosts  []string `json:"hosts,omitempty"` // extra SANs for the master's certificate
}

func load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{EdgeListen: ":443", ClientPort: 4222, GatewayHost: "0.0.0.0", GatewayPort: 7222}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) normalize() error {
	if c.Name == "" {
		return errors.New("name is required")
	}
	if c.StoreDir == "" {
		return errors.New("storeDir is required")
	}
	if c.DataDir == "" {
		c.DataDir = c.StoreDir
	}
	if c.PublicName == "" {
		c.PublicName = c.Name
	}
	if c.CACert == "" {
		c.CACert = filepath.Join(c.DataDir, "ca.pem")
	}
	if c.CAKey == "" {
		c.CAKey = filepath.Join(c.DataDir, "ca-key.pem")
	}
	if c.Cert == "" {
		c.Cert = filepath.Join(c.DataDir, "master.pem")
	}
	if c.Key == "" {
		c.Key = filepath.Join(c.DataDir, "master-key.pem")
	}
	for _, p := range []string{c.StoreDir, c.DataDir} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s must be absolute", p)
		}
	}
	for name, url := range c.Peers {
		if name == c.Name {
			return fmt.Errorf("peers must not name this master (%s)", name)
		}
		if !strings.HasPrefix(url, "nats://") {
			return fmt.Errorf("peer %s must be a nats:// URL", name)
		}
	}
	return nil
}

func main() {
	path := flag.String("c", "/etc/fbmaster/config.json", "config file")
	check := flag.Bool("check", false, "validate the config and exit")
	createCA := flag.Bool("create-ca", false, "create the fleet CA when missing (first master only)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	logger := log.New(os.Stderr, "fbmaster ", log.LstdFlags)
	c, err := load(*path)
	if err != nil {
		logger.Fatal(err)
	}
	if *check {
		if err := checkConfig(c); err != nil {
			logger.Fatal(err)
		}
		fmt.Println("config is valid")
		return
	}
	if err := run(c, *createCA, logger); err != nil {
		logger.Fatal(err)
	}
}

// checkConfig reports what the master would do without starting it.
func checkConfig(c *Config) error {
	for _, dir := range []string{c.StoreDir, c.DataDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".check-*")
		if err != nil {
			return fmt.Errorf("%s is not writable: %w", dir, err)
		}
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
	if c.AdminListen != "" && !c.AdminAllowPublic {
		host, _, err := net.SplitHostPort(c.AdminListen)
		if err != nil || !(host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())) {
			return fmt.Errorf("adminListen %q is not loopback (set adminAllowPublic to expose it)", c.AdminListen)
		}
	}
	for _, m := range c.Masters {
		if m == "" {
			return errors.New("masters contains an empty name")
		}
	}
	fmt.Printf("master %s\n  data      %s\n  store     %s\n  edge      %s (public name %s)\n  api       %s\n  admin     %s\n  peers     %d\n",
		c.Name, c.DataDir, c.StoreDir, c.EdgeListen, c.PublicName, orDefault(c.APIListen, "(off)"), orDefault(c.AdminListen, "(loopback, free port)"), len(c.Peers))
	return nil
}

func run(c *Config, createCA bool, logger *log.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, c, createCA, logger)
}

// serve is run without the signal plumbing, so a test can start and stop it.
func serve(ctx context.Context, c *Config, createCA bool, logger *log.Logger) error {
	if err := checkConfig(c); err != nil {
		return err
	}
	ca, err := ensureCA(c, createCA, logger)
	if err != nil {
		return err
	}
	own, err := ensureIdentity(c, ca, logger)
	if err != nil {
		return err
	}
	mtls := ca.MutualTLS(own)

	peers := map[string]string{}
	for name, url := range c.Peers {
		peers[name] = url
	}
	d, err := masterd.Start(ctx, masterd.Config{
		Name: c.Name, Masters: c.Masters, StoreDir: c.StoreDir,
		PublicName: c.PublicName, EdgeListen: c.EdgeListen, ClientPort: c.ClientPort,
		ClientTLS: mtls, GatewayTLS: mtls, GatewayHost: c.GatewayHost, GatewayPort: c.GatewayPort,
		Advertise: c.Advertise, Peers: peers,
		PoolName: c.PoolName, PublicIPs: c.PublicIPs,
		UIDir: c.UIDir, APIListen: c.APIListen,
		AdminListen: c.AdminListen, AdminAllowPublic: c.AdminAllowPublic,
		FleetCA: ca, NodeBinary: os.Getenv("FBMasterNodeBinary"),
		Logf: logger.Printf,
	})
	if err != nil {
		return err
	}
	defer d.Close()
	logger.Printf("master %s, version %s", c.Name, version)
	if c.UIDir != "" {
		logger.Printf("interface served at /panel from %s", c.UIDir)
	} else {
		logger.Printf("no interface (uiDir is empty): the admin API and API tokens are the way in")
	}

	// the admin surface is the operator's way in; it must stay loopback unless
	// explicitly opened
	if addr, err := d.AdminAddress(); err != nil {
		return err
	} else {
		logger.Printf("admin will listen on %s", addr)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- d.ListenAdmin(ctx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// ensureCA loads the fleet CA, or creates it when asked and none exists.
func ensureCA(c *Config, create bool, logger *log.Logger) (*fleetca.CA, error) {
	certPEM, certErr := os.ReadFile(c.CACert)
	keyPEM, keyErr := os.ReadFile(c.CAKey)
	switch {
	case certErr == nil && keyErr == nil:
		return fleetca.Load(certPEM, keyPEM)
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		if !create {
			return nil, fmt.Errorf("no fleet CA at %s; run with -create-ca on the first master", c.CACert)
		}
		ca, err := fleetca.New(c.Name + "-fleet")
		if err != nil {
			return nil, err
		}
		certPEM, keyPEM, err = ca.PEM()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(c.CACert), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(c.CACert, certPEM, 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(c.CAKey, keyPEM, 0o600); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(certPEM)
		logger.Printf("created the fleet CA in %s (sha256 %s)", c.CACert, hex.EncodeToString(sum[:8]))
		return ca, nil
	default:
		return nil, fmt.Errorf("fleet CA is incomplete: cert=%v key=%v", certErr, keyErr)
	}
}

// ensureIdentity loads this master's certificate, or issues one from the CA.
func ensureIdentity(c *Config, ca *fleetca.CA, logger *log.Logger) (tls.Certificate, error) {
	if cert, err := tls.LoadX509KeyPair(c.Cert, c.Key); err == nil {
		return cert, nil
	} else if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) {
		// a malformed pair is an error, not a reason to mint another
		if _, statErr := os.Stat(c.Cert); statErr == nil {
			return tls.Certificate{}, err
		}
	}
	hosts := append([]string{c.Name, c.PublicName}, c.Hosts...)
	if c.Advertise != "" {
		if host, _, err := net.SplitHostPort(c.Advertise); err == nil && host != "" {
			hosts = append(hosts, host)
		}
	}
	cert, err := ca.Issue(c.Name, dedupe(hosts), 2*365*24*time.Hour)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(filepath.Dir(c.Cert), 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(c.Cert, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(c.Key, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	logger.Printf("issued this master's certificate for %v in %s", dedupe(hosts), c.Cert)
	return cert, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
