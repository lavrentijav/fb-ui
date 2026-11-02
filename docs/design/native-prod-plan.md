# Native production plan

Status: 2026-11-02, branch `native`. Companion to `native-node-status.md`
(what exists) and `native-core.md` (target shape). This file is the order of
work from "tested code" to "runs the fleet", with acceptance criteria.

## Definition of done

A master and a node are production-ready when all of these hold:

1. **Deployable:** one versioned artefact each (`fbmaster`, `fbnode`), built by
   CI, with `--version`, a checksum and a documented config file.
2. **Safe by default:** the admin surface is authenticated and loopback-only by
   default; a master refuses to start exposed without saying so; no secret is
   logged or returned twice.
3. **Joinable:** a fresh master can join an existing fleet with one token; a
   fresh node can enrol with one token; both work without a human copying key
   material.
4. **Recoverable:** the agent keeps serving when every master is down; a master
   restart replays the op log; a JetStream directory can be backed up and
   restored; the last known configuration is on the node's disk.
5. **Upgradable:** a node and a master can be updated in place with a health
   gate and automatic rollback.
6. **Migratable:** the existing panel's SQLite can be imported with a dry-run
   report and a cutover/rollback runbook.
7. **Observable:** node online/offline, last error, traffic, certificate expiry
   and master quorum are visible without SSH.
