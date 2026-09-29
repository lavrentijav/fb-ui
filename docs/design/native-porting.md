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
| Node agent (`fbnode`) | not started |
| Master ↔ agent runtime | not started |
| Master modules / build tags | not started |
| Go UI console | not started |
| Masters replication | not started |

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
