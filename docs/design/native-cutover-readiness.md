# Native cutover readiness

Status: 2026-11-13, branch `work/native-ready`. This is the rehearsal record and
the open decisions before pointing the fleet at the native master. It uses a
**copy** of the fi2 database from the 2026-11-11 backup; no production database
was opened for writing.

## Current state (verified live 2026-11-14, side-by-side)

The "Cutover 2026-11-14" section below records the first cutover, which was
rolled back after the inbound-name loss. The fleet is now running **side-by-side
on alternate ports**; the final switch to `:443` is still pending the owner's go.

| host | native edge | old panel |
|---|---|---|
| ru2 (master, issuer) | `*:1443` | x-ui `*:443` |
| fi2 (master, non-issuer) | `*:1443` | nginx `:443` |
| fi1 / de2 / de3 / nl3 / dns1(`ru1`) / dns2(`ru`) | `*:444` | x-ui `*:443` |
| nl2 | — offline (SSH down) | unreachable |

Verified read-only on 2026-11-14: `GET /nodes/list` reports 6 data nodes online
(`fi1 de2 de3 nl3 ru ru1`), `nl2` offline, `ru2` a retired local master spec.
`/status` reports 182 clients; subscriptions and their aliases are intact and
serve 8–14 links each. All nodes run `v3.8.5-177-gf6250a6c-dirty`.

## fi2 rehearsal

Source: `x-ui-consistent-20261111.db` (the fi2 backup), decompressed to a
private temporary directory and opened read-only. Shape: `mixed` — the clients
table and `inbounds.settings` both carry clients.

| Fact | Value |
|---|---:|
| inbounds | 5 |
| VLESS ws inbounds | 4 (one disabled) |
| trojan ws inbounds | 1 |
| clients (unique emails) | 74 (50 enabled) |
| client_inbounds links | 207 |
| groups | 3 (`customer`, `family`, one derived from `clients.group_name`) |
| clients with no link | 6 |

Importer plan and apply against a temporary loopback master:

```
fbmaster import -db <copy>/x-ui.db -local-node fi2 \
  -share-host boykisser.online -apply -verify \
  -master http://127.0.0.1:<admin> -token <bootstrap>
```

Result:

- created 3 inbounds, 133 client records, 3 groups;
- `-verify`: inbounds 3/3, clients 133/133, groups 3/3, missing 0, mismatched 0;
- a second rehearsal created native subscriptions for every email with active
  client records: **44 emails, 86 expected links, 86 returned links, 0
  zero-link or mismatched subscriptions**;
- 68 unique emails exist in total; the rest are disabled/expired or have no
  link, and were not given a subscription (expected: old `subId` is not
  imported; native tokens are created with `PUT /sub/<email>`);
- skipped: the trojan inbound (63 clients stay in the old panel) and the
  disabled inbound; 6 clients have no link; 165 settings rows and panel users
  are not imported.

All client emails, UUIDs and subscription ids were validated before apply:
74/74 UUIDs, no invalid email, no duplicate VLESS ports. The rehearsal output
kept identifiers out of the report.

## Staging import with aliases

A staging master on ru2 (`/opt/fbui-staging`, loopback ports, no production
traffic) imported the cleaned central database after ru3-6 and Poland were
removed:

| Fact | Value |
|---|---:|
| VLESS ws/xhttp inbounds | 21 |
| client records | 785 |
| groups | 2 |
| primary subscriptions | 70 |
| aliases (old panel `subId`) | 70 |
| verify problems | 0 |

The import skipped 12 disabled inbounds and one wireguard inbound; 163 settings
rows and panel users are not imported. An active sample email served 17 links
through its old `subId` alias and the same 17 through the primary token. 25
subscriptions have no links because all their clients are disabled or expired.

Old URL compatibility:

- `sub.walk.fire-core.ru:84/frvpn/<id>` (fi2 `rev_v.conf`) currently rewrites to
  `/walksub/` and proxies to the old panel; cutover changes the rewrite to
  `/sub/<id>` and the upstream to the native master.
