# Porting the main line into `native`

The fleet runs the main line while `native` is rewritten. Every commit that
lands on the main line must be looked at once and either carried into
`native` or recorded as not needed, so nothing is lost when `native` takes
over.

## Marker

`.native-sync` (repo root, only on `native`) holds the SHA of the last
main-line commit that has been reviewed. The main line is
`origin/sync/upstream-3.8.5` until it is merged into `origin/main`; then
`origin/main`.

## Process (one run, e.g. a dispatched Claude on each push)

1. `git fetch origin` and list `$(cat .native-sync)..origin/<main line>`,
   oldest first, `--no-merges` plus each upstream merge as one unit.
2. For each commit decide:
   - **port** — the behaviour exists in an area `native` has already
     rewritten (see the status table below): reimplement it the native way,
     with tests, in one commit `port(<area>): <subject> (main <short sha>)`;
   - **carry** — the area is not rewritten yet: `native` still has the old
     code, so merge/cherry-pick it as is;
   - **skip** — irrelevant to `native` (e.g. React-only change to a page
     already replaced, CI for removed tooling): record why.
3. Append one row per commit to the ledger below, set `.native-sync` to the
   last reviewed SHA, commit `chore(native): sync to main <short sha>`.
4. Close the run as resolved only when every listed commit has a ledger row
   and `make test-go` passes on `native`.

## Rewrite status

| Area | State on `native` |
|---|---|
| Node agent (`fbnode`) | in progress: `cmd/fbnode` runs (journal, nearest-master pick, desired state, Xray control, edge 443, certs, drain, presence, live network routing, IP limits/fail2ban); missing: sidecars, master↔agent runtime |
| Virtual networks (phase 1) | in progress: users, addresses, names, per-network DNS resolver (`internal/native/vdns`), networks, invites, presence, in-network routing, client config; lab `TestMembersOnDifferentNodes` passes; missing: exposes by name/allocated port |
| Master ↔ agent runtime | not started |
| Master modules / build tags | in progress: module registry (`internal/native/module`), `/api/v1` server and API tokens, `mhost` (leader lease), modules `audit`, `webhooks`, `certs` (status, rollback), `routing` (lists, rules, ASN, tester; rules compiled into `nodespec.Spec.Rules`), adapter `compat` for the old `/panel/api`; tags `no_audit`, `no_webhook`, `no_routing`, `no_certs`, `no_compat`. Missing: the rest of the modules, session login |
| Go UI console | not started |
| Multi-master core (`internal/native/{hlc,bus,pgstate,lww,master,oplog}`) | in progress: JetStream supercluster + Postgres projection, split/heal tested |

## Ledger

| main sha | subject | decision | native commit / reason |
|---|---|---|---|
| 3d3f0e940 | fix(xray): restart a core that runs but has lost an inbound's port | carry | merge 92f0cb5a7 (area not rewritten) |
| d854d0d63 | fix(sub): treat encryption=none as the same VLESS link when deduping | carry | merge 92f0cb5a7 (area not rewritten) |
| 00cd0ad17 | fix(xray): check stored inbound ports, not only the process config | carry | merge 92f0cb5a7 (area not rewritten) |
| e143f2bed | fix(xray): drop client encryption from VLESS inbounds sent to the core | carry | merge 92f0cb5a7 (area not rewritten) |
| 2fe9c4421 | fix(dns): accept a Cloudflare Global API Key as email:key | carry | merge 92f0cb5a7 (area not rewritten) |
| 87c197607 | feat(dns): subdomains below a provider's zones | carry | merge 92f0cb5a7 (area not rewritten) |
| dac09166c | feat(provision): name a new node in several domains | carry | merge 92f0cb5a7 (area not rewritten) |
| d315e5b6c | feat(domains): plan and apply a move to (or copy into) a new base domain | carry | merge 92f0cb5a7 (area not rewritten) |
| dec3e7684 | fix(cascades): dial a cascade target the way its host group advertises it | carry | merge 92f0cb5a7 (area not rewritten) |
| 214973d8b | feat(nodes): release a node to another master without touching it | carry | merge 92f0cb5a7 (area not rewritten) |
| 223a60f82 | fix(nodes): never alias a stray inbound row onto one another row owns | carry | merge 92f0cb5a7 (area not rewritten) |
| 23a83e29c | fix(nodes): adopt a client another node serves when its credential matches | carry | merge 92f0cb5a7 (area not rewritten) |
| ccdedc834 | fix(routing): key a node's managed rules by the node's own inbound tags | carry | merge 92f0cb5a7 (area not rewritten) |
| d98841c95 | fix(nodes): a credential never stored for an email cannot clash | carry | merge 92f0cb5a7 (area not rewritten) |
| 2040ac3ec | fix(dns): list only active Cloudflare zones | carry | merge 92f0cb5a7 (area not rewritten) |
| 95ab039d7 | fix(templates): send template requests as JSON | carry | merge 92f0cb5a7 (area not rewritten) |
| 5bb19108e | fix(domains): rename the host of a URL during migration | carry | merge 92f0cb5a7 (area not rewritten) |
| 08cb40991 | feat(webhooks,audit): signed event webhooks and an operator audit log | port | `feat(native): audit module`, `webhook module`, leader-only delivery (a0c64e6cb, 35c1dfaed, 760084f49); spec `~/doc/fbui-native-modules-design.md` |
| ed5b62a3e | feat(filters): ASN lists resolved to announced prefixes via RIPEstat | port | `internal/native/routing` (ResolveASN, Compile) and `routingmod`; rules reach the node as `nodespec.Spec.Rules` |
| 678eec493 | feat(filters): route tester that shows which rule decides | port | `routing.Test` and `POST /api/v1/routing/test` |
| 34f1172ed | feat(filters): move a rule earlier or later within its panel | skip | React-only; the page is replaced when the native UI exists |
| 3d90c1325 | fix(routing): restart this panel's core when its own filter rules change | skip | the panel's own-core restart does not exist in `fbnode` (bundle apply restarts only on a changed hash) |
| e258c000e | fix(certs): order a challenge in the account the zone is delegated to; keep the previous pair and add rollback | port | `dnsacct` picks the delegated account; `certs.Rollback`, `prev*` fields on renewal, `POST /api/v1/certs/{id}/rollback` (`certsmod`) |
| f79f2f330 | fix(certs): decide DNS delegation from public resolvers, not the host's own | port | `dnsacct/delegation.go` (public resolvers) |
| a2882e9ce | fix(hosts): rebuild the cascades that dial an inbound when its hosts change | carry | cherry-pick `fd6d2b2bc` (area not rewritten) |
| 696b8f378 | docs: describe fb-ui in the README | carry | already in `native` as `7b008c97d` |

Note (2026-11-02): re-ledgered after the 2026-10-29 rebase. The range `5bb19108e..08cb40991`
was matched by subject: the rows above with new hashes cover the rebased work, `a2882e9ce` was
carried, `696b8f378` was already in `native`, and `.native-sync` is now `08cb40991`.
