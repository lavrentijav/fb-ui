package certs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

// Writer is the write side of a master.
type Writer interface {
	Set(ctx context.Context, table, key string, fields map[string]any) (oplog.Op, error)
}

// HoldAfterRollback is how long renewal stays off for a rolled-back
// certificate; the older pair is often close to expiry and would otherwise be
// renewed again on the next tick, undoing the rollback.
const HoldAfterRollback = 24 * time.Hour

// HeldUntil is the unix-ms time until which renewal of rec is suspended.
func HeldUntil(rec map[string]json.RawMessage) int64 {
	v, _ := field[int64](rec, "holdUntil")
	return v
}

// Rollback swaps the pair a renewal replaced back in. The pair it replaces
// becomes the previous one, so a second call undoes the first. Nodes pull the
// result with their next bundle.
func Rollback(ctx context.Context, w Writer, r Reader, id string, now time.Time) error {
	rec, ok := r.Get(Table, id)
	if !ok {
		return errors.New("certs: no such certificate")
	}
	prevCert, _ := field[string](rec, "prevCertPem")
	prevKey, _ := field[string](rec, "prevKeyPem")
	if _, err := tls.X509KeyPair([]byte(prevCert), []byte(prevKey)); err != nil {
		return errors.New("certs: there is no earlier version of this certificate to go back to")
	}
	cert, _ := field[string](rec, "certPem")
	key, _ := field[string](rec, "keyPem")
	issuedFor, _ := field[[]string](rec, "issuedFor")
	notAfter, _ := field[int64](rec, "notAfter")
	prevFor, _ := field[[]string](rec, "prevIssuedFor")
	prevNotAfter, _ := field[int64](rec, "prevNotAfter")
	_, err := w.Set(ctx, Table, id, map[string]any{
		"certPem": prevCert, "keyPem": prevKey, "issuedFor": prevFor, "notAfter": prevNotAfter,
		"prevCertPem": cert, "prevKeyPem": key, "prevIssuedFor": issuedFor, "prevNotAfter": notAfter,
		"holdUntil": now.Add(HoldAfterRollback).UnixMilli(),
	})
	return err
}
