# fbnode and the native master: readiness, and a lite node profile

Status: 2026-11-02, branch `native` at `ec6812efb`. Verdict up front:

- **fbnode (node agent): ready for a supervised pilot on real entry/exit nodes.**
  It does the whole traffic plane and survives the master being down. It is
  not yet ready for unattended fleet rollout: no self-update, no sidecars, no
  per-node history, no packaging/versioning.
- **native master: not ready to roll out.** The control plane exists and is
  tested, but there is no production binary, no bootstrap/join path for
  masters, the admin API is unauthenticated, and there is no migration from
  the panel's database. Details and a checklist below.
- **Sub-nodes:** `lite` is the full node (complete data plane, no admin/user
  surface, dependent on the master) and is the default `fbnode` build; it
  keeps a micro SQLite (state, journal, traffic counters, bans) and never a
  copy of the panel database. `relay` is the only reduced profile (edge only,
  no Xray, no SQLite). The production plan is in `native-prod-plan.md`.

## 1. What fbnode does today

Measured: static build (CGO off, `-trimpath -ldflags '-s -w'`) is **8.8 MB**
with **245** transitive packages, no gin/gorm/sqlite/pgx in the graph. The
RSS is dominated by Xray, not by the agent.

| Capability | State | Where |
|---|---|---|
| Enrollment (`fbnode enroll`, one-time token, CA pinned by fingerprint) | done, lab `TestNodeEnrollsItself` | `enroll`, `cmd/fbnode` |
| Nearest-master pick + probing + rebind | done, lab drives it | `pick`, `probe` |
| Desired state pull, last state kept on disk, apply before any master answers | done, unit + lab | `desired`, `agent` |
| Journaled at-least-once reports, retry to another master | done, unit + lab | `agent/journal`, `agent/sender` |
| Xray process control, hot apply without cutting tunnels, restart only when needed | done, lab `TestXrayChangesKeepTunnels` | `xrayrun` |
| Edge 443: SNI → TCP/HTTP, reverse, drain (finish open tunnels, refuse new) | done, lab `TestDrainKeepsOpenTunnels`, `TestReverseReachesThePlayer` | `edgerun` |
| Certificates from the masters' ACME, per-node certs, rollback | done (ACME lab skipped here: needs `NETLAB_PEBBLE*`) | `certs`, `certsmod` |
| Traffic collection, quotas, disabling an exhausted client | done, lab `TestUsageDisablesAnExhaustedClient` | `xrayrun`, `usagemod` |
| IP limits (ban the older device), fail2ban | done, lab `TestIPLimitBansTheOlderDevice` | `agent/iplimit`, `failban` |
| Virtual networks: members, per-network DNS, presence-based routing, client config | done, lab `TestMembersOnDifferentNodes` | `vnet`, `vdns`, `presence`, `vroute` |
| Cascades and group balancers (leastPing, fallback) | done, lab `TestGroupBalancerFailsOver`, `TestCascadeThroughEdges` | `nodespec`, `register` |
| Health (load/memory/uptime) reported with traffic | done | `reports` |
| Subscriptions, sub page/login/vnet, metadata | done, lab | `sub`, `submeta`, `masterd` |

Lab: **23 tests, 22 pass, 1 skip** (the ACME test needs Pebble and
challtestsrv binaries in the environment). No known flake after the timing
fixes in `pool` and `sub`.

### Why it is safe for a pilot

- A node with no reachable master keeps serving the last applied config; the
  journal replays the gap when a master returns.
- Every change goes through a hash-compared bundle: Xray is restarted only
  when the config really changed, otherwise users/inbounds/routing are applied
  through the Xray API, so tunnels are not cut.
- An agent never exposes a management port: it dials out, authenticated by its
  fleet certificate.

### What blocks unattended production, in order

1. ~~No self-update / rollback~~ — done: `fbnode upgrade` verifies a pinned
   sha256, swaps with a `.previous` file, and the next starts roll back after
   three failures; a healthy run clears the marker. Still missing: a signature
   over the manifest and a master command to trigger it.
2. **No sidecars** (TUIC, MTProto, AmneziaWG) — parity with the main line's
   node. Not needed for a VLESS-only pilot.
