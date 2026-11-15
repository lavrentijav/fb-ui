# Migrating from the old panel

Status: 2026-11-13. `fbmaster import` reads the old panel's SQLite read-only and
plans the native records; with `-apply` it sends them to a live master. The
importer now understands both the upstream shape (clients inside
`inbounds.settings`) and this fork's client records. This page is the runbook
and the honest list of what does not move.

## Schema detection

`-schema` prints what the importer found without planning anything:

| Shape | Where the clients live | What is read |
|---|---|---|
| `upstream-settings` | `inbounds.settings` JSON, key `clients` | one client and one inbound link per settings entry |
| `client-records` | `clients` + `client_inbounds` tables | one record per row, links from `client_inbounds` |
| `mixed` | both | the `clients` table wins for a duplicate email; settings fill the gaps |
| `empty` | nowhere | only inbounds, templates and reporting |

A missing `nodes`, `client_groups`, `client_inbounds` or `settings` table is a
shape, not an error: an older panel simply has no node or group support. A
database without `inbounds` is refused as "not a panel database".

The source is opened with `mode=ro` and `query_only`, so the file (and its
mtime/hash) never changes; a test reads a fixture twice and compares both.

## What moves

| Old entity | Native record | Notes |
|---|---|---|
| VLESS inbound on ws/xhttp | `inbounds` (per node) | tag, port, network, path, host, route `direct` |
| Client attached via `client_inbounds` | one `client` record per node and inbound | uuid, enable, totalBytes, expiry, limitIp |
| Client inside `inbounds.settings` | the same `client` record | the credential is the settings `id` for VLESS |
| Client groups | `groups` + membership | names from `client_groups` and `clients.group_name`; members are the clients in that group |
| Enabled/disabled state | kept | a disabled inbound or client is reported, not silently enabled |
| Old subscription id | imported as an alias | a new primary subscription is created per email and the old id becomes an alias, so `/sub/<old-id>` keeps working |

A client without a VLESS UUID (a trojan password, a wireguard key, …) is
reported and skipped on a VLESS inbound: its credential is protocol-specific
and cannot be reused.

## What does not move (reported, never dropped silently)

- protocols other than VLESS (trojan, vmess, shadowsocks, hysteria, tuic,
  wireguard, mtproto, amneziawg) — the native node model runs VLESS over
  ws/xhttp for now; the skip reason names how many clients stay behind;
- transports other than ws/xhttp (grpc, tcp, kcp, reality-shared shapes);
- clients with no link in either `client_inbounds` or a settings JSON — the
  report counts them and says they must be linked by hand;
- settings, certificates, DNS accounts, filter rules, templates, host
  overrides, HWID registrations, panel users — each is re-created through its
  native module, with the reason shown in the report.

## Flags

| Flag | Meaning |
|---|---|
| `-db PATH` | old `x-ui.db` (required; opened read-only) |
| `-local-node NAME` | native node that takes the old panel's own inbounds |
| `-node-map 2=ru4,3=de2` | rename old node ids; the default is the `nodes` table |
| `-share-host HOST[,node=HOST...]` | public host for an inbound whose settings have no `Host` header and whose share address is the node; a `node=HOST` entry names that node (or common country entry) explicitly |
| `-apply` | send the plan to `-master`/`-token`; without it the run is a dry run |
| `-verify` | compare the plan with the master (alone, or after `-apply`); non-zero on gaps |
| `-report json\|markdown` | report format (default `json`) |
| `-schema` | print the detected schema and exit |
| `-redact` | mask UUID and subscription id in the report (default true) |
| `-with-secrets` | print raw identifiers; overrides `-redact` |
| `-master URL`, `-token T` | master admin URL and admin API token (with `-apply`/`-verify`) |
| `-insecure` | skip TLS verification of the master (bootstrap only) |
| `-json` | legacy alias for `-report json` |

`-apply` is idempotent: a second run gets `409` for every existing inbound,
client and group, keeps the record, and still verifies. Before adding an
inbound the importer registers an **empty** spec for a node that has none
(`PUT /nodes/{node}/spec`); an existing spec is never replaced.

## Plan (dry run)

```
fbmaster import -db /etc/x-ui/x-ui.db -local-node fi1 \
  -node-map 2=ru4 -report markdown
```

The default report masks UUIDs and old subscription ids, so it can be pasted
into a ticket. Use `-with-secrets` only on a trusted terminal. The JSON report
is the same plan `-apply` would send, plus the detected `schema`, `skipped`
records and `notes` (settings, templates, hosts, certificates).

