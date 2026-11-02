# Native nodes: what a master gives a node

Status: in progress 2026-09-29, branch `native`. Starts from the smallest
useful node and grows; each step is proven in the network lab first.

## The contract

A master renders a `nodespec.Spec` into a `nodespec.Bundle` and serves it
through `desired` (stamped, hashed). The node (`fbnode`) applies it with
`agent.BundleApplier`: Xray first, then the edge, so the edge never points at
an inbound that is not listening yet.

| Spec | Becomes on the node |
|---|---|
| `Inbounds` (VLESS, ws/xhttp, loopback) | Xray inbounds; with `Host`, an edge HTTP route `Host`+`Path` → the inbound and a handoff SNI route for `Host` |
| `Route` of an inbound | routing rule: `direct`, `block` or `proxy_<country>` |
| `Exits` | `proxy_<country>` outbound: VLESS over ws/xhttp + TLS to the exit node's cascade inbound |
| `Reverse` `http` | edge terminates TLS for the name and proxies Host+path to a local/remote URL |
| `Reverse` `tcp` | edge splices TLS untouched by SNI (mail, other TLS services), optional PROXY v1/v2 |
| `Certs` | certificates the edge serves, picked by SNI (exact or `*.zone`) |
| `CertIDs` | certificates the masters issue and renew (ACME); resolved into `Certs` at render, with the node's own `node:<name>` for names no group certificate covers. A name whose certificate is not issued yet is left out of the edge (refused like an unknown SNI), not the whole bundle |
| `DirectAllow` | private CIDRs `direct` may reach (Xray blocks private targets by default) |
| `Drain` | maintenance: the edge refuses new tunnels, open ones finish (idle ones closed after `Idle`); the node reports the tunnels still open, masters keep the newest state per node |
| `Client.LimitIP` | an IP limit per user, rendered into `Bundle.Limits` (the strictest, if the user is on several inbounds). The **node** enforces it with `internal/native/failban`: every `banEvery` (default 10 s) it reads `xray api statsonlineiplist -all`, keeps the user's newest `LimitIP` addresses and bans the rest as a `sourceIP → block` rule applied live (`adrules`); an address counts as live for `banHold` (default 2 min) after it was last seen, so a banned device does not come back as the newest one |
| always | `direct`, `block`, stats API on 127.0.0.1:10085, per-user traffic counters |

A node that moves no traffic still reports every `Heartbeat` (default 30 s),
so a master can tell a quiet node from a dead one; `GET /nodes/state` merges
the spec registry with the newest report per node: `online`, `lastSeen`,
`onlineUsers`, `draining`, `drainSince`, `drainActive`. `POST /nodes/{node}/drain {on, idle}` sets it, and the lab
toggles it through the API.

## Enrollment

A new node needs only a one-time token:

1. `POST /enroll/tokens {"node":"ru4","ttl":"1h"}` on a master's admin API
   records `enroll` (key = sha256 of a random secret, field `node`, `expires`)
   and answers `{token, caPin}`. The token is `enr1_<secret>_<sha256 of the CA>`
   — the fingerprint is the out-of-band channel an operator carries.
2. `fbnode enroll -master https://master.<zone> -token <token> -node ru4 \
   -masters ru2=tls://ru2.m.<zone>:443,fi1=... -dir /etc/fbnode` POSTs the
   token as a bearer; the master issues `node.<name>` (365 days) and answers
   the CA, certificate and key.
3. The node **verifies** the answer before writing anything: the served CA's
   sha256 must equal the token's fingerprint and the leaf must be
   `CN=node.<name>` signed by that CA. A man in the middle is refused.
4. The token works once: a successful redemption deletes the record, and a
   second attempt is refused by any master.

The redeem endpoint is mounted on the admin API and on `master.<zone>` (which
serves a publicly trusted certificate), so the key only travels over TLS in
production. The admin API itself is still unauthenticated — it must stay bound
to loopback until the session login exists.

## Subscriptions

`PUT /sub/<email>` on a master's admin API writes `subtoken` (key = token,
field `email`) and answers `{token}`; it reuses the email's token when the
projection already has one. `GET /sub/<token>` (admin API and the pool name)
answers the client's links, one per line, base64-encoded:

`vless://<uuid>@<host>:443?encryption=none&security=tls&sni=<host>&type=ws&host=<host>&path=<path>&alpn=http/1.1#<node>-<tag>`

A link exists for every inbound with `Host != ""` across every node's spec
whose client list holds the email (reverse `#rev` users are skipped). Unknown
tokens answer 404; a token is valid on every master once it replicates.

Inbounds behind the edge trust `X-Edge-Proxy` in `trustedXForwardedFor`, so
Xray sees the client's real address (IP limits, fail2ban), not 127.0.0.1.

## Network lab (`tools/netlab/run.sh`)

Unprivileged user+net+mount namespace, nothing changes on the host:
bridge `10.0.0.1/16`, one namespace per machine with its own address, every
node on :443, names from a lab `/etc/hosts`, certificates from a lab CA that
every process trusts through `SSL_CERT_FILE` (like a public CA).

| Host | Address | Names |
|---|---|---|
| ru4 (entry) | 10.0.2.4 | ru4.lab.firewalk.test, ru.lab.firewalk.test |
| de2 (exit) | 10.0.3.2 | de2.lab.firewalk.test |
| web (a site) | 10.0.8.1 | web.lab.test, mail.lab.firewalk.test |
| client | 10.0.9.1 | — |
| fi1m, ru2m, de2m (masters) | 10.0.1.1–3 | `<m>.m.lab.firewalk.test` |

`TestCascadeThroughEdges`: client → ru4:443 (edge, TLS, WS → Xray,
`proxy_de`) → de2:443 (edge, WS → Xray, `direct`) → site sees de2's
address; `direct` exits from ru4; `block` goes nowhere; HTTP and TCP reverse;
unknown SNI refused; traffic counted on both nodes.

`TestMastersDriveNodes`: three `masterd` processes (JetStream gateways on
:7222 between masters, their own edge on :443 splicing
`<m>.m.lab.firewalk.test` to the bus, which speaks TLS first so SNI routing
works), nodes run the real `fbnode` binary and reach masters only through
443. Specs go in through different masters (de2 via ru2, ru4 via fi1); ru4
bound to ru2 got "no spec" for ~2 s while fi1's write replicated, then the
bundle; nodes' reports reach the masters. Then fi1 and ru2 are killed and
ru4's route is changed on de2m alone (no quorum): ru4 fails over to de2m
and applies it.

Found by the lab, now handled:
- clients with a Chrome fingerprint (Xray default, v2rayNG) refuse Ed25519
  certificates — public-facing TLS must be ECDSA/RSA (as Let's Encrypt
  issues); Ed25519 stays for the fleet's internal mTLS only;
- Xray 26 `freedom` blackholes private destinations unless `finalRules`
  allow them (`DirectAllow`);
- without `trustedXForwardedFor` Xray logs every client as 127.0.0.1;
- the online-stats API reports `lastSeen` 0/stale on this core, so IP limits
  order addresses by when the node first saw them, not by the reported time.

## Next steps (in order)

1. ~~Masters in the lab~~ — done (`TestMastersDriveNodes`); enrollment is
   done too (see Enrollment above and next step 8).
2. ~~Group balancer~~ — done for exits (`TestGroupBalancerFailsOver`: de2
   stopped, traffic on de1 in ~2 s). Entry-side DNS pool still to do.
   Nodes of one group (e.g. exits `de1`, `de2`) behind
   one `proxy_de`: an `Exit` with several hops renders one outbound per hop
   plus an Xray balancer `proxy_de` (observatory probing through each hop;
   strategy leastPing, fallback to the next alive). Routing rules then use
   `balancerTag`. Entry side: a group's shared name (`ru.firewalk.top`) is a
   DNS pool of its members; the master drops a member in maintenance or
   down from the pool. Lab test: stop de2, traffic moves to de1.
3. ~~Drain/maintenance through the bundle~~ — done (`TestDrainKeepsOpenTunnels`:
   a download running when maintenance starts finishes, new tunnels are
   refused, lifting it lets them in). The agent no longer restarts Xray for
   a bundle whose Xray config did not change — that would cut every tunnel.
