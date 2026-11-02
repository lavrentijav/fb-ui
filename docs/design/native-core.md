# Native core: plan

Status: accepted 2026-09-27, lives on branch `native`. Not the top priority:
a parallel track that gradually rewrites the codebase — nodes first, until
they are production-ready, then the master. The main line
(`sync/upstream-3.8.5`, later `main`) keeps serving the fleet meanwhile;
its commits are carried over by the porting process in `native-porting.md`.

Goal: native Go everywhere, a small node agent instead of the full panel on
nodes, a modular master, and eventually no Node.js in the toolchain — first
shrink Node's role, then drop it.

## Measured starting point (2026-09-27)

- Panel on a node: 104–147 MB RSS, more than Xray itself (25–110 MB).
- Binary 73 MB, 93 dependencies (xray-core, gVisor, pgx, LDAP, bots, Swagger).
- Frontend: ~78k lines TS (≈90% upstream), 8.5 MB static; costs build time and
  toolchain, not server RAM.

## Target shape

| | `x-ui` master | `fbnode` agent |
|---|---|---|
| Does | full DB, UI, API, most modules | Xray + edge 443 + reports (+ fail2ban) |
| Storage | SQLite/Postgres, replicated between masters | last applied state + bounded journal on disk |
| Listens | panel/API, subscriptions | only the public edge; no management port |
| Build | CGO (SQLite), build tags cut unused modules | pure Go, static, no web/gin/gorm/sqlite |
| RSS | shrinking | target 20–40 MB |

- The agent dials the master (list of masters, nearest first, failover); ed25519
  identity pinned at enrollment. Nothing to scan on a node.
- Master renders the node's full Xray config with its own generator and pushes
  `DesiredState{version, config, certs, edge, maintenance, bans}`; agent applies
  (client-only changes via Xray gRPC, otherwise restart) and keeps it on disk.
- Agent sends journaled `Report{seq, traffic, onlines, IPs, health}`; master acks.
- Master gets `runtime.Agent` next to `runtime.Remote`: nodes migrate one by one.

## Pros

- Much smaller attack surface on nodes: no web, no API port, few deps.
- 3–5× less RAM on nodes; runs on 512 MB VPS.
- One implementation of config logic (on the master) instead of per-node copies.
- Node keeps serving from its last state when every master is down.
- Modular master: a feature = one package (storage, API, jobs, pages).
- Go-only toolchain in the end: one language, simple static builds.

## Cons and risks

- Upstream merges get harder the further we diverge; UI is the worst part
  (≈90% upstream). Mitigation: rewrite our own sections first, upstream last.
- Two node kinds live side by side during migration (Remote and Agent).
- Loses 23k lines of frontend tests when a page moves; Go tests must replace them.
- Sidecars (TUIC, MTProto, AmneziaWG) need porting as agent modules.
- A Go server-rendered UI will look similar, not identical to AntD.
- Real effort: agent MVP ~1–2 weeks, production parity ~1 month, full UI
  6–10 weeks spread over time.

## Tracks and order

1. **Shrink Node.js (cheap, anytime).** Keep React but stop needing Node on
   build servers: ship prebuilt `dist` from CI or bundle with esbuild's Go API;
   drop Storybook from the default gate. Node stays only for frontend tests.
2. **Node agent.** MVP (enroll, session, apply state, Xray, traffic, edge,
   drain, certs) → try on nl2, measure → parity (journal, fail2ban/IP limit,
   sidecars) → wizard installs `fbnode` → migrate nodes one by one.
3. **Modular master.** Module contract; build tags drop Telegram, Discord,
   LDAP, etc.; clean DB layer (no ad-hoc settings JSON, proper migrations).
4. **Go UI console** (templ + htmx, vendored uPlot/CodeMirror, no Node) with
   the same look and the same layout as today (sidebar, sections, tables and
   modals in the same places): own sections first (nodes, wizard, edge,
   certs/DNS, templates, maintenance), upstream sections last. When done,
   Node.js leaves the repo entirely.
5. **Multi-master** (fi1, ru2, de2): every master manages every node, also
   during a split. Moved to the foundation — see `native-multimaster.md`
   (JetStream mirrors between masters + Postgres projection with field-level LWW).

## Readiness (2026-11-02)

See `native-node-status.md`: the agent is pilot-ready for entry/exit nodes; the
master is not production-ready (no entrypoint, no master join, no DB
migration, unauthenticated admin API). That file also designs `relay` and
`lite` profiles for sub-nodes.

## Open questions

- ~~Store for journals/replication~~ — decided 2026-09-29: JetStream
  (operational data, mirrored) + Postgres on every master (`native-multimaster.md`).
- Keep following upstream at all after track 4, or freeze at a version?
- Postgres on masters or replicated SQLite?