## Apply and verify

```
fbmaster import -db /etc/x-ui/x-ui.db -local-node fi1 \
  -apply -verify -master http://127.0.0.1:8080 -token "$ADMIN_TOKEN"
```

Verification compares:

- every planned inbound tag per node (`GET /inbounds/{node}`);
- every planned client key and its UUID/quota/expiry/limit (`GET /clients`);
- every planned group and its members (`GET /groups`);
- link count per email for subscriptions that exist (`GET /sub`), counting
  only enabled and unexpired clients; an email without an active link is a
  note, not a failure. Aliases are checked separately (`aliases` in the
  report).

A missing record or a different value is printed as a problem and exits
non-zero. The plan is written to stdout in the selected format.

## Cutover runbook

1. **Snapshot.** Copy `x-ui.db` aside; keep the old panel running. The importer
   never writes to it.
2. **Inspect.** Run `-schema`, then the dry run. Read the skipped list; every
   skip is either a feature you will add later (a sidecar) or something to fix
   in the old panel first (a bad tag).
3. **Start the fleet.** First master with `-create-ca`; further masters with
   `fbmaster join`; nodes enroll with `fbnode enroll` (one token each).
   Enrolling issues the identity; the importer registers the empty spec on the
   first apply.
4. **Apply.** Run the importer with `-apply -verify` against one master.
   Existing records are kept (409 is reported as "already exists").
5. **Verify by hand too.** On each node: `xray.json` contains the inbound and
   its clients; a link from the subscription connects; traffic appears in the
   master's dashboard; quotas cut at the limit (the node enforces locally).
6. **Publish the edge.** On the issuer master set `poolName:
   master.firewalk.top`, `subNames: [sub.firewalk.top, ...]`, `acme: true`,
   `acmeEmail`; a joined master uses the same `poolName`/`subNames` with
   `acme: false` and serves the replicated certificate without writing DNS.
   Add the DNS accounts (`PUT /dns/{id}`) for every zone involved; the
   credential must be a Technitium API token from Administration → Sessions,
   not the web login password. Start the masters; the issuer orders one SAN
   certificate, then the pool writes A records for the pool and sub names.
   Verify `https://master.firewalk.top/panel` and
   `https://sub.firewalk.top/sub/<token>`; a sub host never serves `/panel`.
7. **Switch subscriptions.** Point the old hostnames at the masters. Old
   `/walksub/<id>` and `/frvpn/<id>` URLs are served natively; the importer
   already kept every old `subId` as an alias. `ru.firewalk.top` is the
   exception: the common RU name lives on the node edges and proxies to the
   master, so it is not in the master's `subNames`. Update the bot
   (`XUI_BACKEND=native`, `NATIVE_API_URL`, `NATIVE_API_TOKEN`,
   `NATIVE_SUB_URL_BASE`) and restart it with approval; run one test purchase.
8. **Retire.** Stop the old panel, disable its unit, keep the database backup
   and the old nginx config for rollback. Do not keep a second panel serving
   traffic.

**Rollback** at any point: the old database is untouched; stop the native
master, start `x-ui`, restore DNS (and the old nginx rewrites), and point the
bot back to 3x-ui. The native import can be repeated from the snapshot.

**Done 2026-11-14:** steps 4–8 ran on ru2: the final import (785 clients,
2 groups, 45 subscriptions, 70 aliases), the 5-SAN certificate, the master on
`:443`, the bot on `XUI_BACKEND=native`, and six nodes (fi1, dns1/`ru1`,
dns2/`ru`, de3, nl3, de2). The old x-ui is stopped and disabled on every
migrated host. `ru.firewalk.top` is the dns1/dns2 multicast, not a master
subname. de2 uses fi2's bus because its outbound to ru2 is blocked. Details
and the egress test are in `native-cutover-readiness.md`.

## Known gaps before this can replace the old panel for everyone

- Reality/XTLS/gRPC and the non-VLESS protocols need the sidecar/transport
  work (phase 3.4).
- Host overrides and templates are re-created by hand or through their module.
- Certificates are issued by the native masters' ACME, not imported.
- Panel users are replaced by API tokens and sessions; there is no password
  migration by design (the old hashes are not reused).
- A mixed database with the same email but different UUIDs per inbound is
  merged per email; the native model keeps one credential per email. The
  report shows the chosen record.
