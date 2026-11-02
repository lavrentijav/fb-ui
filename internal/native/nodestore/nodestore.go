// Package nodestore is a node's own micro SQLite: the small, durable set of
// facts a node needs to keep working when no master is reachable. It is not a
// copy of the panel: no clients, inbounds, groups or settings live here. The
// master stays the only source of truth; a node keeps
//
//   - the last applied desired state (so it starts serving before any master
//     answers),
//   - the report journal (numbered, unacked until a master confirms),
//   - per-client traffic counters for local quota/limit enforcement,
//   - the IP bans its own limit watcher issued,
//   - small key/value metadata (schema version, cursors).
//
// Everything is queried and updated locally, so quota enforcement never waits
// for a master round trip.
package nodestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: the agent builds with CGO_ENABLED=0
)

// SchemaVersion is bumped when the statements below change in a way an
// existing database cannot follow without a migration.
const SchemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS meta (
	k TEXT PRIMARY KEY,
	v TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS state (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	node TEXT NOT NULL,
	hash TEXT NOT NULL,
	stamp TEXT NOT NULL,
	config BLOB NOT NULL,
	at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS journal (
	seq INTEGER PRIMARY KEY,
	at INTEGER NOT NULL,
	body BLOB NOT NULL,
	acked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS traffic (
	email TEXT PRIMARY KEY,
	up INTEGER NOT NULL DEFAULT 0,
	down INTEGER NOT NULL DEFAULT 0,
	at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bans (
	ip TEXT NOT NULL,
	email TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	until INTEGER NOT NULL,
	at INTEGER NOT NULL,
	hits INTEGER NOT NULL DEFAULT 1,
	PRIMARY KEY (ip, email)
);
CREATE INDEX IF NOT EXISTS bans_until ON bans(until);
`

// Store is the open database. It is safe for concurrent use (database/sql
// serialises; SQLite runs with a busy timeout and WAL).
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path. A node has exactly one.
func Open(path string) (*Store, error) {
	// a fresh node has no data directory yet
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("nodestore: %w", err)
		}
	}
	// _pragma parameters apply per connection: WAL survives restarts, the busy
	// timeout keeps a concurrent reader from failing a write, and NORMAL is the
	// right trade for counters that a master can correct anyway.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("nodestore: %w", err)
	}
	// one writer keeps SQLite's locking simple and the node has little traffic
	// in this database
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("nodestore: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("nodestore: schema: %w", err)
	}
	s := &Store{db: db}
	var have string
	if err := db.QueryRow(`SELECT v FROM meta WHERE k='schema'`).Scan(&have); err == sql.ErrNoRows {
		if err := s.SetMeta(context.Background(), "schema", fmt.Sprint(SchemaVersion)); err != nil {
			db.Close()
			return nil, err
		}
	} else if err != nil {
		db.Close()
		return nil, fmt.Errorf("nodestore: schema version: %w", err)
	} else if have != fmt.Sprint(SchemaVersion) {
		db.Close()
		return nil, fmt.Errorf("nodestore: schema %s, want %d (a node store is rebuilt from the master, not migrated)", have, SchemaVersion)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path-independent errors a caller can react to.
var (
	ErrNoState = errors.New("nodestore: no state yet")
)

// GetMeta reads a metadata value.
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM meta WHERE k=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetMeta writes a metadata value.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	return err
}

// State is the last desired state the node applied.
type State struct {
	Node   string
	Hash   string
	Stamp  string
	Config []byte
	At     time.Time
}

// SaveState replaces the stored state (there is only ever one).
func (s *Store) SaveState(ctx context.Context, st State) error {
	at := st.At.UnixMilli()
	if st.At.IsZero() {
		at = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO state(id, node, hash, stamp, config, at) VALUES(1, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET node=excluded.node, hash=excluded.hash,
	stamp=excluded.stamp, config=excluded.config, at=excluded.at`,
		st.Node, st.Hash, st.Stamp, st.Config, at)
	return err
}

// State answers the last applied state; ErrNoState when the node has not
// applied one yet (a fresh install).
func (s *Store) State(ctx context.Context) (State, error) {
	var st State
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT node, hash, stamp, config, at FROM state WHERE id=1`).
		Scan(&st.Node, &st.Hash, &st.Stamp, &st.Config, &at)
	if err == sql.ErrNoRows {
		return State{}, ErrNoState
	}
	if err != nil {
		return State{}, err
	}
	st.At = time.UnixMilli(at)
	return st, nil
}

// Report is one journaled report.
type Report struct {
	Seq  uint64
	At   time.Time
	Body []byte
}

// AppendReport adds a report and answers its sequence number. The number never
// repeats, even after acknowledged reports are pruned: it is what lets a
// master count a report once across retries.
func (s *Store) AppendReport(ctx context.Context, at time.Time, body []byte) (uint64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var next int64 = 1
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT v FROM meta WHERE k='journal.next'`).Scan(&raw)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return 0, err
	default:
		if _, err := fmt.Sscan(raw, &next); err != nil {
			return 0, fmt.Errorf("nodestore: bad journal cursor %q", raw)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO journal(seq, at, body, acked) VALUES(?, ?, ?, 0)`,
		next, at.UnixMilli(), body); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta(k, v) VALUES('journal.next', ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`,
		fmt.Sprint(next+1)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return uint64(next), nil
}

// Pending answers up to limit unacknowledged reports, oldest first.
func (s *Store) Pending(ctx context.Context, limit int) ([]Report, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, at, body FROM journal WHERE acked=0 ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		var at int64
		if err := rows.Scan(&r.Seq, &at, &r.Body); err != nil {
			return nil, err
		}
		r.At = time.UnixMilli(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ack marks every report up to seq as delivered.
func (s *Store) Ack(ctx context.Context, seq uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE journal SET acked=1 WHERE seq<=? AND acked=0`, seq)
	return err
}

// PendingCount is what the sender shows in logs and health.
func (s *Store) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal WHERE acked=0`).Scan(&n)
	return n, err
}

// Prune deletes acknowledged reports, keeping the newest keep rows so a
// debugging operator can still see recent history. Sequence numbering is
// unaffected (it lives in meta).
func (s *Store) Prune(ctx context.Context, keep int) (int64, error) {
	if keep < 0 {
		keep = 0
	}
	res, err := s.db.ExecContext(ctx, `
DELETE FROM journal WHERE acked=1 AND seq NOT IN (
	SELECT seq FROM journal WHERE acked=1 ORDER BY seq DESC LIMIT ?
)`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Traffic is one client's locally counted bytes.
type Traffic struct {
	Up   int64
	Down int64
	At   time.Time
}

// AddTraffic accumulates a client's traffic for local enforcement.
func (s *Store) AddTraffic(ctx context.Context, email string, up, down int64) error {
	if email == "" || (up == 0 && down == 0) {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO traffic(email, up, down, at) VALUES(?, ?, ?, ?)
ON CONFLICT(email) DO UPDATE SET up=up+excluded.up, down=down+excluded.down, at=excluded.at`,
		email, up, down, time.Now().UnixMilli())
	return err
}

// Traffic answers every counted client.
func (s *Store) Traffic(ctx context.Context) (map[string]Traffic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email, up, down, at FROM traffic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Traffic{}
	for rows.Next() {
		var email string
		var t Traffic
		var at int64
		if err := rows.Scan(&email, &t.Up, &t.Down, &at); err != nil {
			return nil, err
		}
		t.At = time.UnixMilli(at)
		out[email] = t
	}
	return out, rows.Err()
}

// One answers a single client's counters.
func (s *Store) One(ctx context.Context, email string) (Traffic, bool, error) {
	var t Traffic
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT up, down, at FROM traffic WHERE email=?`, email).
		Scan(&t.Up, &t.Down, &at)
	if err == sql.ErrNoRows {
		return Traffic{}, false, nil
	}
	if err != nil {
		return Traffic{}, false, err
	}
	t.At = time.UnixMilli(at)
	return t, true, nil
}

// ResetTraffic forgets one client's counters (the operator reset, or a master
// correction).
func (s *Store) ResetTraffic(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM traffic WHERE email=?`, email)
	return err
}

// Ban is one blocked address issued by the node's IP-limit watcher.
type Ban struct {
	IP     string
	Email  string
	Reason string
	Until  time.Time
	At     time.Time
	Hits   int
}

// AddBan records (or refreshes) a ban. A ban is local: the master does not
// need to know before the next report.
func (s *Store) AddBan(ctx context.Context, b Ban) error {
	until := b.Until.UnixMilli()
	if b.Until.IsZero() {
		until = time.Now().Add(time.Hour).UnixMilli()
	}
	at := b.At.UnixMilli()
	if b.At.IsZero() {
		at = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO bans(ip, email, reason, until, at, hits) VALUES(?, ?, ?, ?, ?, 1)
ON CONFLICT(ip, email) DO UPDATE SET reason=excluded.reason,
	until=excluded.until, at=excluded.at, hits=hits+1`,
		b.IP, b.Email, b.Reason, until, at)
	return err
}

// RemoveBan lifts one ban.
func (s *Store) RemoveBan(ctx context.Context, ip, email string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM bans WHERE ip=? AND email=?`, ip, email)
	return err
}

// Bans answers the bans still in force at now.
func (s *Store) Bans(ctx context.Context, now time.Time) ([]Ban, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ip, email, reason, until, at, hits FROM bans WHERE until>? ORDER BY until`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		var b Ban
		var until, at int64
		if err := rows.Scan(&b.IP, &b.Email, &b.Reason, &until, &at, &b.Hits); err != nil {
			return nil, err
		}
		b.Until, b.At = time.UnixMilli(until), time.UnixMilli(at)
		out = append(out, b)
	}
	return out, rows.Err()
}

// ExpireBans drops bans that ended at or before now.
func (s *Store) ExpireBans(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bans WHERE until<=?`, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
