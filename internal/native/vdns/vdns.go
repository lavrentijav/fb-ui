// Package vdns answers the networks' internal names on a node: one loopback
// resolver per network, so a name is visible only to its own members.
package vdns

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
)

const ttl = 300

// Name is one member's name as the master rendered it.
type Name struct {
	Name    string
	IP      string
	Network string
}

// Resolver is where the node listens for one network's members.
type Resolver struct {
	Network string
	Port    int
}

type view struct {
	zone string
	a    map[string][4]byte
	ptr  map[string]string
}

// Handler answers A, PTR and (empty) AAAA for one network's names. Names of
// other networks, and anything outside the zone, are refused.
type Handler struct{ snap atomic.Pointer[view] }

func NewHandler() *Handler {
	h := &Handler{}
	h.snap.Store(&view{a: map[string][4]byte{}, ptr: map[string]string{}})
	return h
}

// Update replaces the answers: names of network only.
func (h *Handler) Update(zone string, names []Name, network string) {
	z := &view{zone: strings.ToLower(strings.TrimSuffix(zone, ".")), a: map[string][4]byte{}, ptr: map[string]string{}}
	if z.zone == "" {
		z.zone = nodespec.VNetZoneDefault
	}
	for _, n := range names {
		if n.Network != network || !validName(n.Name) {
			continue
		}
		ip, err := netip.ParseAddr(n.IP)
		if err != nil || !ip.Is4() {
			continue
		}
		v4 := ip.As4()
		fqdn := strings.ToLower(n.Name) + "." + z.zone
		z.a[fqdn] = v4
		z.ptr[reverse(v4)] = fqdn
	}
	h.snap.Store(z)
}

// Reply builds the answer to one DNS query; nil means drop it.
func (h *Handler) Reply(pkt []byte) []byte {
	var p dnsmessage.Parser
	hdr, err := p.Start(pkt)
	if err != nil || hdr.Response {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	z := h.snap.Load()
	name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	out := dnsmessage.Header{ID: hdr.ID, Response: true, Authoritative: true, RecursionDesired: hdr.RecursionDesired}
	var answers []dnsmessage.Resource
	switch q.Type {
	case dnsmessage.TypePTR:
		if fqdn, ok := z.ptr[name]; ok {
			if target, err := dnsmessage.NewName(fqdn + "."); err == nil {
				answers = append(answers, dnsmessage.Resource{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: ttl},
					Body:   &dnsmessage.PTRResource{PTR: target},
				})
			}
		} else if strings.HasSuffix(name, ".in-addr.arpa") {
			out.RCode = dnsmessage.RCodeNameError
		} else {
			out.RCode = dnsmessage.RCodeRefused
		}
	default:
		if !inZone(z.zone, name) {
			out.RCode = dnsmessage.RCodeRefused
			break
		}
		a, ok := z.a[name]
		switch {
		case !ok:
			out.RCode = dnsmessage.RCodeNameError
		case q.Type == dnsmessage.TypeA:
			answers = append(answers, dnsmessage.Resource{
				Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl},
				Body:   &dnsmessage.AResource{A: a},
			})
		}
	}
	b := dnsmessage.NewBuilder(nil, out)
	b.EnableCompression()
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	for _, a := range answers {
		if rr, ok := a.Body.(*dnsmessage.AResource); ok {
			_ = b.AResource(a.Header, *rr)
			continue
		}
		if rr, ok := a.Body.(*dnsmessage.PTRResource); ok {
			_ = b.PTRResource(a.Header, *rr)
		}
	}
	msg, err := b.Finish()
	if err != nil {
		return nil
	}
	return msg
}

func inZone(zone, name string) bool {
	return name == zone || strings.HasSuffix(name, "."+zone)
}

func reverse(ip [4]byte) string {
	return strconv.Itoa(int(ip[3])) + "." + strconv.Itoa(int(ip[2])) + "." +
		strconv.Itoa(int(ip[1])) + "." + strconv.Itoa(int(ip[0])) + ".in-addr.arpa"
}

func validName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			if c != '-' || (i > 0 && i < len(s)-1) {
				continue
			}
		}
		return false
	}
	return true
}

// ServeUDP answers on conn until it is closed.
func ServeUDP(conn net.PacketConn, h *Handler) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if resp := h.Reply(buf[:n]); resp != nil {
			_, _ = conn.WriteTo(resp, from)
		}
	}
}

// ServeTCP answers length-prefixed queries on ln until it is closed.
func ServeTCP(ln net.Listener, h *Handler) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go serveTCPConn(c, h)
	}
}

func serveTCPConn(c net.Conn, h *Handler) {
	defer c.Close()
	var size [2]byte
	for {
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(size[:]))
		if n == 0 || n > 4096 {
			return
		}
		pkt := make([]byte, n)
		if _, err := io.ReadFull(c, pkt); err != nil {
			return
		}
		resp := h.Reply(pkt)
		if resp == nil {
			return
		}
		binary.BigEndian.PutUint16(size[:], uint16(len(resp)))
		if _, err := c.Write(append(size[:], resp...)); err != nil {
			return
		}
	}
}

// Manager keeps one UDP+TCP resolver per network on loopback and follows
// the bundles it is given.
type Manager struct {
	Logf func(format string, args ...any)

	mu       sync.Mutex
	handlers map[string]*Handler
	ports    map[string]int
	udp      map[string]*net.UDPConn
	tcp      map[string]net.Listener
}

func NewManager(logf func(string, ...any)) *Manager {
	return &Manager{
		Logf: logf, handlers: map[string]*Handler{}, ports: map[string]int{},
		udp: map[string]*net.UDPConn{}, tcp: map[string]net.Listener{},
	}
}

// Update reconciles the listeners with the bundle's resolvers; a network
// that is gone stops answering at once.
func (m *Manager) Update(zone string, names []Name, rs []Resolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]int{}
	for _, r := range rs {
		if r.Network != "" && r.Port > 0 && r.Port <= 65535 {
			want[r.Network] = r.Port
		}
	}
	for network := range m.udp {
		if _, ok := want[network]; !ok {
			m.stop(network)
		}
	}
	for network := range m.handlers {
		if _, ok := want[network]; !ok {
			delete(m.handlers, network)
		}
	}
	for network, port := range want {
		h := m.handlers[network]
		if h == nil {
			h = NewHandler()
			m.handlers[network] = h
		}
		h.Update(zone, names, network)
		if m.udp[network] != nil {
			if m.ports[network] == port {
				continue
			}
			m.stop(network)
		}
		if err := m.listen(network, port, h); err != nil {
			m.logf("dns %s: %v", network, err)
			continue
		}
		m.ports[network] = port
	}
}

func (m *Manager) listen(network string, port int, h *Handler) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	pc, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		pc.Close()
		return err
	}
	m.udp[network], m.tcp[network] = pc.(*net.UDPConn), ln
	go ServeUDP(pc, h)
	go ServeTCP(ln, h)
	return nil
}

func (m *Manager) stop(network string) {
	if c := m.udp[network]; c != nil {
		_ = c.Close()
		delete(m.udp, network)
		delete(m.ports, network)
	}
	if l := m.tcp[network]; l != nil {
		_ = l.Close()
		delete(m.tcp, network)
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for network := range m.udp {
		m.stop(network)
	}
	m.handlers = map[string]*Handler{}
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}