- `ru.firewalk.top/walksub/<id>` points at dns1/dns2 (currently 404); it needs
  the same proxy there, or a DNS move to the masters.
- `ru.walk.boykisser.online` / `ru2.walk.boykisser.online` terminate TLS on
  ru2:443 in x-ui today; the native public handler behind nginx must take that
  port over at cutover.
- `boykisser.online` root is a static site; add `/walksub` and `/frvpn`
  locations only if old clients use it.

The master now serves `/walksub/<token>` and `/frvpn/<token>` itself, so an
old host only needs its name in `subNames` (certificate and edge route) and a
DNS record pointing at the pool; the nginx rewrite becomes optional.

The staging master keeps real client data in `/opt/fbui-staging`; it is not
running on the public ports and the old panel is still serving traffic.

## Multi-master rehearsal (ru2 + fi2)

Two staging masters now share the same op log: ru2 is the issuer and future
DNS writer, fi2 joined with the fleet CA certificate only (non-issuer). Both
run the same binary (`v3.8.5-177-gf6250a6c-dirty`) with loopback edge/API/admin
and gateway `:17222`.

| Fact | Value |
|---|---|
| ru2 certificate | `CN=ru2`, SAN `ru2`, `master.firewalk.top`, `ru2.firewalk.top`, `144.31.61.39` |
| fi2 certificate | `CN=fi2`, SAN `fi2`, `fi2.firewalk.top`, `64.188.125.37` |
| gateway mTLS | `nats://144.31.61.39:17222` ↔ `nats://64.188.125.37:17222`, both reachable |
| state | 70 subscriptions visible on both masters |
| write path | a test subscription created on fi2 appeared on ru2 (71) and was deleted back to 70 on both |
| link counts | 45 of 70 subscriptions have links, max 21 per subscription (after the 2026-11-14 binary roll) |
| pool certificate | `pool:master.firewalk.top` is present on both masters |

Only the issuer master has `acme: true`; a joined master keeps `acme` false
and reports pool health without taking the DNS lease. The public edge, pool
certificate and `subNames` are not applied to the staging masters yet — their
edge is loopback, so no DNS record or traffic is touched.

