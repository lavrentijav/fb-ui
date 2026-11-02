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
- **Lite profile for sub-nodes:** designed, not built. Two honest variants; the
  recommendation is one binary with a `profile` setting.

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

## 4. Lite profile for sub-nodes

Everything above is one agent binary. A sub-node in this architecture is a node
that does not carry users of its own: it either forwards traffic to a parent
node/exit, or only serves a small set of users without networks. Two profiles,
both runtime-switchable so a node can be promoted without reinstalling:

### Profile `relay` — no Xray at all

The edge terminates TLS and splices to an upstream (the parent node or an
exit's cascade inbound) with the existing `Reverse{Kind:"tcp"}` path. The agent
still pulls state, applies `Edge`/`Certs`/`Drain`, reports health, and journals;
it never starts Xray, never collects traffic (there is no local accounting),
never runs vdns/presence/IP-limit.

- Footprint: only the Go runtime + NATS + edge. Expect ~10–15 MB RSS versus
  15–25 MB for the agent alone plus 25–110 MB for Xray.
- Use: a cheap country front, a CDN-side hop, a hop in a chain of cascades.
- Master side: the spec has no inbounds; routes are `Reverse` TCP entries.
  Nothing to render for Xray, so `Render` must skip the Xray config (today it
  always produces one).

### Profile `lite` — full features minus the multi-tenant extras

Xray and the edge as usual, but no virtual-network DNS, no presence
publication, no IP-limit watcher/fail2ban. For nodes whose users have no
`limitIp` and are in no network.

- Footprint: saves the three small goroutine sets and a few MB of RSS; the real
  win is a smaller failure surface and no loopback DNS listeners on a node that
  does not need them.
- Master side: refuse to render `VNet`/limits for a lite node (a clear error
  instead of silently dropping them).

### Implementation shape (one binary)

1. `config.json`: `"profile": "full" | "lite" | "relay"` (default `full`).
2. `cmd/fbnode`:
   - `relay`: `xray.bin` optional; skip `xrayrun`, `vdns`, `presence`,
     `IPLimit`; `BundleApplier.Xray = nil`; `Node.Xray = nil`.
   - `lite`: skip `vdns`, `presence`, `IPLimit`.
   - `full`: today's wiring.
3. `agent.BundleApplier`: nil-guard Xray and DNS (apply edge/certs/drain
   always — a relay must still rotate certificates).
4. `agent.Node.Collect`: with nil Xray, send a heartbeat with health only.
5. `nodespec.Render`: when a spec has no inbounds and no exits (a relay), still
   produce `Edge`/`Certs`/`Drain` and an empty Xray config (or a marker the
   agent ignores).
6. Optional build tags `no_vdns`, `no_failban`, `no_xray` to cut the packages
   for a relay-only build; a relay build should stay under ~6 MB.
7. Tests: a unit test per profile guard (nil-safe apply/collect) and a netlab
   `TestRelayNodeForwards` (a relay node with no Xray passes a tunnel to an
   exit and reports health, not traffic).

### Recommendation

Ship one binary with the runtime profile first (low risk, no matrix of
artifacts), and add the build tags only if a relay image needs to be
distributed to untrusted hosts. Both profiles are additive: the full agent and
the lab stay as they are, and the master already has everything it needs to
render the routes — only the "no inbounds" render case is new.

## 5. One-paragraph summary for the operator

The node agent is finished enough to run a pilot on real entry and exit nodes:
it enrolls itself, keeps serving when the masters are unreachable, applies
changes without dropping tunnels, and the lab exercises all of it (22/23, the
ACME test needs Pebble). Three things remain before a fleet rollout — agent
self-update, sidecars, and packaging/history. The native master is **not**
ready: the code is tested, but there is no production binary, no safe way to
join a second master or migrate the existing database, and the admin HTTP
surface is unauthenticated today, so it must not be exposed. A sub-node can be
made much lighter with a `relay` profile (edge only, no Xray) or a `lite`
profile (no networks/IP limits); the design is in this file, and it is a
contained change to `cmd/fbnode`, `agent` and `nodespec.Render`.
