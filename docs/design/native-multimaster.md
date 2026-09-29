# Native multi-master

Status: accepted 2026-09-29. Replaces track 5 of `native-core.md`: masters
replicate from the start, because the agent protocol depends on it.

## Requirement

Every master (fi1, ru2, de2, later more) is a full peer: any master accepts
any change and manages any node, including while the others are unreachable.
Split between RU and EU is a normal state, not a disaster.

## Decision: leaderless op log with last-writer-wins fields

A leader (Raft, JetStream R3) would stop writes on the minority side of a
split — exactly the RU/EU case. So masters are AP:

- **Clock.** Each master stamps changes with a hybrid logical clock
  (wall ms + logical counter + origin id). Stamps are totally ordered and
  stay monotonic when a host clock jumps back.
- **Ops.** A change is an op `{origin, seq, stamp, table, key, fields |
  delete}`. Each master appends its own ops to its own log with `seq`
  1, 2, 3…; the log is the only thing replicated.
- **Replication.** Masters pull from each other with a version vector
  (`origin → last seq seen`) and ingest every origin's ops strictly in seq
  order. Ops are relayed, so fi1 learns de2's changes through ru2 too.
- **Merge.** State is materialized per field: the field with the highest
  stamp wins; a delete is a tombstone with a stamp and hides every field
  older than it. Applying the same ops in any order gives the same state
  (tested by permutation).
- **Single-writer data never conflicts.** Traffic and online reports belong
  to the node that measured them (origin = node, seq = its report journal);
  totals are sums over origins, not LWW.
- **Invariants that need one winner** (unique email, port on a node) are
  checked on write locally and re-checked after merge; a loser is flagged
  in the UI instead of silently dropped.

## Nodes (`fbnode`)

- The agent keeps a list of masters and talks to whichever answers first;
  it trusts the masters' ed25519 keys pinned at enrollment.
- Desired state carries the stamp of the newest op it was rendered from; the
  agent applies a state only if its stamp is higher than the applied one, so
  two masters pushing in turn cannot roll it back.
- Reports are journaled on the node and acked by whichever master received
  them; that master turns them into ops, which replicate like any other.

## Storage

The op log is append-only files per origin (length + CRC per record, torn
tail truncated on open) in pure Go; the materialized state is a SQLite (or
Postgres) projection rebuilt from the log. No NATS or Raft dependency.
Compaction: once every known master has acked a seq, ops below it can be
folded into a snapshot.

## Packages (branch `native`)

| Package | Role |
|---|---|
| `internal/native/hlc` | hybrid logical clock and stamps |
| `internal/native/oplog` | ops, per-origin durable log, version vectors |
| `internal/native/lww` | field-level last-writer-wins state |
| next: `internal/native/repl` | master ↔ master pull over mutual ed25519 |
| next: `internal/native/agent` | `fbnode`: desired state apply, report journal |
