//go:build netlab

package netlab

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runGame is a game server on the player's machine: TCP and UDP echo on
// NETLAB_ADDR, answering with where it runs.
func runGame() int {
	addr := os.Getenv("NETLAB_ADDR")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo([]byte("udp from player: "+string(buf[:n])), from)
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprintf(c, "tcp from player: %s", line)
			}()
		}
	}()
	waitSignal()
	return 0
}

func tcpAsk(addr, msg string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintln(c, msg)
	got, err := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(got), err
}

func udpAsk(addr, msg string) (string, error) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	return string(buf[:n]), err
}

// A player behind the VPN hosts a game on their own machine: someone on the
// internet reaches ru4:25565 and lands on the player's 127.0.0.1:25565 through
// the player's own tunnel (VLESS reverse), TCP and UDP; with the player gone,
// the port is dead.
func TestReverseReachesThePlayer(t *testing.T) {
	l := newLab(t)
	const id = "33333333-3333-4333-8333-333333333333"
	crt := l.cert(t, "ru4.lab.firewalk.test")
	dir := filepath.Join(l.dir, "rev")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "cert.pem"), []byte(crt.CertPEM), 0o600)
	os.WriteFile(filepath.Join(dir, "key.pem"), []byte(crt.KeyPEM), 0o600)

	node := fmt.Sprintf(`{"log":{"loglevel":"info"},
	 "inbounds":[
	  {"tag":"vless","listen":"%[1]s","port":443,"protocol":"vless",
	   "settings":{"decryption":"none","clients":[{"id":"%[2]s","email":"alice","reverse":{"tag":"to-alice"}}]},
	   "streamSettings":{"security":"tls","tlsSettings":{"certificates":[{"certificateFile":"%[3]s/cert.pem","keyFile":"%[3]s/key.pem"}]}}},
	  {"tag":"alice-25565","listen":"%[1]s","port":25565,"protocol":"tunnel",
	   "settings":{"address":"127.0.0.1","port":25565,"network":"tcp,udp"}}],
	 "outbounds":[{"tag":"direct","protocol":"freedom"}],
	 "routing":{"rules":[{"type":"field","inboundTag":["alice-25565"],"outboundTag":"to-alice"}]}}`, ipOf("ru4"), id, dir)
	player := fmt.Sprintf(`{"log":{"loglevel":"info"},
	 "outbounds":[
	  {"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.0/8"]}]}},
	  {"tag":"vpn","protocol":"vless","settings":{"address":"ru4.lab.firewalk.test","port":443,"id":"%s","encryption":"none",
	   "reverse":{"tag":"from-node"}},"streamSettings":{"security":"tls","tlsSettings":{"serverName":"ru4.lab.firewalk.test"}}}],
	 "routing":{"rules":[{"type":"field","inboundTag":["from-node"],"outboundTag":"direct"}]}}`, id)
	os.WriteFile(filepath.Join(dir, "node.json"), []byte(node), 0o600)
	os.WriteFile(filepath.Join(dir, "player.json"), []byte(player), 0o600)

	spawnCmd(t, "ru4", nil, l.xray, "run", "-c", filepath.Join(dir, "node.json"))
	spawn(t, "client", "game", "NETLAB_ADDR=127.0.0.1:25565")
	waitTCP(t, ipOf("ru4")+":443")
	public := ipOf("ru4") + ":25565"

	if got, err := tcpAsk(public, "hi"); err == nil {
		t.Fatalf("the port answered before the player connected: %q", got)
	}
	playerVPN := spawnCmd(t, "client", nil, l.xray, "run", "-c", filepath.Join(dir, "player.json"))
	start := time.Now()
	eventually(t, 30*time.Second, "TCP from the internet reaches the player", func() (bool, string) {
		got, err := tcpAsk(public, "hello over tcp")
		if err != nil {
			return false, err.Error()
		}
		return got == "tcp from player: hello over tcp", got
	})
	t.Logf("tunnel ready %s after the player connected", time.Since(start).Round(100*time.Millisecond))
	got, err := udpAsk(public, "hello over udp")
	if err != nil || got != "udp from player: hello over udp" {
		t.Fatalf("UDP: %q %v", got, err)
	}
	// nothing listens on the player's address from outside
	if _, err := tcpAsk(ipOf("client")+":25565", "direct"); err == nil {
		t.Fatal("the player's game port is reachable directly")
	}

	_ = playerVPN.Process.Kill()
	_ = playerVPN.Wait()
	eventually(t, 30*time.Second, "the port dies with the player", func() (bool, string) {
		got, err := tcpAsk(public, "still there?")
		if err != nil {
			return true, err.Error()
		}
		return false, got
	})
	_ = exec.Command("true").Run()
}