4. ~~Certificates from the masters' ACME~~ — done (`TestMastersIssueCerts`,
   Pebble + challtestsrv on the lab's `acme` host): a group wildcard asked
   for through ru2 is issued once (lease in the op log, fi1 won), every
   master holds it, ru4 serves it; a name added through de2 re-issues and
   reaches ru4 with its next bundle. The ACME account key is shared through
   the op log. DNS accounts live in the masters' state (`PUT /dns/<id>`,
   zones found from the credential, the longest zone over all accounts
   wins, rediscovered on a miss at most once a minute). A node's own
   certificate (`node:<name>`) is asked for automatically for the names it
   terminates that no group certificate covers, and re-planned on every
   renewal tick, so a spec saved before a group certificate replicated is
   corrected (the lab saves ru4's spec first to force this).
5. ~~Xray changes without cutting tunnels~~ — done (`TestXrayChangesKeepTunnels`:
   a user added, a route changed and an inbound added while a download runs;
   it finishes, Xray keeps its pid). The agent diffs the running config and
   the next one and applies it through `xray api` (users with `adu`/`rmu`,
   changed inbounds/outbounds re-added, the whole routing swapped at once
   with `adrules`, removals last). Anything else — log, api, dns, policy,
   observatory, the default (first) outbound — restarts Xray, as does any
   failed call.
6. ~~IP limits on the node~~ — done (`TestIPLimitBansTheOlderDevice`: two
   devices of one user, `limitIp: 1`; the one that connected first is blocked
   on the node within seconds, the newer one keeps working). The master only
   carries the limit in the spec; nothing is reported back.
7. ~~Subscriptions served by masters~~ — done for the link list
   (`TestSubscriptionLinkConnects`): `PUT /sub/<email>` mints (or reuses) a
   token in the op log, any master serves `GET /sub/<token>` (also on
   `master.<zone>`) as base64 VLESS links built from the specs — one per
   inbound with a `Host`, connecting to the edge on 443 with the inbound's
   path. Group names are a later step: a `Host` is already whatever name the
   spec puts there, so a group name needs only the DNS pool that resolves it.
8. ~~Node enrollment~~ — done (`TestNodeEnrollsItself`: a token minted through
   ru2 lets a node get its `node.ru4` certificate, apply its spec, serve
   traffic and report; the token is spent). `fbnode enroll` writes the identity
   and a config; the CA is pinned by the token's fingerprint.
9. Sidecars (TUIC, MTProto, AmneziaWG) and the master's view of node state
   (reports → API) are the rest of the fbnode track.

## Clients

A client is a record per node, inbound and email (`client` table, key
`<node>/<inbound>/<email>`): uuid, `enable`, `totalBytes` (0 = unlimited),
`expiresAt` (unix ms, 0 = never), `limitIp`, `remark`.

When a master renders a node it adds the node's **active** clients (enabled,
not expired) to the inbound with that tag; a client whose inbound no longer
exists is simply not attached, and the record outlives the port. A client a
virtual network already added to an inbound is not added twice. Traffic
exhaustion is not enforced yet: the node's counters would have to flow back
into a per-client usage table, which is the next step.

API: `GET|POST|PUT /api/v1/clients`, `GET|DELETE /api/v1/clients/{node}/
{inbound}/{email}` (`POST` creates or replaces, `PUT` refuses a duplicate,
201 on create), and the same operations on the master's admin API
(`/clients`). `expiry` accepts RFC3339 or unix ms. The subscription renders
from the effective specs, so a client reaches its links as soon as the record
replicates; disabling it removes it from both the node's config and the
subscription while the record stays.

`TestClientsReachNodesAndSubscriptions` creates a client through ru2, sees it
in ru4's Xray config and in the subscription from de2, then disables it and
sees it leave both.

## Traffic and allowances

`internal/native/usage` accumulates per-client bytes from the node reports;
`usagemod` runs on the leader:

1. For each master's report stream it reads past a cursor (`usage_cursor`,
   field `seq`), adds each report's deltas to the totals and writes the cursor
   back. Only the leader does this, and each report is counted once (its
   `(node, seq)` is the stream's position).
2. It snapshots changed totals into the `usage` table (field `bytes`), so a
   restart or another master reading the projection sees the same numbers.
3. It disables every enabled client whose total reached `totalBytes` and
   emits `client.exhausted` (`{email, node, inbound, bytes}`). The sweep only
   touches an enabled record, so it is idempotent.

API: `GET /api/v1/usage` (email → bytes), `GET /api/v1/usage/{email}`,
`POST /api/v1/usage/{email}/reset`; the admin API also exposes `GET /usage`
read from the projection. Tag `no_usage`.

`TestUsageDisablesAnExhaustedClient`: a client with a 1 KB allowance loads a
page through ru4, the master counts the bytes from ru4's report and disables
the client, which then leaves the node's config.

## Editing inbounds

`internal/native/inbounds` is the panel's main write path over a node's spec
(no separate store):

- `POST /api/v1/inbounds/{node}` adds an inbound (tag, port, network ws/xhttp,
  path, public host, route, vnet flag) — 409 on a repeated tag, and the port
  must be free;
- `PUT /api/v1/inbounds/{node}/{tag}` changes its shape (a tag rename is
  allowed), keeping the clients already on it;
- `DELETE /api/v1/inbounds/{node}/{tag}` refuses while a routing rule or an
  exit still names the tag, so a delete cannot leave a broken route;
- `GET /api/v1/inbounds/{node}` lists them.

Every write goes through `nodespec.Render` first, so a spec a node could not
run is never stored. Clients are **not** edited here — they come from the
`client` table (`inbounds` never accepts a client list), so an inbound edit
cannot silently drop people. The same operations exist on the master's admin
API (`/inbounds/...`), tag `no_inbounds`.

`TestInboundEditingReachesTheNode`: an inbound added through ru2 reaches ru4's
Xray config, a port change moves it, a rule blocks the delete with a clear
error, and once the rule is gone the delete removes it from the node.

## Vitals and the overview

A node's report carries a `Health` block (`l1` load, `mu`/`mt` memory in
bytes, `df` free disk, `up` uptime seconds, `v` agent version), read from
`/proc` on Linux; a platform without `/proc` leaves the fields zero rather
than failing. `/nodes/state` shows it per node.

`internal/native/dashboardmod` (module `dashboard`) answers the overview in
one call: `GET /api/v1/dashboard` with node counts (total/online/draining and
online users), client counts (total/enabled/disabled/expired), and the total
traffic counted. It reads node state through `module.ReportAware` and the
usage snapshots, so it works without Postgres. `GET /dashboard` on the
master's admin API answers the same, and `GET /api/v1/dashboard/online`
answers just the node numbers. Tag `no_dashboard`.

## Groups

A group is a **named set of client emails** (`group` table, key = name,
fields `remark`, `emails`). Membership lives on the group, not on each client
record: a client has a record per node and inbound, so the email is the stable
identity. That makes the useful bulk operations one write each:

- `POST /api/v1/groups {name, remark, emails, overwrite}` creates or replaces;
  `GET|DELETE /api/v1/groups(/{name})` lists (with a summary) or removes.
- `POST /api/v1/groups/{name}/members {emails, add}` adds or removes members.
- `POST /api/v1/groups/{name}/enable {enable}` turns every member's records on
  or off across all nodes and inbounds, answering how many changed (idempotent).
- `POST /api/v1/groups/{name}/attach {node, inbound}` creates the missing
  records for the members, reusing an email's existing uuid so a person keeps
  one identity; a member with no identity anywhere is refused with a clear
  error rather than given a random uuid.
- `POST /api/v1/groups/{name}/detach {node, inbound}` removes the members'
  records from that inbound only.

A summary counts records, enabled/disabled/expired and the traffic used from
the usage snapshots — per email, so the same person on two nodes is not
counted twice. The same operations exist on the master's admin API
(`/groups`, `/groups/{name}/{op}`), tag `no_groups`.

`TestGroupsBulkOperations` covers it end to end: two clients, a group, attach
to a second inbound (both records reach the node's config), disable the group
(both leave the config), detach (only the second inbound's records go).