**Resolved 2026-11-14:** the API token is in Vaultwarden
`dns/dns1-technitium-api` and the issuer master ordered the pool certificate
for `master.firewalk.top`, `sub.firewalk.top`, `ru.walk.boykisser.online` and
`ru2.walk.boykisser.online` (Let's Encrypt, valid until 2027-02-12). The first
two attempts failed while the zone's secondaries were behind: dns2, de2 and
ru2 receive a change about 12 s after the NOTIFY, while the master's
propagation check polls every 5 s, and Let's Encrypt then caches the failed
lookup for the SOA minimum (15 min). Retry after that window; do not restart
the master in a tight loop.

## Fleet DNS and node map (2026-11-14)

`firewalk.top` is the only working domain. Zone data read by AXFR from dns1:

| Name | Address | Role |
|---|---|---|
| `master.firewalk.top` | — (the pool writes it) | panel + API |
| `sub.firewalk.top` | — (the pool writes it) | subscriptions |
| `ru.firewalk.top` | 95.183.12.146, 5.42.126.75 | common RU entry (dns1/dns2) |
| `de2.firewalk.top` | 179.254.162.173 (de2 itself is .174) | Germany |
| `fi1.firewalk.top` | 179.198.50.34 | Finland |
| `ru2.firewalk.top` | 144.31.61.39 | RU master |
| `walk.firewalk.top` | 144.31.61.39 | sales bot callback |
| `pl1.firewalk.top` | 2.26.225.114 | dead Poland, remove at cutover |
| `ru3`–`ru6` | 185.73.126.108/.86, 132.243.195.137, 104.143.201.121 | removed from the native import, records to delete |

The old panel's node table (`/etc/x-ui/x-ui.db`, read read-only) says:
1 `de2`=179.254.162.174, 2 `nl2`=94.183.224.29, 8 `fi1`=179.198.50.34,
9 `fi2`=64.188.125.37 (disabled), 12 `de3`=144.31.136.15,
13 `ru8`=5.42.126.75, 14 `ru7`=95.183.12.146, 15 `nl3`=94.183.224.116. The
staging import maps 13→`ru`, 14→`ru1`, and the panel host to `ru2`, so the
imported `ru`/`ru1` nodes are the two common-RU edge hosts (dns1/dns2), not
the cascade hosts the target naming expects.

Target naming from the owner (2026-11-14): `ru` is the common RU entry, a
multicast of `ru1` (old ru7 = dns1, 95.183.12.146) and `ru2` (old ru8 = dns2,
5.42.126.75); `de` is a multicast of `de1` + `de2`, `fi` of `fi1` + `fi2`,
and so on. The staging import kept the internal names `ru` (old ru8) and
`ru1` (old ru7), but both advertise Host `ru.firewalk.top`, so the public
result matches; a fresh import should use `-node-map 14=ru1,13=ru2`. The
master host 144.31.61.39 is control plane only; its three local duplicate
inbounds were removed. The ssh aliases `ru1`/`ru2` (78.17.181.250 /
144.31.61.39) are host names, not the owner's public node names.

## Cutover 2026-11-14

- ru2: old `x-ui` stopped and disabled; `fbmaster`
  (`v3.8.5-177-gf6250a6c-dirty`, `4e9b984c…`) binds `:443` with the 5-SAN
  certificate and serves `/panel`, `/sub`, `/walksub`, `/frvpn`, `/healthz`;
  `walk.firewalk.top` reverses to the bot at `http://127.0.0.1:55004`.
- DNS: `master`/`sub`/`walk`/`ru.walk`/`ru2.walk` → 144.31.61.39; `de2`
  corrected to .174; the disabled `ru.walk` A record enabled; `fi2`, `de3`
  and `nl3` A records added.
- Final import from `/etc/x-ui/x-ui.db` after stopping x-ui: 785 clients,
  2 groups, 45 subscriptions, 70 aliases, zero missing; all inbounds now
  advertise their public FQDN (the `ru` pair advertises `ru.firewalk.top`).
- Bot: `XUI_BACKEND=native`, `NATIVE_API_URL=http://127.0.0.1:18081`, a
  dedicated admin token and `NATIVE_SUB_URL_BASE=https://sub.firewalk.top/sub`;
  Platega enabled; `firewalk-xray-tg.service` restores the Telegram SOCKS
  proxy on 127.0.0.1:10808 (the old x-ui held it before).
- Nodes (fbnode `57899bcc…`, edge `:443`, Xray on loopback): fi1, dns1
  (`ru1`), dns2 (`ru`), de3, nl3 and de2. de2 cannot reach ru2 (outbound
  blocked), so it uses fi2: the fi2 bus is exposed on
  `fi2.firewalk.top:9443` (`systemd-socket-proxyd` → 127.0.0.1:19443) and
  de2's config lists `fi2=tls://fi2.firewalk.top:9443`.
- End-to-end test key: 18 links; egress through fi1 64.188.93.248, de2
  179.254.162.173, nl3 94.183.224.116, ru 5.42.126.75; xhttp links present.
- `fbnode.service` needs `CAP_NET_ADMIN` in `CapabilityBoundingSet`: the
  standard Xray binary carries `cap_net_bind_service,cap_net_admin` file
  caps, and `fork/exec` otherwise fails with EPERM.
- Remaining: de1/nl2/fi2 as data nodes, the common `de`/`fi`/`nl` names,
  fi2's public `:443` is still nginx (replication works, public HA does not),
  old high-port links are retired (clients must refresh), and the empty `ru2`
  node is the master's retired local spec.

## Gaps found by the rehearsal

1. ~~Host.~~ Resolved at cutover: every imported inbound now advertises its
   public FQDN; the `-share-host` per-node map is implemented and tested
   (`ru=ru.firewalk.top,de2=de2.firewalk.top,...`).
2. **Remark.** The native inbound model has no remark. The compat view exposes
   `"<node>/<tag>"`, so an old `nfc_...` remark no longer starts with `nfc_`.
   The sales bot excludes `nfc_` inbounds by remark; the native adapter
   instead targets every non-prefixed inbound, so a reserved `nfc_` inbound
   must be named on the node spec if it is added later.
3. ~~Subscriptions.~~ Resolved at cutover: the final import created 45
   subscriptions and kept 70 old `subId` values as aliases; the bot uses
   `NATIVE_SUB_URL_BASE=https://sub.firewalk.top/sub`.
4. **Protocols.** Trojan (and any sidecar protocols) are reporting-only; the
   native node model is VLESS ws/xhttp. A cutover either drops those inbounds
   from the native fleet or keeps the old panel running for them.
5. **Link count.** `/api/v1/subs` read the stored spec only, so every staging
   subscription reported `links: 0` while `/sub/<token>` served 17 links (the
   clients live in `clientsmod`). Fixed: the list applies the same module
   contributions as the sub page (`TestSubscriptionListCountsRenderedLinks`).

## Sales bot (`firewalkbot-dev` on ru2)

The bot is a Python/aiogram service at `/opt/appuser/firewalkbot-dev`; it reads
`data/.env` first, then the repository `.env`.

**Decision: bot-side native adapter (option B).** `libs/x_native/api.py`
implements the `ThreeXUIAPI` surface against the master's `/api/v1` and the
fleet view. `bot/services.py` selects it when `XUI_BACKEND=native`; the old
`libs/x_ui` path is untouched. One bot key is one email + one UUID fanned out
to every non-`nfc_` inbound; expiry, device limit and delete touch every record
of that UUID. Native mints the subscription token, returned as `subId`.

Switch without code changes:

```dotenv
XUI_BACKEND=native
NATIVE_API_URL=http://127.0.0.1:<admin>
NATIVE_API_TOKEN=<admin token>
NATIVE_SUB_URL_BASE=https://<public>/sub
```

Mint a dedicated token via `POST /api/v1/access/tokens`
(`{"name":"firewalkbot","scope":"admin","ttlHours":0}`); the bootstrap
token printed once at master start is enough to create it. The admin surface
stays loopback; use an SSH tunnel or private network if the bot is elsewhere.

**Custom subscription parameters** are implemented in the native backend:
table `subparam` keyed by the subscription token, routes
`GET/PUT/POST/DELETE /api/v1/subs/{token}/params[...]`, and adapter methods
`get_subscription_params` / `set_subscription_params` /
`delete_subscription_param`. The subscription page and `GET /api/v1/sub/me`
carry the same map. This is the place for bot/operator metadata (for example a
Telegram id) without adding client columns.

**Aliases** are a native feature: one subscription can have extra public
tokens (`GET/POST/DELETE /api/v1/subs/{token}/aliases`). The importer keeps
every old panel `subId` as an alias, so old links on `fire-core.ru`,
`boykisser.online` and `ru.firewalk.top` resolve to the primary subscription
without reissuing the client. Deleting or disabling the primary still controls
the links; aliases have no login of their own.

Tests: `tests/test_native_api.py` (fake master) — 4 passed; an end-to-end smoke
against a real `fbmaster` created a spec, a client and a subscription, checked
the VLESS link and the params round-trip. Full bot suite: 43 passed, 2 failed
in the owner's uncommitted legal-docs work (`test_public_site_and_docs`,
`test_legal_docs_render_without_placeholders`), not in the adapter.

