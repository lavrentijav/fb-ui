# Native interface: a module over the master API

Status: started 2026-11-02, branch `native`. The interface is an optional
layer: a master serves `/api/v1` (and the old `/panel/api` through `compat`)
without it, and the UI module can be cut with `-tags no_ui`.

## What is done

- `internal/native/uimod` — the interface's server side:
  - `POST /api/v1/ui/login {token}` verifies an API token, creates a session
    (`session` table, key = SHA-256 of the cookie value, holding the token's
    hash, name, scope and expiry) and sets an `HttpOnly`, `SameSite=Strict`
    cookie `fbui_session` (12 h). The page never keeps the token in JS.
  - `POST /api/v1/ui/logout` drops the session and clears the cookie.
  - `GET /api/v1/ui/me` answers who the session belongs to.
  - `module.SessionAuth`: the API server accepts the cookie as a credential,
    so `/api/v1/*` works from the browser; revoking the API token ends its
    sessions; expired sessions are refused and dropped.
  - `ScopePublic` routes need no token (login/logout), everything else keeps
    the scope check.
- `masterd.Config.UIDir` — a built interface (an `index.html` plus assets) is
  served at `/panel` with a client-route fallback to `index.html`, and
  `/panel/api/*` goes to the modules' `compat` adapter, so the current React
  app talks to a native master today.
- `Run` on the leader prunes expired sessions.

## Done in the page (2026-11-02)

- The sidebar is grouped into six sections (Обзор, Доступ, Флот, Вход (443),
  Маршрутизация, Система). `Clients` and `Edge` are single entries now:
  `/clients` has tabs `Clients / Groups` and `/edge` has `Edge / Hosts`, with
  `/groups` and `/hosts` redirecting into the matching tab. A page rendered
  inside another's tabs reads `useEmbedded()` and drops its own sidebar and
  padding.
- The old `/outbound` and `/routing` aliases are gone (they were the same
  `XrayPage`); both are now sections of `/xray` (`#routing`, `#outbound`), and
  old paths redirect there. `/network` moved to `/cascades` with a redirect.
- `Xray → DNS` is labelled "Core DNS" so it is not confused with certificate
  DNS providers.
- In native mode the master injects `window.X_UI_NATIVE`; the login page then
  asks for an API token and posts it to `/api/v1/ui/login`, which sets the
  session cookie (the old username/password flow stays for the main line).

## Subscription page (done 2026-11-02)

The subscription page is a separate build entry (`subpage.html`) served by the
master at `/sub/<token>` for browsers (raw links for clients), with the login
and the virtual network in a **Manage** tab backed by `/api/v1/sub/*`. It also
renders the response metadata (announcement with colour and button,
support/website links, update interval, HWID limit, refill date, mirror URLs,
a per-server traffic bar). See `native-nodes.md` for the API and
`native-sub-params.md` for every parameter sent.

## Subscriptions and virtual networks (done 2026-11-02)

Two operator pages under **Доступ**:

- **Subscriptions** (`/subscriptions`) lists every subscription with its
  login, enabled state, link count, traffic and expiry. It creates one per
  client email (`PUT /sub/<email>`), shows the generated password once, and
  can copy the link, reset the password, switch the link list off and delete.
  The list is `GET /sub`; the switch is `POST /sub/<token>/enable`.
- **Virtual networks** (`/vnet`) observes the networks subscribers create:
  title, owner and members with their internal addresses. It is read-only --
  subscribers manage their own networks from the subscription page -- and
  reads `GET /vnet/networks`.

Both used the master's loopback admin API, which the panel (served at
`/panel`) cannot reach; they now go through the **subs** module:
`GET /api/v1/subs`, `PUT /api/v1/subs/{email}`, `POST
/api/v1/subs/{email}/password`, `POST /api/v1/subs/{token}/enable`, `DELETE
/api/v1/subs/{token}` and `GET /api/v1/subs/networks`, with the old
`/panel/api/subscriptions/*` and `/panel/api/vnets/list` mapped onto them, so
the page's session cookie authenticates the calls. The module reads the same
op-log tables (`subtoken`, `nodespec`, `subhost`, `client`, `usage`, `vnet`,
`vnet_member`, `vuser`, `uname`) as the rest of the fleet; the admin
endpoints on the loopback API remain for scripts and the lab.

## System pages (done 2026-11-02)

Three pages under **Система**, all against APIs that already existed:

- **Notifications** (`/notifications`, hash tabs `#telegram`, `#email`,
  `#discord`) merges the three transports into one page with a shared Save.
  The old `/settings#telegram`-style links still render inside Settings.
