# fbnode and the native master: readiness, and a lite node profile

Status: 2026-11-14, branch `work/native-ready` off `native` at `f6250a6c`.
Verdict up front:

- **Cut over on 2026-11-14.** The old x-ui is stopped and disabled on ru2,
  dns1, dns2, fi1, de3, nl3 and de2. The native master serves `:443` on ru2
  with the ACME certificate; six data nodes run fbnode (fi1, dns1/`ru1`,
  dns2/`ru`, de3, nl3, de2); subscriptions and the sales bot use the native
  API. The fleet passed an end-to-end egress test through every node.
- **fbnode (node agent): pilot-ready on real entry/exit nodes.** It does the
  whole traffic plane and survives the master being down. Relay no longer
  creates a presence watcher, so an edge-only hop starts cleanly. Still not
  ready for unattended fleet rollout: no sidecars, no per-node history, no
  core install/upgrade.
- **native master: live for the supervised fleet.** It has a production
  binary (`--version`, config, `-check`, `-create-ca`), authenticated
  admin/API, bootstrap admin token, master join, a built UI and an importer
  that handles both old database shapes with dry-run/apply/verify. Not ready
  for unattended production: no backup drill, no master upgrade path, no
  per-node history, and some legacy UI sections are still holes.
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
fixes in `pool` and `sub`. Re-run on 2026-11-13 with Xray 26.9.9 built from
the version `go.mod` pins: the full netlab suite exited 0, with the ACME test
skipped for the missing Pebble/challtestsrv. The separate Xray API and
`xrayrun` E2E tests also pass against that binary.

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
2. **No sidecars yet** (TUIC, MTProto, AmneziaWG) — the supervisor exists
   (`internal/native/sidecar`: atomic config, restart only on change,
   backoff restart, stop on removal, tested) and `Bundle.Sidecars` carries the
   set; what remains is a renderer per protocol. Not needed for a VLESS-only
   pilot.
3. **Xray is not installed or upgraded by us.** `provision` refuses a host
   whose `xray.bin` is missing; there is no download/upgrade of the core.
4. **No per-node history.** Only the newest report is kept (`/nodes/state`);
   charts need `reportdb`/history on the master.
5. **No packaging.** No Makefile/CI target, no version stamping, no `.deb`/unit
   template outside `provision.install`.
6. **Node host DNS is manual.** Provisioning does not create the node's
   `node.<zone>` record; the wizard needs an A record typed by the operator
   (the docs list this as "missing").

## 2. The native master: what was fixed and what remains

The control plane is implemented and tested (modules, `/api/v1`, sessions,
compat `/panel/api`, subscriptions, settings, notifications, webhooks, audit,
access, pool, vnet, certs, routing). Since the last status:

- the production entrypoint, loopback-by-default authenticated admin, fleet
  CA, master join and bootstrap admin token are in place;
- the built interface is served at `/panel`, logs in with the bootstrap/API
  token, and gets `/api/v1/ui/*` and `/api/v1/access/*` through the admin
  surface (the outer gate now lets `/api/v1/*` reach the routes that carry
  their own token/session check);
- the importer handles upstream `inbounds.settings` clients and this fork's
  client records, with `-schema`, Markdown/JSON reports, redaction, read-only
  source, idempotent apply and `-verify`;
- subscriptions carry operator-defined custom parameters (`subparam` table,
  `/api/v1/subs/{token}/params`) and extra public aliases
  (`/api/v1/subs/{token}/aliases`); the importer keeps every old panel `subId`
  as an alias, so old links keep resolving after the cutover;
- the master's edge serves `poolName` (panel) and every `subNames` entry
  (subscriptions only) from one ACME SAN certificate, and serves the old
  `/walksub/<token>` and `/frvpn/<token>` paths natively; on 2026-11-14 the
  ru2 staging master issued that certificate (master, sub and the two
  `walk.boykisser.online` names, valid to 2027-02-12) through the Technitium
  API token;
- a two-master rehearsal (ru2 issuer, fi2 joined non-issuer) replicated all
  70 staging subscriptions in both directions over the mTLS gateway on 17222;
  a joined master reports pool health but never takes the DNS lease;
