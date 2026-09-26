// Package provisiontest runs an in-process SSH server for tests of code that
// drives servers through the provision package.
package provisiontest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Handler answers one exec request: it may read stdin and write output, and
// returns the exit status.
type Handler func(cmd string, stdin io.Reader, out io.Writer) int

// ShellHandler runs the command through /bin/sh on the test machine.
func ShellHandler(cmd string, stdin io.Reader, out io.Writer) int {
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Stdin, c.Stdout, c.Stderr = stdin, out, out
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 127
	}
	return 0
}

type Server struct {
	Host     string
	Port     int
	HostKey  ssh.PublicKey
	UserKey  ed25519.PrivateKey
	Password string

	mu       sync.Mutex
	commands []string
}

// Start listens on 127.0.0.1 and accepts password (when non-empty) or UserKey.
func Start(t *testing.T, password string, h Handler) *Server {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	_, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	userPub, _ := ssh.NewPublicKey(userPriv.Public())
	s := &Server{HostKey: hostSigner.PublicKey(), UserKey: userPriv, Password: password}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if password != "" && string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if string(k.Marshal()) == string(userPub.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostSigner)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	s.Host = host
	s.Port, _ = strconv.Atoi(port)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, cfg, h)
		}
	}()
	return s
}

// Commands returns every command executed so far, in order.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// UserKeyPEM is UserKey as an OpenSSH private key file.
func (s *Server) UserKeyPEM(t *testing.T) string {
	t.Helper()
	b, err := ssh.MarshalPrivateKey(s.UserKey, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(b))
}

func (s *Server) serve(c net.Conn, cfg *ssh.ServerConfig, h Handler) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range chReqs {
				if req.Type != "exec" || len(req.Payload) < 4 {
					_ = req.Reply(false, nil)
					continue
				}
				n := binary.BigEndian.Uint32(req.Payload[:4])
				cmd := string(req.Payload[4 : 4+n])
				s.mu.Lock()
				s.commands = append(s.commands, cmd)
				s.mu.Unlock()
				_ = req.Reply(true, nil)
				status := h(cmd, ch, ch)
				_, _ = ch.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, uint32(status)))
				return
			}
		}()
	}
}