3. **Xray is not installed or upgraded by us.** `provision` refuses a host
   whose `xray.bin` is missing; there is no download/upgrade of the core.
4. **No per-node history.** Only the newest report is kept (`/nodes/state`);
   charts need `reportdb`/history on the master.
5. **No packaging.** No Makefile/CI target, no version stamping, no `.deb`/unit
   template outside `provision.install`.
6. **Node host DNS is manual.** Provisioning does not create the node's
   `node.<zone>` record; the wizard needs an A record typed by the operator
   (the docs list this as "missing").

## 2. The native master: why not yet

The control plane is implemented and tested (modules, `/api/v1`, sessions,
compat `/panel/api`, subscriptions, settings, notifications, webhooks, audit,
access, pool, vnet, certs, routing), but the deployment story is missing:

| Blocker | Detail |
|---|---|
| ~~No production entrypoint~~ | done: `cmd/fbmaster` owns config, `--check`, `--version`, `-create-ca`, signals and logging. |
| ~~No admin listen setting / unauthenticated admin~~ | done: `AdminListen` defaults to loopback, a public bind needs `AdminAllowPublic`, and `AdminHandler` requires a token or the panel session (phase 0). |
| ~~Master join/bootstrap~~ | mostly done: `fbmaster` creates/persists the CA, `POST /master/tokens` + `POST /master/join` and `fbmaster join` give a new master its CA and certificate without copying keys. Open: a per-master "issuer" flag and the full trust-model write-up. |
| ~~No DB migration~~ | partly done: `fbmaster import` plans and applies inbounds, clients and groups from the old SQLite (VLESS ws/xhttp); other protocols/transports and settings/hosts/templates are reported and re-created through their modules. See `native-migration.md`. |
| UI not shipped | `UIDir` is a directory of built assets; nothing builds or ships it in CI, and the login needs a bootstrap API token (logged once) — an operator workflow around that is needed. |
| Cert/ACME bootstrap | DNS accounts are created through the admin API (`PUT /dns/{id}`) and the pool certificate depends on them; there is no first-run wizard. |
| Backup/DR | JetStream store and the optional Postgres projections have no backup/restore procedure. |
| Upgrades | No master upgrade path at all; an in-place restart relies on JetStream replay, which works but is untested for disk-format changes. |

## 3. Ship checklist

Node (pilot-ready → production-ready):

1. `make fbnode` with version stamping; publish a static binary + SHA-256.
2. Agent self-update with rollback (staged binary, health gate, revert).
3. Sidecars as agent modules (at least TUIC and AmneziaWG for parity).
4. Report history on the master (reuse `reportdb`; the schema exists).
5. `provision`: create the node's DNS record through `dnsacct`; optional Xray
   install/upgrade.

Master (not ready → ready):

1. `cmd/fbmaster` (config file, flags, `--check`, version, signals) and a
   `Config.AdminListen` that defaults to `127.0.0.1:0` (loopback only).
2. First-run: generate or load the fleet CA, print the bootstrap admin token
   and the enroll command, refuse to start with a world-open admin listener.
3. Master join: a token that issues a master's gateway certificate from the
   fleet CA, plus CA distribution to peers.
4. Importer from the old panel's SQLite (inbounds, clients, settings, hosts,
   subscriptions) with a dry-run report.
5. CI: build the master and the node, build the UI (or ship `dist`), produce
   the release artifacts; a `--version` on both binaries.
6. Backup/restore of the JetStream directory and the Postgres schema; an
   upgrade path that replays the op log on a new binary.

## 4. The node is dependent: micro store, not a database

A node in this architecture is **not** a small panel. It holds no copy of the
panel's database — no clients table, no inbounds table, no groups, no
settings, no users. The master is the only source of truth; the node receives
a rendered spec and reports back. What it keeps locally is a **micro SQLite**
(`internal/native/nodestore`, pure-Go driver, CGO-free) with only the facts a
node must not lose:

| Table | What | Why it is local |
|---|---|---|
| `state` | the last applied desired state (node, hash, stamp, config) | start serving before any master answers |
| `journal` | numbered reports, unacked until a master confirms | at-least-once traffic delivery, no double counting |
| `traffic` | per-client up/down counters | enforce quota/expiry without a master round trip |
| `bans` | IP bans the limit watcher issued (ip, email, reason, until, hits) | survive a restart, still block the device |
| `meta` | schema version, journal cursor | numbering/version safety |