- **Webhooks** (`/webhooks`) lists endpoints with their event filters, last
  delivery and status; it creates and edits them (name, address, events,
  signing secret), sends a test delivery and deletes. Uses
  `/panel/api/webhooks/*`.
- **Audit log** (`/audit`) shows every API change (time, actor, method, route,
  status, address) with an actor filter and pagination, newest first. Uses
  `/panel/api/audit/list`.

The sidebar's Settings submenu now holds General, Security and Subscription
only; notifications moved to their own entry.

## Access page (done 2026-11-02)

**Access** (`/access`) is the operator's view of the API's own credentials:

- **API tokens** (`GET /api/v1/access/tokens`) lists name, scope, creation and
  expiry, whether it is still live and how many browser sessions it holds. A
  new token (`POST /api/v1/access/tokens {name, scope, ttlHours}`) is answered
  once and only its hash is stored; deleting one (`DELETE
  /api/v1/access/tokens/{id}`) also ends its sessions, and the last live admin
  token cannot be deleted so nobody can lock the fleet out.
- **Sessions** (`GET /api/v1/access/sessions`) lists the browsers logged in
  through a token (name, scope, signed-in and expiry); `DELETE
  /api/v1/access/sessions/{id}` ends one and `POST
  /api/v1/access/tokens/{id}/signout` ends all of a token's.

`accessmod` is the module (`no_access` cuts it); tokens gained an `at` stamp
and sessions already carried name, scope, expiry and their token's hash.

## Old pages on a native master

The React app still calls `/panel/api/*` (the compat adapter). Modules map
every section the native fleet has: clients (create/delete/list, groups), the
subscription page and its management, hosts, templates, inbounds per node,
certs (list/rollback), routing (lists, rules, tester), settings, usage,
dashboard, webhooks, audit, access and the fleet pool.

Everything else the old app asks for — the panel-local Xray view
(`/panel/api/xray/*`), server metrics and actions (`/panel/api/server/*`), the
old node/peer/cluster model, the old cascade graph, DNS accounts, subscription
balancers, and firmware-like provisioning helpers — has no native equivalent:
a native master runs no core of its own and keeps no panel-local database. The
compat adapter answers those with the old envelope, `success: false` and
"this section is not available on a native master", so a screen shows a
message instead of an empty error.

Porting those screens means moving them to `/api/v1` against the modules above
(or removing them, for features the native model does not have), one section
at a time; that is the remaining work in this file.

## Still to do

1. ~~Point the React app's session handling at `/api/v1/ui/login`~~ — done for
   the login page; the old `/login` stays for the main-line panel.
2. ~~Tabs (Clients+Groups, Edge+Hosts, notifications)~~ — done. Cover the old
   `/panel/api` surface the interface still calls. `LegacyRoute` gained a
   `Rewrite` hook for old routes that name their subject in the body (a delete
   by name), and `groups` now answers the whole `clients/groups/*` set,
   including the old summary shape (`clientCount/trafficUsed/up/down`) and
   rename/reset/bulk add/remove. Webhooks, audit and clients are done. Hosts
   is next but the old record (a group of addresses over `inboundIds`) does
   not map onto the native entry (one address per node/inbound) without an
   adapter; nodes, certs, DNS and routing have no native routes for most of
   their old calls yet.
3. Then move the interface to `/api/v1` per section, in the order below.
4. Build the UI in CI (`make build-fe`) and ship `dist/` beside the binary
   (`UIDir`), not inside it, so the master stays small and a UI change needs
   no new binary.

## The interface today (20 entries)

| Entry | What the page does |
|---|---|
| `/` Dashboard | Panel and Xray state, traffic totals, online counts, node health. |
| `/inbounds` | Inbounds: protocol/port/transport, their clients, enable/disable, clone, QR. |
| `/clients` | Every client across inbounds: traffic, expiry, IP limit, groups, links/QR, bulk operations. Tabs: Clients · Groups (attach/detach inbounds, bulk add/remove, group settings). |
| `/nodes` | Fleet: nodes and masters, add node, panel update, mTLS, maintenance/drain, metric history. |
| `/network` | Cascade topology graph (`CascadeLink`): source inbound → target inbound, outbound generation. |
| `/filters` | Lists (domain/IP/ASN, remote URL) and rules (block/direct/cascade per inbound), route tester. |

