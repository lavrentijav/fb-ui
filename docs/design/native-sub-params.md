# Subscription parameters: what we send, and why we send more than a client reads

Status: implemented 2026-11-02 (`internal/native/submeta`, `sub.Link`, `masterd.renderSub`).

## The rule

Send the superset. Every client surveyed (Happ, v2RayTun, v2rayN/NG, Hiddify,
Flamix) ignores headers and query parameters it does not know, so an unknown
field costs a few hundred bytes and lets a client grow into it without the
panel changing. The only things worth not sending are values we do not have,
and anything that would be wrong rather than merely unread (a per-server quota
we cannot compute, a signature we cannot produce).

Two carriers:

- **Response headers** — the standard way. Read by any client, invisible to a
  CDN that strips nothing.
- **`#key: value` body lines** (`?format=plain`) — the Happ/Flamix fallback for
  a proxy or CDN that rewrites headers, and for clients that parse only a body.
  The default body stays base64 so the older clients keep working.

`?format=plain` is new; `?format=page` (a browser) is unchanged.

## Response headers

`subscription-userinfo` is always sent; every other header is omitted when its
value is empty.

| Header | Meaning | Source in the native master | Notes |
|---|---|---|---|
| `Subscription-Userinfo` | `upload=; download=; total=; expire=` | `usage` (up/down total) + `client` totals/expiry | `total=0` unlimited, `expire=0` never |
| `Profile-Title` | profile name, `base64:` | `sub.title`, else `panel.name · login`, else login | `Content-Disposition` carries the same name |
| `Profile-Update-Interval` | hours | `sub.updateHours` (default 12) | `0` omits it |
| `Support-Url` | support button | `sub.supportURL` | |
| `Profile-Web-Page-Url` | "open site" | the subscription's own page | |
| `Announce` | banner text, `base64:` | `sub.announce` | |
| `Announce-Url` | v2RayTun: banner tap target | `sub.announceURL` | |
| `Sub-Info-Text` / `Sub-Info-Color` | Happ banner | `sub.announce`, `sub.announceColor` | `warning`→`red`, `success`→`green` |
| `Sub-Info-Button-Text` / `-Link` | Happ banner button | `sub.announceButton*` | |
| `Subscription-Refill-Date` | Remnawave: next traffic reset | `sub.refillDate` | unix seconds parsed from `YYYY-MM-DD` |
| `ProviderID` | Happ: provider identity | `sub.providerID` | also a link parameter |
| `Profile-Backup-Urls` | Flamix: mirrors to retry | `sub.backupURLs` | comma separated |
| `Fallback-Url` | Happ's name for the same | first of the above | |
| `X-Subscription-Fallback-IPs` | sibling masters' addresses | every other master's `masters.ips` | client keeps SNI and retries the IP — this is where the multi-address pool pays off |
| `X-Hwid-Limit` | Remnawave: device cap | strictest `limitIp` of the email's records | read from the client table and the specs |
| `X-Subscription-Fallback-Domains` | sibling panel names | reserved, empty today (one shared pool name) | future per-master names |
| `X-Subscription-Signature` | ed25519 over the body | reserved, not signed yet | see "not sent" |
| `Server-Userinfo` | Flamix: per-server quota, `base64:` JSON | `usage_node` (per node) | key = the link's remark |

## Share-link query parameters

A VLESS link for a native inbound (the edge terminates TLS on 443):

| Parameter | Value | Why |
|---|---|---|
| `encryption` | `none` | required by modern cores |
| `security` | `tls` (or `none` for a private advertised address) | |
| `type` | `ws` / `xhttp` | now taken from the inbound, not hard-coded to `ws` |
| `path`, `host` | the inbound's path / the advertised Host | |
| `sni`, `alpn` | the edge name / `http/1.1` | |
| `fp` | `chrome` | matches the node's uTLS profile; helps clients that pick a fingerprint |
| `headerType` | `none` | legacy but universally understood |
| `allowInsecure` | `0` | explicit; a client should verify the edge certificate |
| `userinfo` | the server's `subscription-userinfo` string | Flamix extension; other clients ignore it |
| `providerid` | `sub.providerID` | Happ extension |
| `node` | the node's name | our own metadata: harmless, useful in logs and future clients |

Every extra is written first and the transport parameters overwrite them, so a
caller cannot break a link by passing `security` or `type`.

Not applicable to this model: `flow=xtls-rprx-vision` (no XTLS), Reality
(`pbk`/`sid`/`spx`; the native edge is a normal TLS terminator), `mux`,
`serviceName` (no gRPC), `mode` (xhttp defaults are fine).

## Flamix client: what it added

From `docs/subscription-headers.md` in the Flamix repository:

- **`profile-backup-urls`** (aliases `backup-urls`, `x-backup-urls`,
  `fallback-urls`) — mirrors tried after the primary host fails. We send the
  operator's `sub.backupURLs`; the sibling IPs ride the cluster header instead
  of becoming unusable `https://ip/sub/...` URLs (the certificate is for the
  pool name, so an IP URL would fail verification).
- **`server-userinfo`** — `base64:` JSON keyed by remark → quota, shown as a
  bar under each server. Native now keeps `usage_node` (per email and node)
  from the node reports, so these numbers are real, not a copy of the global
  quota. Each link also carries the same value as a `userinfo=` parameter.
- **`subscription-refill-date`** — Remnawave-compatible next reset date.