## Platega enablement checklist

1. Read `PLATEGA_FIREWALK` from Vaultwarden (folder `agents`; merchant id in the
   login, secret as the password) and run
   `scripts/apply_platega_vault.py --dry-run`, then without the flag. The script
   writes `PLATEGA_ENABLED=true`, `PLATEGA_MERCHANT_ID`, `PLATEGA_SECRET` into
   `data/.env` without printing values. **Blocker:** Vaultwarden currently
   answers `Invalid master password`; refresh `VAULT_EMAIL`/`VAULT_PASSWORD`
   (or add the item with `vault-add`) first.
2. Callback URL in the Platega cabinet:
   `https://walk.firewalk.top/payments/platega/callback`.
3. Restart `firewalkbot-dev` (with owner approval); in the admin "Платёжные
   шлюзы" the Platega health check should say the keys were accepted.
4. Make one test payment at the minimum amount; confirm the callback and the
   polling path both deliver exactly one key (idempotency
   `provider_tx_id=platega_<id>`).
5. Only then disable the YooMoney entry in `config.CURRENCIES`.

The token is not in the repository or the current env; `apply_platega_vault.py`
is ready but cannot read the vault until the login is fixed. `PLATEGA_ENABLED`
stays off in the meantime.

## Bot checks run

- `pytest tests/test_native_api.py`: 4 passed (fan-out, reserved `nfc_`
  exclusion, update/delete by UUID, params round-trip).
