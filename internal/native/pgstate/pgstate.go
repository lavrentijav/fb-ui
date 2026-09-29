// Package pgstate is a master's Postgres projection of the op streams: the
// same field-level LWW as package lww, done by conditional upserts.
package pgstate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mhsanaei/3x-ui/v3/internal/native/hlc"
	"github.com/mhsanaei/3x-ui/v3/internal/native/oplog"
)

var validSchema = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type Store struct {
	db     *pgxpool.Pool
	schema string
}

// Open creates the projection tables in schema if missing.
func Open(ctx context.Context, db *pgxpool.Pool, schema string) (*Store, error) {
	if !validSchema.MatchString(schema) {
		return nil, fmt.Errorf("pgstate: bad schema name %q", schema)
	}
	s := &Store{db: db, schema: schema}
	_, err := db.Exec(ctx, s.q(`
CREATE SCHEMA IF NOT EXISTS %[1]s;
CREATE TABLE IF NOT EXISTS %[1]s.lww_field (
	tbl text NOT NULL, key text NOT NULL, field text NOT NULL,
	stamp bytea NOT NULL, value jsonb NOT NULL,
	PRIMARY KEY (tbl, key, field));
CREATE TABLE IF NOT EXISTS %[1]s.lww_tomb (
	tbl text NOT NULL, key text NOT NULL, stamp bytea NOT NULL,
	PRIMARY KEY (tbl, key));
CREATE TABLE IF NOT EXISTS %[1]s.lww_applied (
	origin text PRIMARY KEY, seq bigint NOT NULL, newest bytea NOT NULL);`))
	if err != nil {
		return nil, fmt.Errorf("pgstate: schema: %w", err)
	}
	return s, nil
}

func (s *Store) q(sql string) string { return fmt.Sprintf(sql, s.schema) }

// Apply merges one op and records it as applied in the same transaction;
// an op already applied is skipped, a seq gap is oplog.ErrGap.
func (s *Store) Apply(ctx context.Context, op oplog.Op) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var have uint64
	var newest []byte
	err = tx.QueryRow(ctx, s.q(`SELECT seq, newest FROM %s.lww_applied WHERE origin = $1 FOR UPDATE`), op.Origin).Scan(&have, &newest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	switch {
	case op.Seq <= have:
		return false, nil
	case op.Seq != have+1:
		return false, fmt.Errorf("%w: %s applied %d, got %d", oplog.ErrGap, op.Origin, have, op.Seq)
	}
	stamp := op.Stamp.Key()
	if op.Delete {
		if _, err := tx.Exec(ctx, s.q(`
INSERT INTO %[1]s.lww_tomb (tbl, key, stamp) VALUES ($1, $2, $3)
ON CONFLICT (tbl, key) DO UPDATE SET stamp = excluded.stamp
WHERE %[1]s.lww_tomb.stamp < excluded.stamp`), op.Table, op.Key, stamp); err != nil {
			return false, err
		}
	}
	for name, value := range op.Fields {
		if _, err := tx.Exec(ctx, s.q(`
INSERT INTO %[1]s.lww_field (tbl, key, field, stamp, value) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tbl, key, field) DO UPDATE SET stamp = excluded.stamp, value = excluded.value
WHERE %[1]s.lww_field.stamp < excluded.stamp`), op.Table, op.Key, name, stamp, string(value)); err != nil {
			return false, err
		}
	}
	if newest == nil || string(stamp) > string(newest) {
		newest = stamp
	}
	if _, err := tx.Exec(ctx, s.q(`
INSERT INTO %s.lww_applied (origin, seq, newest) VALUES ($1, $2, $3)
ON CONFLICT (origin) DO UPDATE SET seq = excluded.seq, newest = excluded.newest`), op.Origin, op.Seq, newest); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// Applied is the last seq applied per origin.
func (s *Store) Applied(ctx context.Context) (oplog.Vector, error) {
	rows, err := s.db.Query(ctx, s.q(`SELECT origin, seq FROM %s.lww_applied`))
	if err != nil {
		return nil, err
	}
	v := oplog.Vector{}
	for rows.Next() {
		var origin string
		var seq uint64
		if err := rows.Scan(&origin, &seq); err != nil {
			return nil, err
		}
		v[origin] = seq
	}
	return v, rows.Err()
}

// Newest is the highest stamp applied from origin, to restore its clock.
func (s *Store) Newest(ctx context.Context, origin string) (hlc.Stamp, error) {
	var key []byte
	err := s.db.QueryRow(ctx, s.q(`SELECT newest FROM %s.lww_applied WHERE origin = $1`), origin).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return hlc.Stamp{}, nil
	}
	if err != nil {
		return hlc.Stamp{}, err
	}
	return hlc.FromKey(key)
}

const visible = `
FROM %[1]s.lww_field f LEFT JOIN %[1]s.lww_tomb t ON t.tbl = f.tbl AND t.key = f.key
WHERE (t.stamp IS NULL OR f.stamp > t.stamp)`

// Get returns the visible fields of a record, false when absent or deleted.
func (s *Store) Get(ctx context.Context, table, key string) (map[string]json.RawMessage, bool, error) {
	rows, err := s.db.Query(ctx, s.q(`SELECT f.field, f.value::text`+visible+` AND f.tbl = $1 AND f.key = $2`), table, key)
	if err != nil {
		return nil, false, err
	}
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, false, err
		}
		out[name] = json.RawMessage(value)
	}
	return out, len(out) > 0, rows.Err()
}

// Keys lists the live records of a table, sorted.
func (s *Store) Keys(ctx context.Context, table string) ([]string, error) {
	rows, err := s.db.Query(ctx, s.q(`SELECT DISTINCT f.key`+visible+` AND f.tbl = $1 ORDER BY f.key`), table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Digest hashes the visible state; masters with equal digests agree.
func (s *Store) Digest(ctx context.Context) ([32]byte, error) {
	rows, err := s.db.Query(ctx, s.q(`SELECT f.tbl, f.key, f.field, f.value::text`+visible+
		` ORDER BY f.tbl COLLATE "C", f.key COLLATE "C", f.field COLLATE "C"`))
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	for rows.Next() {
		var tbl, key, field, value string
		if err := rows.Scan(&tbl, &key, &field, &value); err != nil {
			return [32]byte{}, err
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", tbl, key, field, value)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, rows.Err()
}
