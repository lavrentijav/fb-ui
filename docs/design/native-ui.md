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
  Маршрутизация, Система); `Clients` and `Edge` became submenus holding their
  merged pages (`Groups`, `Hosts`).
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
and the virtual network in a **Manage** tab backed by `/api/v1/sub/*`. See
`native-nodes.md` for the API.

## Still to do

1. ~~Point the React app's session handling at `/api/v1/ui/login`~~ — done for
   the login page; the old `/login` stays for the main-line panel.
2. Turn the submenus into in-page tabs (Clients+Groups, Edge+Hosts) and merge
   Settings' notification tabs; then declare `Legacy()` routes for every
   module so `/panel/api` covers each section the interface shows (webhooks
   and audit are done; nodes, certs, DNS, routing, clients remain).
3. Then move the interface to `/api/v1` per section, in the order below.
4. Build the UI in CI (`make build-fe`) and ship `dist/` beside the binary
   (`UIDir`), not inside it, so the master stays small and a UI change needs
   no new binary.

## The interface today (17 entries)

| Entry | What the page does |
|---|---|
| `/` Dashboard | Panel and Xray state, traffic totals, online counts, node health. |
| `/inbounds` | Inbounds: protocol/port/transport, their clients, enable/disable, clone, QR. |
| `/clients` | Every client across inbounds: traffic, expiry, IP limit, groups, links/QR, bulk operations. |
| `/groups` | Client groups: attach/detach inbounds, bulk add/remove, group settings. |
| `/nodes` | Fleet: nodes and masters, add node, panel update, mTLS, maintenance/drain, metric history. |
| `/network` | Cascade topology graph (`CascadeLink`): source inbound → target inbound, outbound generation. |
| `/filters` | Lists (domain/IP/ASN, remote URL) and rules (block/direct/cascade per inbound), route tester. |
| `/hosts` | Host overrides per inbound: addresses, SNI, path, shuffle, ECH, pinned cert. |
| `/certs` | TLS certificates: ACME issuance/renewal, DNS accounts, rollback, expiries. |
| `/edge` | The shared 443 edge: what is exposed, stub sites, port sharing. |
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
| **Система** | Settings · Access · Notifications · Webhooks · Audit log · API docs | Settings keeps General + Security; **Notifications** merges Telegram/email/Discord into one page with provider tabs; **Access** holds API tokens and live sessions (both are security state); **Webhooks** and **Audit log** become pages (their API already exists); API docs moves here |
| logout | — | unchanged |

Why this shape: three questions an operator asks — *who has access*
(**Доступ**), *what runs where* (**Флот**), *what the internet reaches*
(**Вход**) — then *where traffic goes* (**Маршрутизация**) and *what the panel
thinks* (**Система**). Removed: two duplicate xray aliases, two separate
notification pages, a separate Hosts page that only edits the edge's table.
Added: the four pages whose API already exists but has no screen (webhooks,
audit, sessions/tokens, virtual networks).

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