8. **Tested:** the lab suite is green; a failure-injection run (kill a master,
   kill a node's master link, fill a disk) has documented outcomes.

## Phase 0 — Safety (do first, small)

Why first: the master's admin handler has no authentication and the lab binds
`:8080`; the status doc already says this must never be exposed. Everything
else can wait; this cannot.

- [x] `Config.AdminListen` (default `127.0.0.1:0`); `ListenAdmin` logs the
      resolved address.
- [x] Refuse to bind a non-loopback admin address unless an explicit
      `AdminAllowPublic: true` is set (`AdminAddress`).
- [x] Gate every admin route with `apiauth`/`uimod` (`apiserver.RequireAuth`,
      `masterd.AdminHandler`); a not-ready master refuses rather than serving.
- [x] Admin path audit (`PublicAdminPath`): only `/healthz`, `/ready`,
      `/enroll[/…]`, `/sub/…` and `/panel/…` are credential-free — each carries
      its own token or is the public interface; everything else needs a bearer
      token or the panel's session cookie. Tests pin the classification.
- [x] Redaction: the bootstrap admin token is the only credential logged, once
      and deliberately; the refusal body never echoes a credential (tested) and
      no list endpoint returns a password or hash (subscription tests).
- Acceptance: `curl` from another host gets 401/403/connection refused; lab
  masters still pass with the loopback default.

## Phase 1 — Master entrypoint and first run

- [x] `cmd/fbmaster`: config file (`/etc/fbmaster/config.json`), flags `-c`,
      `--check`, `--version`, `-create-ca`; signal handling; logs; one process.
- [x] `--check` validates the config, peer URLs, admin loopback rule, that the
      data directories are writable, and prints every address it would bind.
- [ ] Store choice: JetStream dir always; Postgres projection optional behind a
      DSN; document which data lives where. (JetStream only so far.)
- [x] Fleet CA: loaded from `caCert`/`caKey`, or generated once with
      `-create-ca` and logged by fingerprint; never regenerated silently, and a
      half-present pair is an error.
- [x] Bootstrap admin token: the existing `bootstrapToken` logs it once on the
      leader and refuses while any token exists.
- [x] First-run text: the master logs the interface directory (or says there
      is none), the admin address and the intended flow; the token line is the
      credential for `fbnode enroll`.
- [x] `GET /healthz` and `GET /ready` on the admin surface: 200 once the
      master is ready (modules started), 503 while booting; both need no
      credential. A runtime test starts a master from a config file, waits for
      healthz, asserts an unauthenticated admin call is refused, and stops it.
- Acceptance: a clean VM with one config file serves the admin API on
  loopback, emits a token, and a scripted test does login → list nodes.

## Phase 2 — Fleet join, CA distribution, pool

- [x] Master join token (`internal/native/masterjoin`): `POST /master/tokens`
      mints a one-time token (name, ttl) and answers the token plus the CA
      fingerprint; `POST /master/join` redeems it for a gateway certificate
      signed by the fleet CA. The token is `mst1_<secret>_<caPin>`, so the
      joining host pins the CA out of band. `fbmaster join -master -token
      -name -dir` writes `ca.pem`, `master.pem`, `master-key.pem` (0600 key)
      and prints the config paths.
- [x] CA distribution: the joining master receives the CA certificate; it
      needs no CA key to serve or connect. (A master that must issue shares
      the key, as today; a per-master "issuer" flag is still open.)
- [ ] Document the trust model in full: which masters may issue node
      certificates, which may issue gateway certificates.
- [ ] Pool DNS: first-run guidance for the pool name, the DNS account
      (`PUT /dns/{id}`) and the certificate; a check that warns when the pool
      name has no certificate or no A record.
- [ ] Multi-address already exists (`ips`, `iphealth`, `PUT /net/ips`); add the
      runbook: detect → announce → verify with `GET /net/ips`.
- Acceptance: start master A, join master B with one token, both serve
  `/api/v1`; kill B, A keeps the fleet; DNS pool drops B after the configured
  fraction (lab extension of `TestMastersDriveNodes`/pool tests).

## Phase 3 — Node production hardening

The node data plane is done (`lite` = full node, dependent on the master, with
its own micro SQLite). Remaining work:

### 3.1 Dependent node: micro store, local enforcement, profiles

A node is dependent: the master owns the database, the node keeps only a
micro SQLite (`internal/native/nodestore`, already implemented: state,
journal, traffic, bans, meta) and rebuilds it from the master if lost.

- [x] `nodestore`: pure-Go SQLite (static, +4.2 MB: 8.8 → 13.0 MB), 5 tables,
      tests for state, journal numbering across prune, traffic, bans, schema.
- [x] Move `desired.Applier` state and `agent.Journal` onto nodestore for
      `lite` (`OpenApplierBackend`, `StoreJournal`, `Reporter`); `relay` keeps
      the small files.
- [x] Local enforcement in `agent`: the bundle carries each client's
      `totalBytes`/`expiresAt` (`nodespec.Quota`); `agent.Quota` cuts a client
      from the running config when its own counter reaches the limit or it
      expired (`BundleApplier.SetDisabled` rewrites only that client), and
      restores it when the master's correction removes the record. The master's
      `usagemod` remains the reconciler.
- [x] `profile` in the node config and `fbnode enroll -profile`: `lite`
      (default) and `relay` (edge only, no Xray, no store).
- [x] Nil-safe `BundleApplier`/`Node.Collect` (relay runs no core; tested).
- [ ] `nodespec.Render` for a spec with no inbounds (edge/certs/drain, empty
      Xray config).
- [x] Test: the node binary opens no listener of its own
      (`cmd/fbnode/listeners_test.go`); the edge is served by `edgerun`.
- [ ] Lab `TestRelayNodeForwards`.

### 3.2 Delivery
- [x] `make fbnode` / `make fbmaster` / `make native-binaries`: static
      (`CGO_ENABLED=0`), `-trimpath`, version from `git describe`, printed
      SHA-256, into `dist/native` (git-ignored).
- [ ] systemd unit template in the repo (today it is generated inside
      `provision`); no config secrets in the unit. Note the unit needs
      `Restart=always`: `fbnode upgrade` relies on it to start the new build.
- [ ] `provision`: install Xray when missing (download, verify checksum,
      pin a version) instead of refusing; optional core upgrade.

### 3.3 Self-update
- [ ] Signed release manifest (checksum + signature with a fleet key). Today
      the operator passes the sha256 explicitly.
- [x] `fbnode upgrade -url -sha256`: downloads, verifies the digest, keeps the
      running binary as `<current>.previous`, renames the new one into place,
      writes a marker and exits so the supervisor starts it.
- [x] Rollback: each start counts in the marker; after three failed starts the
      previous binary is restored and the process exits; a build that stays up
      past `HealthyAfter` (60 s) clears the marker.
- [x] `--version` on both binaries, and the agent reports its version with
      health (the field already existed).
- [ ] Master command `POST /nodes/{node}/upgrade` and per-node version in
      `/nodes/state`.
- Acceptance: lab: upgrade a node to a bad build, it rolls back and keeps
  serving; upgrade to a good build reports the new version.

### 3.4 Sidecars (parity with the main line)
- [x] `internal/native/sidecar`: a generic supervisor with the same rules as
      Xray — only a changed spec restarts (hash of binary/args/env/config), a
      config file is written atomically, an unexpected exit is restarted with
      backoff while the spec is still desired, and removing it stops the
      process (SIGTERM, then kill after a timeout). `Bundle.Sidecars` carries
      the set and `nodespec.Spec` validates it, so a bad sidecar fails the
      render rather than the node.
- [ ] Renderers for TUIC, MTProto (mtg) and AmneziaWG: their configs from the
      old panel's models into `sidecar.Spec` (this is the remaining work — the
      process manager is done).
- [ ] Traffic accounting from sidecars where the sidecar exposes it; otherwise
      document "not counted" and surface it.
- Acceptance: lab: each sidecar carries a tunnel, a config change does not cut
  an open one, and a crash is restarted.

### 3.5 Node lifecycle
- [ ] Provision creates the node's DNS record through `dnsacct`.
- [ ] Drain runbook: how to take a node out, what "active" means, when it is
      safe to stop.
- [ ] Node history: reuse `reportdb`/history tables; a per-node chart endpoint
      and a retention policy.
