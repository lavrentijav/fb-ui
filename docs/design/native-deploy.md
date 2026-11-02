# Deploying the native fleet

Status: 2026-11-02. This is the operator-facing install page; the roadmap is
`native-prod-plan.md`, the node's internals `native-node-status.md`.

## Artifacts

`make native-binaries` builds both (static, version-stamped, checksummmed) into
`dist/native/`. CI builds the same for linux/amd64 and linux/arm64 plus the
interface (`internal/web/dist`) and attaches them to a `native-v*` tag as
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

Then:

```
fbmaster -c /etc/fbmaster/config.json -check     # what would bind
fbmaster -c /etc/fbmaster/config.json -create-ca # first master only
systemctl enable --now fbmaster
```

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

## Node

```
install -m 0755 fbnode /usr/local/bin/fbnode
cp packaging/fbnode.service /etc/systemd/system/
systemctl daemon-reload
```

The node must already have Xray (`/usr/local/x-ui/bin/xray-linux-amd64` by
default) in the `lite` profile.

```
fbnode enroll -master https://master.example -token enr1_... \
  -node ru4 -masters fi1=tls://fi1.m.example:443,ru2=tls://ru2.m.example:443 \
  [-profile relay] -dir /etc/fbnode
systemctl enable --now fbnode
```

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