| `/certs` | TLS certificates: ACME issuance/renewal, DNS accounts, rollback, expiries. |
| `/edge` | The shared 443 edge: what is exposed, stub sites, port sharing. Tabs: Edge · Hosts (per-inbound addresses, SNI, path, shuffle, ECH, pinned cert). |
| `/templates` | Inbound templates: save a set of inbounds, stamp it onto other servers. |
| `/xray` | Core settings: basic template, balancers, DNS, advanced JSON. |
| `/outbound` | Alias of `/xray` (balancers/outbounds). |
| `/routing` | Alias of `/xray` (routing/DNS). |
| `/settings` | General, security (2FA, API tokens, LDAP), Telegram, email, Discord, subscription (formats, balancers). |
| `/api-docs` | OpenAPI reference and a request builder. |
| logout | Ends the session. |

## Proposed layout (7 sections)

| Section | Pages | What changes |
|---|---|---|
| **Обзор** | Dashboard | unchanged |
| **Доступ** | Inbounds · Clients · Groups · Subscriptions · Virtual networks | Clients gets tabs `Все клиенты / Группы` (groups only organise clients); a new **Subscriptions** page for link formats, sub domains, fallback headers; a new **Virtual networks** page for vnet users, invites, exposes (the data the `vnet` module now keeps) |
| **Флот** | Nodes · Cascades · Templates | `/network` renamed **Cascades** (it is the cascade graph, not the vnet); Nodes gets tabs `Список / История / Обслуживание`; Templates stays |
| **Вход (443)** | Edge · Hosts · TLS & DNS | **Hosts merges into Edge** as the `Имена и маршруты` tab (both describe the 443 surface); **Certs + DNS accounts** join one page with tabs |
| **Маршрутизация** | Filters · Outbounds & Balancers · Xray core | drop the `/outbound` and `/routing` aliases; split the xray page: rules/balancers to Routing, templates/DNS to **Xray core** |
| **Система** | Settings · Access · Notifications · Webhooks · Audit log · API docs | Settings keeps General + Security; **Notifications** merges Telegram/email/Discord into one page with provider tabs (done); **Access** holds API tokens and live sessions (both are security state); **Webhooks** and **Audit log** are pages (done); API docs lives here |
| logout | — | unchanged |

Why this shape: three questions an operator asks — *who has access*
(**Доступ**), *what runs where* (**Флот**), *what the internet reaches*
(**Вход**) — then *where traffic goes* (**Маршрутизация**) and *what the panel
thinks* (**Система**). Removed: two duplicate xray aliases, two separate
notification pages, a separate Hosts page that only edits the edge's table.
Added: webhooks, audit, notifications, subscriptions and virtual networks;
sessions/tokens (Access) still needs an API.

## Tests

`internal/native/uimod` covers login → cookie → `/ui/me`, that the answer
never echoes the token, logout, a revoked token ending its session, and
expiry. `internal/native/masterd` covers static serving, the client-route
fallback and a missing build.

## Settings and notifications (done 2026-11-02)

- `internal/native/settings` — typed key/value settings in the op log
  (`setting` table): a schema names each key's kind (string/bool/int/list/
  secret), default and optional check, so a bad value is refused on write.
  `Get/Set/SetMany/All`; a secret is never returned (only `hasSecret`) and an
  empty secret keeps the stored one, so a masked form saves safely.
  The fleet schema (`settings.Fleet`) covers the panel name and the three
  notification transports; it grows only when a module needs a key.
- `internal/native/notify` — transports with injectable HTTP and SMTP dialers:
  - Telegram: `sendMessage` to every configured chat;
  - Discord: one webhook, mentions suppressed;
  - Email: SMTP with implicit TLS or STARTTLS, AUTH PLAIN, header values
    sanitised so a field cannot inject a header line.
- `internal/native/notifymod` — module `notifymod`:
  - `GET /status` (each transport's last attempt), `POST /test/{transport}`;
  - on the **leader** it reads the event table past a daily cursor per
    transport, filters by that transport's `notify.<name>.events` list (empty
    = all), sends, and advances the cursor only after success — a failed
    transport retries the same event next pass instead of skipping it. A
    transport enabled for the first time starts at the newest event, so it
    never floods the operator with old history.
- `internal/native/settingsmod` — `GET/PUT /api/v1/settings` plus `Legacy()`
  mappings for the old panel routes.
- Tags `no_settings`, `no_notify` cut the modules.

The lab runs the real thing end to end in unit tests: the event path on a
`modtest` host (leader-only, no replay, filter, retry, no double send) and the
transports against an `httptest` receiver and an in-process SMTP server.
