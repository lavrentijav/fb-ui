// Package reportdb projects every master's node-report stream into this
// master's Postgres, counting each report once whichever master got it.
package reportdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mhsanaei/3x-ui/v3/internal/native/bus"
	"github.com/mhsanaei/3x-ui/v3/internal/native/reports"
	"github.com/mhsanaei/3x-ui/v3/internal/native/wire"
)

var validSchema = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Store is the Postgres side on one master.
type Store struct {
	db      *pgxpool.Pool
	schema  string
	bus     *bus.Bus
	masters []string
}

func Open(ctx context.Context, db *pgxpool.Pool, schema string, b *bus.Bus, masters []string) (*Store, error) {
	if !validSchema.MatchString(schema) {
		return nil, fmt.Errorf("reports: bad schema name %q", schema)
	}
	s := &Store{db: db, schema: schema, bus: b, masters: masters}
	_, err := db.Exec(ctx, s.q(`
CREATE SCHEMA IF NOT EXISTS %[1]s;
CREATE TABLE IF NOT EXISTS %[1]s.rep_pos (master text PRIMARY KEY, seq bigint NOT NULL);
CREATE TABLE IF NOT EXISTS %[1]s.rep_seen (
	node text NOT NULL, seq bigint NOT NULL, at timestamptz NOT NULL,
	PRIMARY KEY (node, seq));
CREATE TABLE IF NOT EXISTS %[1]s.traffic (
	email text NOT NULL, node text NOT NULL, up bigint NOT NULL, down bigint NOT NULL,
	PRIMARY KEY (email, node));
CREATE TABLE IF NOT EXISTS %[1]s.online (
	node text NOT NULL, email text NOT NULL, seen timestamptz NOT NULL,
	PRIMARY KEY (node, email));`))
	if err != nil {
		return nil, fmt.Errorf("reports: schema: %w", err)
	}
	return s, nil
}

func (s *Store) q(sql string) string { return fmt.Sprintf(sql, s.schema) }

// Progress of one CatchUp.
type Progress struct {
	Applied, Duplicates int
	// Lost counts reports that aged out of JetStream before this master
	// projected them (it was away longer than wire.ReportRetention).
	Lost uint64
}

// CatchUp projects every master's report stream beyond what is applied.
func (s *Store) CatchUp(ctx context.Context) (Progress, error) {
	var p Progress
	for _, m := range s.masters {
		first, last, err := s.bus.ReportRange(ctx, m)
		if err != nil {
			return p, err
		}
		var pos uint64
		err = s.db.QueryRow(ctx, s.q(`SELECT seq FROM %s.rep_pos WHERE master = $1`), m).Scan(&pos)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
		if first > 0 && pos+1 < first {
			p.Lost += first - pos - 1
			pos = first - 1
		}
		for seq := pos + 1; seq <= last; seq++ {
			raw, err := s.bus.GetReport(ctx, m, seq)
			if err != nil {
				return p, fmt.Errorf("reports: %s#%d: %w", m, seq, err)
			}
			fresh, err := s.apply(ctx, m, seq, raw)
			if err != nil {
				return p, err
			}
			if fresh {
				p.Applied++
			} else {
				p.Duplicates++
			}
		}
	}
	return p, nil
}

// apply counts one report once, whichever master's stream it arrives from,
// and advances that stream's position in the same transaction.
func (s *Store) apply(ctx context.Context, master string, seq uint64, raw []byte) (bool, error) {
	var r reports.Report
	if err := json.Unmarshal(raw, &r); err != nil || r.Node == "" {
		// a malformed report must not block the stream forever
		return false, s.advance(ctx, s.db, master, seq)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	at := time.UnixMilli(r.At)
	tag, err := tx.Exec(ctx, s.q(`INSERT INTO %s.rep_seen (node, seq, at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`), r.Node, r.Seq, at)
	if err != nil {
		return false, err
	}
	fresh := tag.RowsAffected() == 1
	if fresh {
		for _, t := range r.Traffic {
			if _, err := tx.Exec(ctx, s.q(`
INSERT INTO %[1]s.traffic (email, node, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (email, node) DO UPDATE SET up = %[1]s.traffic.up + excluded.up, down = %[1]s.traffic.down + excluded.down`),
				t.Email, r.Node, t.Up, t.Down); err != nil {
				return false, err
			}
		}
		for _, email := range r.Online {
			if _, err := tx.Exec(ctx, s.q(`
INSERT INTO %[1]s.online (node, email, seen) VALUES ($1, $2, $3)
ON CONFLICT (node, email) DO UPDATE SET seen = GREATEST(%[1]s.online.seen, excluded.seen)`), r.Node, email, at); err != nil {
				return false, err
			}
		}
	}
	if err := s.advance(ctx, tx, master, seq); err != nil {
		return false, err
	}
	return fresh, tx.Commit(ctx)
}

// execer is a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (s *Store) advance(ctx context.Context, db execer, master string, seq uint64) error {
	_, err := db.Exec(ctx, s.q(`INSERT INTO %s.rep_pos (master, seq) VALUES ($1, $2)
ON CONFLICT (master) DO UPDATE SET seq = excluded.seq`), master, seq)
	return err
}

// Total is a client's traffic summed over every node.
func (s *Store) Total(ctx context.Context, email string) (up, down int64, err error) {
	err = s.db.QueryRow(ctx, s.q(`SELECT COALESCE(SUM(up), 0), COALESCE(SUM(down), 0) FROM %s.traffic WHERE email = $1`), email).Scan(&up, &down)
	return up, down, err
}

// Online lists clients seen on any node since t, sorted.
func (s *Store) Online(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.db.Query(ctx, s.q(`SELECT DISTINCT email FROM %s.online WHERE seen >= $1 ORDER BY email`), since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// PruneSeen forgets report ids older than keep; keep must exceed
// wire.ReportRetention or a late mirror copy could be counted twice.
func (s *Store) PruneSeen(ctx context.Context, keep time.Duration) (int64, error) {
	if keep <= wire.ReportRetention {
		return 0, fmt.Errorf("reports: keep %s must exceed retention %s", keep, wire.ReportRetention)
	}
	tag, err := s.db.Exec(ctx, s.q(`DELETE FROM %s.rep_seen WHERE at < $1`), time.Now().Add(-keep))
	return tag.RowsAffected(), err
}
