# Native multi-master

Status: accepted 2026-09-29. Replaces track 5 of `native-core.md`: masters
replicate from the start, because the agent protocol depends on it.

## Requirement

Every master (fi1, ru2, de2, later more) is a full peer: any master accepts
any change and manages any node, including while the others are unreachable.
Split between RU and EU is a normal state, not a disaster.

## Storage on a master

- **JetStream** (embedded NATS) carries operational data and the change
  log. Each master is its own one-server cluster; gateways join them into a
  supercluster. Master X owns stream `OPS_X` (R1, file storage) and keeps a
  mirror `M_X_Y` of every other master's stream — all masters are mirrors
  of each other.
- **Postgres** on every master holds the materialized state, built from the
  streams (`internal/native/pgstate`). It is not replicated by Postgres
  itself (that is single-primary); every master projects the same ops.

## Why this stays writable during a split (tested)

`bus` tests cut the gateways through TCP proxies:

- publishing to a master's own R1 stream needs no JetStream meta quorum —
  both sides of RU|EU keep writing;
- creating streams or consumers does need the quorum, so the topology is
  created up front (`Bus.Ensure`) and reading uses direct get by sequence,
  no consumers;
- after the heal mirrors catch up in seconds; mirrors keep the source
  sequence numbers, so `OPS_X` seq = op seq of origin X.

## Merge

- **Clock.** Hybrid logical clock (wall ms + logical + origin id), totally
  ordered, monotonic across host clock steps and restarts.
- **Ops.** `{origin, seq, stamp, table, key, fields | delete}`, published
  with expected-last-sequence so an origin's seqs have no gaps or forks.
- **Projection.** Per field, the highest stamp wins (conditional upsert on a
  sortable `bytea` stamp); a delete is a tombstone hiding older fields. The
  applied seq per origin is stored in the same transaction, so a crash
  never applies an op twice. Any interleaving gives identical rows (tested
  against the in-memory `lww` reference).
- **Single-writer data** (traffic, onlines) belongs to the node that
  measured it; totals are sums over nodes, never LWW.
- **Invariants that need one winner** (unique email, port on a node) are
  checked on write and re-checked after merge; a loser is flagged in the UI.

## Nodes (`fbnode`)

- The agent knows all masters and binds to the nearest one (`pick` +
  `probe`): lowest score of RTT × (1 + 2·load), plus a penalty for slow
  throughput and a ×10 penalty for a path MSS below the minimum. It
  re-chooses every `RebindEvery` (setting, default 1 h) with 20 %
  hysteresis, and at once when sends to the bound master fail. Masters
  answer `probe.<name>` with their load and a sized payload.
- Masters are trusted by certificates from the fleet CA pinned at enrollment.
- Desired state carries the stamp of the newest op it was rendered from;
  the agent never applies an older one, so two masters cannot roll it back.
- Reports are journaled on the node (`oplog`: per-origin file log with CRC)
  and acked by whichever master received them.

## Packages (branch `native`)

| Package | Role |
|---|---|
| `internal/native/hlc` | hybrid logical clock, sortable stamp keys |
| `internal/native/bus` | embedded NATS JetStream: own stream, mirrors, publish/get |
| `internal/native/pgstate` | Postgres LWW projection |
| `internal/native/lww` | in-memory LWW reference (tests, tools) |
| `internal/native/master` | writes on any master, catch-up projection |
| `internal/native/oplog` | durable per-origin file log (node journal) |
| `internal/native/fleetca` | fleet CA, mutual TLS between masters |
| `internal/native/pick` | node's choice of master, hourly rebind |
| `internal/native/probe` | RTT, throughput, TCP MSS, load over NATS |
| next | TLS + auth on gateways, operational streams (traffic → Postgres), `fbnode` |

Tests: `XUI_TEST_PG_DSN` enables the Postgres cases, `XUI_NATS_SPLIT_TEST=1`
the bus split test.