That is the whole schema (5 tables, ~100 lines of SQL). Rebuilt from the
master if lost: a node store is disposable, never migrated — an unknown schema
version is refused rather than half-read. Cost, measured: the static agent
grows from 8.8 MB to **13.0 MB** with the pure-Go SQLite driver (+4.2 MB), and
RSS stays dominated by Xray.

### What a node still does in full

"Nodet" here does not mean fewer features. A node keeps the **complete data
plane**: every transport and protocol, edge 443, cascades and balancers,
routing rules, certificates, virtual networks (data, per-network DNS,
presence-based routing), IP limits, traffic counting, quotas, reports, health,
journal, drain, sidecars. Traffic and everything around it is local:

- counts per email from the Xray stats API, durably in `traffic`;
- enforces a client's `totalBytes`/`expiresAt` from its own spec **locally**,
  so a user is cut at the limit even if the master is unreachable; the master
  stays authoritative and corrects the counters on the next report;
- keeps its own bans and applies them through Xray/fail2ban;
- reports deltas from the `journal`, which survives restarts and failover.

### What it deliberately does not have

- no admin API, no API tokens, no sessions, no panel, no UI;
- no group or user management, no subscription management, no settings or
  template editing;
- **no listener except the public edge** — it dials the masters outbound; an
  operator changes a node only through a master.

That is what "lite" means: the full node without the operator surface.

### Profile `relay` (the only reduced one)

For a cheap country front or a CDN-side hop: edge-only forwarding with
`Reverse{Kind:"tcp"}`, no Xray, no traffic accounting, no vdns/presence/IP
limits, and **no SQLite** (the journal/state files it already has are enough;
this keeps the relay small). ~10–15 MB RSS. Master side: a spec with no
inbounds renders edge/certs/drain and an empty Xray config.

### Implementation shape

1. `config.json`: `"profile": "lite" | "relay"` (default `lite`), and
   `fbnode enroll -profile relay` writes it for a forwarding hop.
2. `cmd/fbnode`: `lite` wires nodestore, Xray, edge, DNS, presence, limits;
   `relay` skips Xray/DNS/presence/limits and keeps the file journal.
3. `desired.Applier` and `agent.Journal` are on nodestore for `lite`
   (`OpenApplierBackend`, `StoreJournal`, `Reporter` interface) and stay
   file-based for `relay` — done.
4. Local enforcement lives in `agent` next to `ipLimit`: compare the counter
   with the spec's `totalBytes`/`expiresAt`, stop the client through Xray,
   report the event; the master's `usagemod` remains the reconciler.
5. A test asserts the agent opens no listener except `Listen` (the no-admin
   rule), and a netlab `TestRelayNodeForwards` covers the forwarding hop.

### Recommendation

`lite` is the default and the only node artefact; `relay` is a runtime profile
of the same binary. The micro store is implemented, tested and wired
(`internal/native/nodestore`): the applied state and the report journal already
live in it for `lite` (`desired.OpenApplierBackend`, `agent.StoreJournal`),
and the file journal remains for `relay`. Tests cover state across reopen,
journal numbering across prune, traffic, bans with expiry and schema
refusal. Local quota enforcement is implemented: the bundle carries each
client's allowance and expiry, and `agent.Quota` stops a client in the
running config from the node's own counters, so a user is cut at the limit
even when no master answers; the master's reconciler still wins.

## 5. One-paragraph summary for the operator

The node agent is finished enough to run a pilot on real entry and exit nodes:
it enrolls itself, keeps serving when the masters are unreachable, applies
changes without dropping tunnels, and the lab exercises all of it (22/23, the
ACME test needs Pebble). Three things remain before a fleet rollout — agent
self-update, sidecars, and packaging/history. The native master is **not**
ready: the code is tested, but there is no production binary, no safe way to
join a second master or migrate the existing database, and the admin HTTP
surface is unauthenticated today, so it must not be exposed. A sub-node gets the
same `lite` node build — everything except the admin and user surface — and
only a cheap forwarding hop needs the reduced `relay` profile (edge only, no
Xray). The step-by-step production plan is in `native-prod-plan.md`.
