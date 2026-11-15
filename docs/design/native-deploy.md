# Deploying the native fleet

Status: 2026-11-13. This is the operator-facing install page; the roadmap is
`native-prod-plan.md`, the node's internals `native-node-status.md`.

## Artifacts

`make native-release` builds both static binaries, the interface, the systemd
units, `SHA256SUMS` and `build-info.md` into `dist/native/`:

```
fbmaster  fbnode  fbui-dist.tar.gz  SHA256SUMS  build-info.md
fbmaster.service  fbnode.service
```

`make native-binaries` builds only the two binaries and prints their sha256.
Both print their version (`fbmaster --version`, `fbnode --version`) and are
statically linked (`CGO_ENABLED=0`, `-trimpath`, `-s -w`). CI builds the same
for linux/amd64 and linux/arm64 plus the interface (`internal/web/dist`) and
attaches them to a `native-v*` tag as
`native-<version>-linux-<arch>.tar.gz`, which contains:

```
fbmaster  fbnode  SHA256SUMS
fbui-dist.tar.gz
packaging/fbmaster.service  packaging/fbnode.service
```

No Node.js is needed on a server: the interface is a built directory.

## Master

```
install -m 0755 fbmaster /usr/local/bin/fbmaster
install -d -m 0700 /etc/fbmaster /var/lib/fbmaster
tar -xzf fbui-dist.tar.gz -C /usr/share   # /usr/share/dist
cp packaging/fbmaster.service /etc/systemd/system/
systemctl daemon-reload
```

`/etc/fbmaster/config.json` (minimum):

```json
{
  "name": "fi1",
  "masters": ["fi1"],
  "storeDir": "/var/lib/fbmaster/js",
  "dataDir": "/var/lib/fbmaster",
  "publicName": "fi1.m.example",
  "edgeListen": ":443",
  "clientPort": 4222,
  "gatewayHost": "0.0.0.0",
  "gatewayPort": 7222,
  "advertise": "203.0.113.10:7222",
  "peers": {},
  "poolName": "master.example",
  "subNames": ["sub.example", "ru.example"],
  "acme": true,
  "acmeEmail": "ops@example",
  "publicIPs": ["203.0.113.10"],
  "apiListen": "127.0.0.1:8443",
  "adminListen": "127.0.0.1:8080",
  "uiDir": "/usr/share/dist",
  "caCert": "/etc/fbmaster/ca.pem",
  "caKey": "/etc/fbmaster/ca-key.pem",
  "cert": "/etc/fbmaster/master.pem",
  "key": "/etc/fbmaster/master-key.pem"
}
```

`publicName` is the name nodes dial on the edge (the bus); `poolName` is the
panel; `subNames` are subscription-only names. They must not collide. With
`acme: true` the master orders one certificate for the pool and every sub name
through DNS-01; add the DNS account first (`PUT /dns/<id>` on the admin
surface, for example a Technitium or Cloudflare account that holds the zone).
`acmeDirectory` points at a staging CA when needed; empty means Let's Encrypt.
The pool writes A records for `poolName` and each `subNames` entry, so
`sub.example` follows the same live masters as the panel. On a multi-master
fleet only one master needs `acme: true`: the issuer orders the certificate
and writes the pool DNS. A joined master may keep `acme` false, still set
`poolName`/`subNames`, serve the replicated certificate and report health,
but it never takes the DNS lease.

`poolName` serves the panel (`/panel`), the API and subscriptions. A `subName`
serves the subscription page, `/sub/<token>`, `/api/v1/sub/*` and the old
`/walksub/<token>` and `/frvpn/<token>` paths, but never `/panel`. Old panel
`subId` values are subscription aliases, so those URLs keep resolving after
the import.

DNS-01 propagation follows the zone's secondaries: a secondary that is a full
copy behind the primary usually picks the change up in about 12 s, and the
master waits for every NS before asking the CA. If validation still fails,
wait for the SOA minimum (15 min by default) before retrying — the CA caches
the failed lookup, so restarts in a tight loop only burn rate limit.

