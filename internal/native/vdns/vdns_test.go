package vdns

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func ask(t *testing.T, h *Handler, name string, typ dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	n, err := dnsmessage.NewName(name + ".")
	if err != nil {
		t.Fatal(err)
	}
	q := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: typ, Class: dnsmessage.ClassINET}},
	}
	pkt, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	resp := h.Reply(pkt)
	if resp == nil {
		t.Fatal("no reply")
	}
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAnswersOnlyItsNetwork(t *testing.T) {
	h := NewHandler()
	h.Update("local", []Name{
		{Name: "alice", IP: "100.64.0.7", Network: "n1"},
		{Name: "eve", IP: "100.64.0.9", Network: "n2"},
	}, "n1")

	m := ask(t, h, "alice.local", dnsmessage.TypeA)
	if m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 1 {
		t.Fatalf("alice.local: rcode %v, %d answers", m.RCode, len(m.Answers))
	}
	if a, ok := m.Answers[0].Body.(*dnsmessage.AResource); !ok || a.A != [4]byte{100, 64, 0, 7} {
		t.Fatalf("alice.local answer %+v", m.Answers[0].Body)
	}
	if m := ask(t, h, "eve.local", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("a name of another network: rcode %v", m.RCode)
	}
	if m := ask(t, h, "example.com", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeRefused {
		t.Fatalf("a name outside the zone: rcode %v", m.RCode)
	}
	if m := ask(t, h, "alice.local", dnsmessage.TypeAAAA); m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Fatalf("AAAA: rcode %v, %d answers", m.RCode, len(m.Answers))
	}
	m = ask(t, h, "7.0.64.100.in-addr.arpa", dnsmessage.TypePTR)
	if m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 1 {
		t.Fatalf("PTR: rcode %v, %d answers", m.RCode, len(m.Answers))
	}
	if ptr, ok := m.Answers[0].Body.(*dnsmessage.PTRResource); !ok || ptr.PTR.String() != "alice.local." {
		t.Fatalf("PTR answer %+v", m.Answers[0].Body)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func queryPacket(t *testing.T, name string) []byte {
	if t != nil {
		t.Helper()
	}
	n, _ := dnsmessage.NewName(name + ".")
	q := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 9, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	pkt, err := q.Pack()
	if err != nil && t != nil {
		t.Fatal(err)
	}
	return pkt
}

func answers(addr, name string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.Dial("udp", addr)
		if err == nil {
			_, _ = c.Write(queryPacket(nil, name))
			_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			buf := make([]byte, 512)
			n, rerr := c.Read(buf)
			c.Close()
			if rerr == nil {
				var resp dnsmessage.Message
				if resp.Unpack(buf[:n]) == nil && len(resp.Answers) == 1 {
					return true
				}
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestManagerStartsMovesAndStopsResolvers(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()
	port := freePort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	m.Update("local", []Name{{Name: "alice", IP: "100.64.0.7", Network: "n1"}},
		[]Resolver{{Network: "n1", Port: port}})
	if !answers(addr, "alice.local") {
		t.Fatal("the resolver never answered")
	}

	tc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	pkt := queryPacket(t, "alice.local")
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(pkt)))
	_, _ = tc.Write(append(size[:], pkt...))
	if _, err := io.ReadFull(tc, size[:]); err != nil {
		t.Fatalf("tcp: %v", err)
	}
	buf := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(tc, buf); err != nil {
		t.Fatalf("tcp: %v", err)
	}
	tc.Close()
	var resp dnsmessage.Message
	if err := resp.Unpack(buf); err != nil || len(resp.Answers) != 1 {
		t.Fatalf("tcp answer: %v %d", err, len(resp.Answers))
	}

	// a new port in a later bundle moves the listener
	port2 := freePort(t)
	addr2 := net.JoinHostPort("127.0.0.1", strconv.Itoa(port2))
	m.Update("local", []Name{{Name: "alice", IP: "100.64.0.7", Network: "n1"}},
		[]Resolver{{Network: "n1", Port: port2}})
	if !answers(addr2, "alice.local") {
		t.Fatal("the resolver did not move to the new port")
	}

	m.Update("local", nil, nil)
	c, err := net.Dial("udp", addr2)
	if err != nil {
		return
	}
	defer c.Close()
	_, _ = c.Write(queryPacket(t, "alice.local"))
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := c.Read(make([]byte, 512)); err == nil {
		t.Fatal("a resolver of a network that is gone still answers")
	}
}