- Full bot suite (test Postgres on `127.0.0.1:15432`): 43 passed, 2 failed,
  2 deselected. Both failures are in the owner's uncommitted legal-docs work
  (`test_public_site_and_docs`, `test_legal_docs_render_without_placeholders`).
- End-to-end smoke against a real `fbmaster`: bootstrap token, spec create,
  client create, `/sub/<token>` plain and base64 delivery, params set/get —
  all passed.
- `scripts/apply_platega_vault.py` tested with a fake `vault-get`: both values
  found, nothing printed, re-run idempotent, `data/.env` mode 600.
- Platega live test still pending credentials; the library's header check is
  covered by its own unit test.

## Cutover order (bot-side adapter chosen)

1. Freeze panel writes; snapshot the old DB; keep the old panel running.
2. Dry-run the importer with the real public `-share-host`; review every skip.
3. Apply to a temporary master; `-verify` must report zero missing/mismatched.
4. Create native subscriptions for every migrated email; spot-check a link
   from the real client.
5. Point the bot at the native admin API (`XUI_BACKEND=native` + `NATIVE_*`),
   restart with owner approval and place one low-value order end to end.
6. Switch subscription URLs; watch traffic, quota and the bot's order log for
   one billing cycle before retiring the old panel.
7. Rollback: set `XUI_BACKEND=3xui` and point `XUI_SUB_URL_BASE`/the node
   configs back at the old panel. Keys created in native stay native; rollback
   only returns new sales to the old panel.

## Open decisions

1. ~~Public hostname~~ — resolved: common country names (`ru.firewalk.top`)
   for multicast entries, `<node>.firewalk.top` for individual nodes,
   `https://sub.firewalk.top/sub` for the bot.
2. Which inbounds/protocols stay on the old panel instead of migrating
   (trojan/sidecars are reporting-only). The old x-ui is now stopped on every
   migrated host; any trojan clients still need a native equivalent or a
   temporary old-panel host.
3. ~~Compat adapter (A) vs bot-side adapter (B)~~ — resolved: bot-side adapter
   is implemented, tested and running.
4. Platega: keys are in `data/.env`; the bot was restarted with
   `https://walk.firewalk.top/payments/platega/callback` live (a `{}` POST
   answers 400, not `disabled`). One real minimum-amount purchase still needs
   the owner.
5. ~~Node ↔ host mapping~~ — resolved: `ru` is the multicast of old ru7
   (dns1) and ru8 (dns2); `de`/`fi`/`nl` follow the same pattern. The staging
   internal names are `ru1`/`ru`; both advertise `ru.firewalk.top`. de2's A
   record was corrected to .174.
6. ~~Public listener~~ — done: ru2:443 is the native master; the old x-ui is
   stopped and disabled. fi2 remains a joined master on the internal bus
   (public :443 is nginx); its bus is exposed on :9443 for de2.