Then:

```
fbmaster -c /etc/fbmaster/config.json -check     # what would bind
fbmaster -c /etc/fbmaster/config.json -create-ca # first master only
systemctl enable --now fbmaster
```

A local end-to-end check that starts a temporary master on loopback, logs in
through `/panel`, fetches a VLESS ws+xhttp subscription and enrolls a relay
node (no Xray, no host changes) is `make native-smoke`.

The first start logs the admin token **once** (leader only, after ~10 s) and
the admin address. Keep the token: it is the way into `/api/v1` and the
`compatibility`/importer CLI. The admin surface is loopback by default; exposing
it requires `adminAllowPublic: true` and should sit behind a proxy with TLS.

A second master joins with a token minted on the first:

```
curl -s -H "Authorization: Bearer $ADMIN" -d '{"name":"ru2"}' \
  http://127.0.0.1:8080/master/tokens
fbmaster join -master https://master.example -token mst1_... -name ru2 -dir /etc/fbmaster
```

Then write its config (`masters` includes every master, `peers` maps the others
to `nats://<advertise>`) and start it. It reuses the CA from `/etc/fbmaster`.
Give each master a distinct `publicName`/`advertise`; open the gateway port
between them and include the advertised IP/name in `hosts`, so the mTLS
gateway certificate matches. A joined master loads `ca.pem` without
`ca-key.pem` and starts as a non-issuer.

## Node

```
install -m 0755 fbnode /usr/local/bin/fbnode
cp packaging/fbnode.service /etc/systemd/system/
systemctl daemon-reload
```

The node must already have Xray (`/usr/local/x-ui/bin/xray-linux-amd64` by
default) in the `lite` profile. `fbnode --version` prints the build; a relay
hop needs no Xray. The shipped unit keeps `CAP_NET_ADMIN` in
`CapabilityBoundingSet`: the standard Xray binary carries
`cap_net_bind_service,cap_net_admin` file caps, and without it `fork/exec`
fails with `operation not permitted`.

```
fbnode enroll -master https://master.example -token enr1_... \
  -node ru4 -masters fi1=tls://fi1.m.example:443,ru2=tls://ru2.m.example:443 \
  [-profile relay] -dir /etc/fbnode
systemctl enable --now fbnode
```

A node that cannot reach the issuer can use a joined master's bus. Expose it
without moving the master's `:443`, for example on fi2:

```ini
# /etc/systemd/system/fbnode-bus.socket
[Socket]
ListenStream=0.0.0.0:9443
```
```ini
# /etc/systemd/system/fbnode-bus.service
[Service]
ExecStart=/lib/systemd/systemd-socket-proxyd 127.0.0.1:19443
```

Then enroll the node with `-masters fi2=tls://fi2.<zone>:9443`. The proxy
forwards raw TCP to the master's loopback edge, so mTLS and the node identity
are unchanged.

`relay` is the only reduced profile: edge-only forwarding, no Xray, no store.

## Upgrade

```
fbnode upgrade -url https://.../fbnode -sha256 <hex> -c /etc/fbnode/config.json
```

It swaps the binary and exits; systemd starts the new build. Three failed
starts roll back to the previous binary automatically. For the master, stop the
service, replace the binary, start it: the op log replays and the fleet
continues (two masters should not be down at once during a quorum-sensitive
change).

## Health

- `GET /healthz` (admin, credential-free) → `ok <name>` once ready.
- `GET /healthz` on a `subName` → the same, so a subscription host is probeable.
- `GET /ready` → 200 when the master is ready.
- `GET /nodes/state` (admin, token) → every node's online state, last report
  and drain.
- `GET /net/ips` → the announced, detected and failing pool addresses.

## Backup

The master's truth is the JetStream directory (`storeDir`) and, when a Postgres
projection is enabled, that schema. Stop the service, copy the directory
(`cp -a` or a filesystem snapshot), start it again; the op log is the source of
truth and replays on start. A node needs no backup: its micro store is rebuilt
from the master.