- [ ] Certificate expiry in `/nodes/state` (the data exists in `certs`).

## Phase 4 — Migration from the old panel

- [x] Importer (`internal/native/importpanel`): opens the old SQLite read-only
      and reads inbounds, clients, `client_inbounds`, nodes and groups; the
      plan maps VLESS ws/xhttp inbounds to native inbounds and one client
      record per client and inbound. Pure, unit-tested on a fixture database.
- [x] Dry run: `fbmaster import -db ... -json` prints every planned record and
      every skip with its reason (other protocols/transports, an inbound of
      the old panel itself without `-local-node`, a client with no link row),
      plus notes for settings/templates/hosts/certs that are re-created
      through their modules.
- [x] Apply: `fbmaster import ... -apply -master URL -token T` posts inbounds,
      clients and groups to the authenticated admin surface; an existing
      record is kept (409) so a rerun is safe.
- [x] Cutover runbook and rollback in `native-migration.md` (snapshot, plan,
      fleet, apply, verify, switch, retire; the old DB is never written).
- [ ] Dual-run period: a node may report to both (or the old panel keeps the
      subscription while the new master serves traffic).
- Acceptance: import the lab's or a copy of the real DB, every inbound/client
  appears in `/api/v1` and renders the same subscription links as the old panel
  (a diff test over link sets).

## Phase 5 — UI and release

- [ ] CI builds the frontend once and ships `dist/` beside the binary (no Node
      on the server); `UIDir` documented.
- [ ] Login UX for the bootstrap token (paste once, session cookie), and a
      "create the first operational token" step.
- [ ] Per-section legacy routes still missing (`nodes`, `certs`, `dns`,
      `routing` pages call `/panel/api/*`); finish them so no page 404s.
- [ ] Release workflow: master + node + UI, versioned, checksummed, with
      upgrade notes.
- Acceptance: a release tarball on a clean VM gives a working panel, a
  subscribable client and an enrolled node with no source checkout.

## Phase 6 — Operations

- [ ] Backup/restore: JetStream dir snapshot, Postgres dump, a restore drill.
- [ ] Master upgrade: new binary, replay, health gate, rollback to the previous
      binary; document the disk-format assumption.
- [ ] Monitoring: node up/down and certificate expiry already exist as events;
      add a documented webhook/Telegram recipe and a `/healthz` check.
- [ ] Failure drills: kill a master, kill the link to the node's master, fill
      the disk, revoke a node certificate, rotate the pool certificate.
- [ ] Security review: admin auth, token storage, session cookies, header
      injection, path traversal on `/panel` and `/subpage.html`, secret logs.
- [ ] Load check: N nodes × M clients reporting, the leader's event delivery,
      subscription fetch under concurrency.
- [ ] Documentation set: install, join, enrol, upgrade, backup, migrate,
      troubleshoot (one page each in the operator docs).

## Order and dependencies

| Order | Phase | Blocks | Effort |
|---|---|---|---|
| 1 | 0 Safety | everything that exposes a port | days |
| 2 | ~~1 Master entrypoint~~ | any real deployment | 1 week (done) |
| 3 | 3.1 Profiles (lite/relay) | cheap sub-nodes | days |
| 4 | 3.2 Delivery + 3.3 Self-update | unattended nodes | 1–2 weeks |
| 5 | 2 Fleet join | multi-master production | 1 week |
| 6 | 4 Migration | replacing the old panel | 1–2 weeks |
| 7 | 3.4 Sidecars | full parity | 1–2 weeks |
| 8 | 5 UI/release | operator-facing release | 1 week |
| 9 | 6 Operations | sign-off | continuous |

`lite` is not a phase of its own: it is the default node. Its work in 3.1 is
the micro store wiring (already built), local quota enforcement and the
`relay` profile with its guard rails.

## What "pilot" means, precisely

Allowed now on real nodes, one at a time, with an operator watching:

- entry and exit nodes carrying real users, with subscriptions served by the
  current panel or a native master;
- traffic, quotas, IP limits, certs, drain, cascades, virtual networks;
- rollback = point the node's config back at the old panel and restart.

Not allowed yet: unattended fleet rollout, migration of the database, exposing
the admin API, or running a master without an SSH fallback.

## Open decisions

1. Release signing key: where it lives, who holds it, how a node gets the
   public half (bootstrap config vs first master push).
2. Persistent store for nodes: keep "last state + journal" (current) or add a
   small SQLite for history on the node? Current answer: keep it minimal, the
   master owns history.
3. Sidecar scope for 1.0: TUIC + AmneziaWG only, or MTProto too?
4. Migration scope: all entities, or inbounds/clients/settings first and the
   rest later?
5. `fbmaster` and `fbnode` in one repository (one `go build` target each) vs
   separate modules. Current answer: one repository, two commands.