- the sales bot runs the native adapter live (`XUI_BACKEND=native`), keeps
  Platega enabled and reaches the master over loopback;
- the live fleet is fi1, dns1 (`ru1`), dns2 (`ru`), de3, nl3 and de2; the
  common `ru.firewalk.top` is a multicast of dns1/dns2. de2 cannot reach ru2
  directly, so it uses fi2's bus, exposed on `fi2.firewalk.top:9443`;
- `make native-release` produces both static binaries, the UI tarball, units,
  `SHA256SUMS` and `build-info.md`.

Remaining blockers for unattended production:

| Blocker | Detail |
|---|---|
| Store choice | JetStream only; the optional Postgres projection is not documented or exercised for the fleet. |
| Cert/ACME bootstrap | `acme: true` in the master config wires issuance; DNS accounts are still created through `PUT /dns/{id}` and there is no first-run wizard. |
| DNS API token | Resolved: the token lives in Vaultwarden `dns/dns1-technitium-api` and the staging master issued a certificate with it. The zone's secondaries lag a NOTIFY by ~12 s and Let's Encrypt caches a failed lookup for the SOA minimum (15 min), so a retry belongs at least 15 min after a failure. |
| Public HA | ru2 serves the pool; fi2 replicates and validates the bus but its public `:443` is nginx for the old sites. Making fi2 a public pool member needs an nginx stream/SNI move. |
| Remaining nodes | de1 and nl2 are not enrolled; fi2 is a master, not a data node. The common `de`/`fi`/`nl` names are not configured yet. |
| Backup/DR | The stop/copy/start procedure is written, but the restore drill has not been run. |
| Upgrades | No master upgrade/rollback path; an in-place restart relies on JetStream replay. |
| History | Only the newest report per node is kept; charts need `reportdb`/history and a retention policy. |
| UI holes | Nodes, certs, DNS and parts of routing still have old routes without a native mapping; the compat adapter answers them with a message, not data. |

## 3. Ship checklist

Node (pilot-ready → production-ready):

1. [x] `make fbnode` with version stamping; a static binary + SHA-256
   (`make native-release`).
2. [x] Agent self-update with rollback (staged binary, health gate, revert).
3. [ ] Sidecars as agent modules (at least TUIC and AmneziaWG for parity).
4. [ ] Report history on the master (reuse `reportdb`; the schema exists).
5. [ ] `provision`: create the node's DNS record through `dnsacct`; optional
   Xray install/upgrade.

Master (not ready → ready):

1. [x] `cmd/fbmaster` (config file, flags, `--check`, `--version`, signals) and
   a `Config.AdminListen` that defaults to `127.0.0.1:0` (loopback only).
2. [x] First-run: generate or load the fleet CA, print the bootstrap admin
   token, refuse to start with a world-open admin listener.
3. [x] Master join: a token that issues a master's gateway certificate from
   the fleet CA, plus CA distribution to peers.
4. [x] Importer from the old panel's SQLite (inbounds, clients, groups) with a
   dry-run report, `-schema`, redaction, idempotent apply and `-verify`. See
   `native-migration.md`.
5. [x] CI builds the master and the node, builds the UI, and ships versioned,
   checksummed artifacts; `--version` works on both binaries.
6. [ ] Backup/restore drill of the JetStream directory and the Postgres
   schema; an upgrade path that replays the op log on a new binary.

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
changes without dropping tunnels, and the lab exercises the data plane (22/23;
the ACME test needs Pebble). Relay is now validated to start without Xray or a
presence watcher. Remaining before a fleet rollout: sidecars, per-node history
and core install/upgrade. The native master is pilot-ready for a supervised,
loopback-only single master: authenticated admin, bootstrap token, join, UI,
and an importer that handles both old database shapes with dry-run/apply/verify.
It is still not ready for unattended production: no backup drill, no master
upgrade path, no history, and some legacy UI sections remain holes. A sub-node
gets the same `lite` node build — everything except the admin and user surface
— and only a cheap forwarding hop needs the reduced `relay` profile (edge only,
no Xray). The step-by-step production plan is in `native-prod-plan.md`.
