package web

import (
	"context"
	"crypto/tls"
	"errors"
	"net"

	"github.com/mhsanaei/3x-ui/v3/internal/edge"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// managedCertFirst serves a certificate the panel manages for the requested
// name, so sites and hosts behind the shared port get their own; anything
// else falls back to the panel's configured pair.
func managedCertFirst(fallback func(*tls.ClientHelloInfo) (*tls.Certificate, error)) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName != "" {
			if c := service.ManagedCertForSNI(hello.ServerName); c != nil {
				return c, nil
			}
		}
		return fallback(hello)
	}
}

// applyEdge (re)configures the shared port. A new listen address is bound
// before the old listener is dropped, so a busy port leaves the edge as it was.
func (s *Server) applyEdge(rt service.EdgeRuntime) error {
	s.edgeMu.Lock()
	defer s.edgeMu.Unlock()
	if s.edgeHTTP != nil {
		if err := s.edgeHTTP.Update(rt.HTTP); err != nil {
			return err
		}
	}
	if !rt.Settings.Enable {
		s.closeEdgeLocked()
		s.edgeLastErr = ""
		return nil
	}
	usesPanel := rt.Config.Default.Action == edge.ActionHandoff
	for _, r := range rt.Config.Routes {
		usesPanel = usesPanel || r.Action == edge.ActionHandoff
	}
	if usesPanel && s.panelHandoff == nil {
		return errors.New("a route sends traffic to the panel, but the panel has no TLS certificate to answer it with")
	}
	if s.edgeLn != nil && s.edgeListen == rt.Settings.Listen {
		s.edgeRouter.Update(rt.Config)
		return nil
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", rt.Settings.Listen)
	if err != nil {
		s.edgeLastErr = err.Error()
		return err
	}
	s.closeEdgeLocked()
	handoffs := map[string]*edge.HandoffListener{}
	if s.panelHandoff != nil {
		handoffs["panel"] = s.panelHandoff
	}
	router := edge.NewRouter(rt.Config, handoffs)
	router.Drain = s.edgeDrain
	router.Logf = func(format string, args ...any) { logger.Debugf(format, args...) }
	s.edgeRouter, s.edgeLn, s.edgeListen, s.edgeLastErr = router, ln, rt.Settings.Listen, ""
	logger.Info("edge: routing", ln.Addr(), "by SNI")
	go func() {
		if err := router.Serve(ln); err != nil {
			logger.Warning("edge: stopped:", err)
		}
	}()
	return nil
}

func (s *Server) closeEdgeLocked() {
	if s.edgeLn != nil {
		_ = s.edgeLn.Close()
	}
	s.edgeLn, s.edgeRouter, s.edgeListen = nil, nil, ""
}

func (s *Server) stopEdge() {
	s.edgeMu.Lock()
	defer s.edgeMu.Unlock()
	s.closeEdgeLocked()
	if s.panelHandoff != nil {
		_ = s.panelHandoff.Close()
	}
}

func (s *Server) edgeStatus() service.EdgeStatus {
	s.edgeMu.Lock()
	defer s.edgeMu.Unlock()
	st := service.EdgeStatus{LastError: s.edgeLastErr, PanelTLS: s.panelHandoff != nil}
	if s.edgeLn != nil {
		st.Running, st.Listen = true, s.edgeLn.Addr().String()
	}
	return st
}
