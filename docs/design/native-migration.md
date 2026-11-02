# Migrating from the old panel

Status: 2026-11-02. The importer (`fbmaster import`) reads the old panel's
SQLite and plans the native records; it applies inbounds, clients and groups.
This page is the runbook and the honest list of what does not move.

## What moves

| Old entity | Native record | Notes |
|---|---|---|
| VLESS inbound on ws/xhttp | `inbounds` (per node) | tag, port, network, path, host, route `direct` |
| Client attached via `client_inbounds` | one `client` record per node and inbound | uuid, enable, totalBytes, expiry, limitIp |
| Client groups | `groups` | name only; membership is per email |
| Enabled/disabled state | kept | a disabled inbound or client is reported, not silently enabled |

## What does not move (reported, never dropped silently)

- protocols other than VLESS (trojan, vmess, shadowsocks, hysteria, tuic,
  wireguard, mtproto, amneziawg) — the native node model runs VLESS over
  ws/xhttp for now;
- transports other than ws/xhttp (grpc, tcp, kcp, reality-shared shapes);
- clients with no `client_inbounds` row in a very old schema (they live inside
  each inbound's settings JSON) — the report counts them;
- settings, certificates, DNS accounts, filter rules, templates, host
  overrides, HWID registrations, panel users — each is re-created through its
  native module, with the reason shown in the report.

## Importer

```
# plan only (default): prints JSON, changes nothing
fbmaster import -db /etc/x-ui/x-ui.db -local-node fi1 [-node-map 2=ru4,3=de2] -json

# apply to a running master's authenticated admin surface
fbmaster import -db /etc/x-ui/x-ui.db -local-node fi1 \
  -apply -master http://127.0.0.1:8080 -token <admin token>
```

`-local-node` names the native node that takes the old panel's own inbounds
(rows with no node id). Old node ids map to the names in the `nodes` table, or
to `-node-map` when renamed.

The plan is pure and unit-tested on a fixture database; readers tolerate a
missing table (an older panel simply has no node or group support).

## Cutover runbook

1. **Snapshot.** Copy `x-ui.db` aside; keep the old panel running.
2. **Plan.** Run `fbmaster import -db ... -json` on a master host. Read the
   skipped list; every skip is either a feature you will add later (a sidecar)
   or something to fix in the old panel first (a bad tag).
3. **Start the fleet.** First master with `-create-ca`; further masters with
   `fbmaster join`; nodes enroll with `fbnode enroll` (one token each).
4. **Apply.** Run the importer with `-apply` against one master. Existing
   records are kept (409 is reported as "already exists"), so a second run is
   safe.
5. **Verify.** On each node: `xray.json` contains the inbound and its clients;
   a link from the subscription connects; traffic appears in the master's
   dashboard; quotas cut at the limit (the node enforces locally).
6. **Switch subscriptions.** Point the subscription URLs at the new fleet
   (the old panel and the native master both answer `/sub/<token>`; the tokens
   are re-issued with `PUT /sub/<email>`).
7. **Retire.** Stop the old panel only after a full billing cycle of traffic
   has been counted on the native side.

**Rollback** at any point before step 7: the old database is untouched; point
a node's config back at the old panel and restart it. Nothing in the importer
writes to the old database (it opens read-only).

## Known gaps before this can replace the old panel for everyone

- Reality/XTLS/gRPC and the non-VLESS protocols need the sidecar/transport
  work (phase 3.4).
- Host overrides and templates are re-created by hand or through their module.
- Certificates are issued by the native masters' ACME, not imported.
- Panel users are replaced by API tokens and sessions; there is no password
  migration by design (the old hashes are not reused).