Flamix also accepts the Happ/v2RayTun keys we already send (`announce`,
`announce-url`, `sub-info-*`, `providerid`, `fallback-url`, HWID headers with
`x-hwid`, `x-device-os`, `x-ver-os`, `x-device-model`). It sends HWID request
headers itself; the panel does not need to answer them beyond `X-Hwid-Limit`.

## Template tokens

`sub.remarkTemplate` defaults to `{{NODE}}-{{INBOUND}}` (the node's tag is not
unique across nodes, so the node must be in the default name). Tokens:

`{{NODE}}`, `{{INBOUND}}`, `{{EMAIL}}`/`{{USERNAME}}`, `{{USER}}`,
`{{PROTOCOL}}`, `{{TRANSPORT}}`, `{{TRAFFIC_USED}}`, `{{TRAFFIC_TOTAL}}`,
`{{TRAFFIC_LEFT}}`, `{{DAYS_LEFT}}`, `{{EXPIRE_DATE}}`, `{{STATUS}}`,
`{{STATUS_EMOJI}}`.

A `|` segment whose tokens all resolve to nothing is dropped, and a hyphen
standing alone between two tokens is elided when empty, so
`{{INBOUND}}-{{EMAIL}}` leaves no leading dash. `sub.showIdentity` appends the
email when the template does not already carry it.

## Settings added (`sub.*`)

`title`, `updateHours`, `supportURL`, `webPageURL`, `announce`,
`announceURL`, `announceColor`, `announceButtonText`, `announceButtonLink`,
`providerID`, `refillDate`, `backupURLs`, `remarkTemplate`, `showIdentity`.
All live in the existing typed settings schema, so a bad URL or date is refused
at write time.

## Custom parameters (operator-defined)

A separate op-log table, `subparam`, keeps free-form key/value strings per
**subscription token**. They are deliberately not interpreted by the master;
the operator, the sales bot or the subscription page can use them for flags,
plan names, routing hints, experiments, etc.

API (admin/monitor token; the token must exist in `subauth`):

```
GET    /api/v1/subs/{token}/params          -> {token, params}
PUT    /api/v1/subs/{token}/params          replace the whole map
POST   /api/v1/subs/{token}/params          merge keys into the map
DELETE /api/v1/subs/{token}/params/{key}    remove one key
DELETE /api/v1/subs/{token}/params          remove the row
```

Body: `{"params": {"flag": "on", "plan": "pro"}}`. Keys are
`[A-Za-z0-9._-]{1,64}`, values are at most 4096 bytes, at most 64 keys per
subscription. The same map is included in `GET /api/v1/subs` (as `params`),
`GET /api/v1/sub/me` and `window.__SUB_PAGE_DATA__.params`, so a client page or
the bot can read it without a second call. Deleting the subscription
(`DELETE /api/v1/subs/{token}`) removes its params row as well.

## Subscription aliases

An alias is an extra public token for one subscription. It is not a second
subscription: `GET /sub/<alias>` renders the primary's email and links, the
management page keeps using the primary token, and deleting an alias never
touches the primary.

API (admin/monitor token; `{token}` is the primary):

```
GET    /api/v1/subs/{token}/aliases           -> {token, aliases}
POST   /api/v1/subs/{token}/aliases           {"alias": "old-sub-id"}
DELETE /api/v1/subs/{token}/aliases/{alias}   remove one alias
```

Aliases are listed as `aliases` in `GET /api/v1/subs` and in the master's
`/sub` view. The importer turns every old panel `subId` into an alias of the
new primary subscription, so `/walksub/<old-id>` and `/frvpn/<old-id>` keep
working after the cutover. The master serves those paths itself, and on every
`subName` host (`sub.firewalk.top`); a `subName` host never serves `/panel`.
Tokens are `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`; an alias cannot be a primary
token or belong to another email.

## Not sent, and why

- **Reality / XTLS / gRPC / Hysteria2 / TUIC / Shadowsocks** — the native node
  model is VLESS over ws/xhttp behind a TLS edge. A future transport adds its
  parameters in `sub.Link`.
- **`routing` / `routing-enable`** — the native rules are compiled into the
  node's Xray config, not shipped to clients; handing out routing JSON would
  duplicate the fleet's policy in every client.
- **`X-Subscription-Signature`** — the wire exists (`internal/cluster` in the
  main line does ed25519). Native needs a fleet key and a client that verifies
  it; the header is reserved.
- **`custom-tunnel-config`, `socks-auth-*`, `tun-type`, `per-app-*`,
  `hide-settings`, `update-always`, `network-filter`** — provider-wide client
  preferences, not subscription facts; belong to a client-settings page, not
  the subscription response.
- **Encrypted subscription links (`happ://crypt*`)** — proprietary keys; both
  the main line and Flamix refuse them.

## Tests

- `submeta`: the quota string, every header's form (base64 title/announce,
  fallback lists, per-server JSON), the plain body, and that a value cannot
  inject a body line.
- `sub`: transport and extra parameters, that an extra cannot override
  `security`, templated remarks with and without advertised endpoints, and the
  remark grammar (empty segments, unlimited marks, status, sizes).
- `subsmod`: params CRUD, key validation, a missing token, and that deleting a
  subscription removes its params row.
- Lab: `TestSubscriptionAdvertisesMetadata` (headers, `fp`/`headerType`/
  `allowInsecure`, remark, HWID limit, `?format=plain`) and the per-server
  header filling in after real traffic in `TestSubscriptionLinkConnects`.
