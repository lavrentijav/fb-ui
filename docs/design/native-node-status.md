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
  surface) and is the default `fbnode` build; `relay` is the only reduced
  profile (edge only, no Xray). The production plan is in
  `native-prod-plan.md`.

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

1. **No self-update / rollback.** An agent can only be replaced by SSH
   reinstall. Needs: signed binary, `upgrade` on the agent, version reporting
   (the field exists), rollback to the previous binary when the new one fails
   to stay up.
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
| No production entrypoint | `cmd/` has only `fbnode`; `runMaster` lives in the netlab test binary. A real `fbmaster` must own config, flags, logging, signals, version. |
| No admin listen setting | `masterd.Config` has `APIListen` (modules) but no admin HTTP address; the lab hard-codes `:8080`. The admin handler has **no authentication** and must bind loopback (or sit behind the edge) — this is a security must, not a nicety. |
| Master join/bootstrap | `FleetCA` is handed in by the caller. Nothing creates/persists the CA, issues a new master's gateway certificate, or distributes the CA to a second master. `enroll` covers nodes only. |
| No DB migration | The old panel's SQLite (inbounds, clients, settings, nodes) has no importer. A master starting fresh means re-creating every inbound and client. |
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

## 4. Node profiles: `lite` (the default node) and `relay`

The split is **not** "small feature set vs big one". It is **data plane vs
control plane**:

| | `fbmaster` | `fbnode` (`lite`, default) | `fbnode --profile relay` |
|---|---|---|---|
| Admin UI / panel | yes | no | no |
| Admin API, API tokens, sessions | yes | **no** | no |
| Groups, subscriptions, settings, templates, audit, webhooks, notifications | yes | **no** | no |
| Serves users (inbounds, edge 443, certs) | — | yes | no |
| Cascades, balancers, routing rules | — | yes | routes only (edge splice) |
| Virtual networks: data plane, per-network DNS, presence, routing | — | yes | no |
| IP limits / fail2ban | — | yes | no |
| Reports, health, journal, drain | — | yes | yes (health/edge only) |
| Sidecars (TUIC, MTProto, AmneziaWG) | — | yes (to port) | no |
| Self-update / Xray install | yes | yes | yes (no Xray) |

So **`lite` is the full node**: it does everything a node must do — every
transport and protocol, every cascade shape, routing, certificates, virtual
networks, limits, accounting — and only refuses the things that belong to the
operator: no admin interaction, no user/group management, no embedded panel.
There is nothing to cut from the data plane; `lite` is the normal `fbnode`
build. The name is kept because it is what a sub-node gets: a node, not a
panel.

### What "no admin/user interaction" means concretely

The agent must never expose a management surface:

- no listening HTTP port except the edge; it dials the masters outbound only;
- no API tokens, no sessions, no audit/webhook/notification configuration;
- no group CRUD, no subscription management, no settings/template editing;
- state is pushed (desired state) and reported (journal); an operator changes a
  node only through a master.

This is already true of `cmd/fbnode`; the plan is to keep it true and to make
the build/test enforce it (a test asserting the agent opens no listener other
than `Listen`).

### Profile `relay` — edge only, no Xray

For a cheap country front, a CDN-side hop, or a hop in a cascade chain. The
edge terminates TLS and splices to an upstream with `Reverse{Kind:"tcp"}`; the
agent still pulls state, applies `Edge`/`Certs`/`Drain`, reports health and
journals. It never starts Xray, never counts traffic, never runs vdns/presence/
IP-limit. ~10–15 MB RSS; no per-user accounting on that hop.

- Master side: the spec has no inbounds and no exits; `nodespec.Render` must
  still produce `Edge`/`Certs`/`Drain` and an empty Xray config (today it always
  produces a full one).
- The relay must still rotate certificates: edge/certs are applied even when
  Xray is nil.

### Implementation shape (one binary, two profiles)

1. `config.json`: `"profile": "lite" | "relay"` (default `lite`).
2. `cmd/fbnode`:
   - `lite`: today's wiring (Xray, edge, DNS, presence, IP limits, journal).
   - `relay`: `xray.bin` optional; skip `xrayrun`, `vdns`, `presence`,
     `IPLimit`; `BundleApplier.Xray = nil`; `Node.Xray = nil`.
3. `agent.BundleApplier`: nil-guard Xray and DNS; always apply edge/certs/drain.
4. `agent.Node.Collect`: with nil Xray, send a heartbeat with health only.
5. `nodespec.Render`: a spec with no inbounds/exits renders edge/certs/drain
   and an empty Xray config.
6. Enforce the no-admin rule in a test: the agent's only listener is `Listen`.
7. Tests: nil-safe apply/collect per profile, and a netlab
   `TestRelayNodeForwards` (a relay passes a tunnel to an exit, reports health,
   traffic stays zero on the relay).

### Recommendation

Ship `lite` as the default and only node artifact; add `relay` as a runtime
profile of the same binary first. Build tags (`no_xray`, `no_vdns`,
`no_failban`) only if a relay image must be handed to untrusted hosts and its
size matters.

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
