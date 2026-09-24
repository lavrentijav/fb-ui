// Package certreload serves a TLS certificate from files and picks up a
// renewed pair without restarting the listener.
package certreload

import (
	"crypto/tls"
	"os"
	"sync"
	"time"
)

const checkEvery = 30 * time.Second

// Reloader is a tls.Config.GetCertificate source backed by a cert/key pair.
type Reloader struct {
	certFile, keyFile string

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	checkedAt time.Time
	now       func() time.Time
}

// New loads the pair once; an unreadable pair at startup is an error so the
// caller can fall back to plain HTTP exactly as it did before.
func New(certFile, keyFile string) (*Reloader, error) {
	r := &Reloader{certFile: certFile, keyFile: keyFile, now: time.Now}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func (r *Reloader) load() error {
	certMod, keyMod := modTime(r.certFile), modTime(r.keyFile)
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.cert, r.certMod, r.keyMod = &c, certMod, keyMod
	return nil
}

// GetCertificate returns the current pair, re-reading the files when their
// mtimes moved. A half-written or broken pair keeps serving the old one.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); now.Sub(r.checkedAt) >= checkEvery {
		r.checkedAt = now
		if !modTime(r.certFile).Equal(r.certMod) || !modTime(r.keyFile).Equal(r.keyMod) {
			_ = r.load()
		}
	}
	return r.cert, nil
}
