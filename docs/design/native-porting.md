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
