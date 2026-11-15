#!/usr/bin/env bash
# End-to-end smoke for the native binaries: a temporary master on loopback,
# health/auth/UI login, a VLESS ws+xhttp subscription, and a relay enrollment.
# Nothing leaves the machine and no credential is printed.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
master_pid=""
node_pid=""
cleanup() {
  if [ -n "$node_pid" ]; then kill "$node_pid" 2>/dev/null || true; fi
  if [ -n "$master_pid" ]; then kill "$master_pid" 2>/dev/null || true; fi
  wait 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

fail() { echo "FAIL: $*" >&2; exit 1; }

admin=$(free_port); api=$(free_port); edge=$(free_port); client=$(free_port); gateway=$(free_port)
mkdir -p "$tmp/data" "$tmp/js"

cat > "$tmp/config.json" <<EOF
{
  "name": "smoke",
  "masters": ["smoke"],
  "storeDir": "$tmp/js",
  "dataDir": "$tmp/data",
  "publicName": "smoke.test",
  "edgeListen": "127.0.0.1:$edge",
  "clientPort": $client,
  "gatewayHost": "127.0.0.1",
  "gatewayPort": $gateway,
  "advertise": "127.0.0.1:$gateway",
  "apiListen": "127.0.0.1:$api",
  "adminListen": "127.0.0.1:$admin",
  "uiDir": "$root/internal/web/dist",
  "caCert": "$tmp/data/ca.pem",
  "caKey": "$tmp/data/ca-key.pem",
  "cert": "$tmp/data/master.pem",
  "key": "$tmp/data/master-key.pem"
}
EOF

"$root/dist/native/fbmaster" -c "$tmp/config.json" -check >/dev/null
"$root/dist/native/fbmaster" -c "$tmp/config.json" -create-ca >"$tmp/master.log" 2>&1 &
master_pid=$!

base="http://127.0.0.1:$admin"
ready=no
for _ in $(seq 1 120); do
  if curl -fsS "$base/healthz" >"$tmp/healthz" 2>/dev/null; then ready=yes; break; fi
  sleep 0.5
done
[ "$ready" = yes ] || { tail -30 "$tmp/master.log" >&2; fail "master never became healthy"; }
grep -q '^ok smoke' "$tmp/healthz" || fail "healthz body"

token=""
for _ in $(seq 1 80); do
  token=$(sed -n 's/.*first admin token (shown once): //p' "$tmp/master.log" | tail -1)
  [ -n "$token" ] && break
  sleep 0.5
done
[ -n "$token" ] || fail "the bootstrap token was not logged"

code=$(curl -s -o /dev/null -w '%{http_code}' "$base/nodes/state")
[ "$code" = 401 ] || fail "unauthenticated admin call answered $code"
code=$(curl -s -o "$tmp/nodes.json" -w '%{http_code}' -H "Authorization: Bearer $token" "$base/nodes/state")
[ "$code" = 200 ] || fail "authenticated nodes/state answered $code"

for page in /panel/ /panel/login /panel/nodes /panel/clients /panel/inbounds /panel/subscriptions; do
  code=$(curl -s -o "$tmp/page" -w '%{http_code}' "$base$page")
  [ "$code" = 200 ] || fail "$page answered $code"
  grep -q 'X_UI_NATIVE=true' "$tmp/page" || fail "$page lost the native flag"
done

code=$(curl -s -o "$tmp/login.json" -c "$tmp/cookies" -w '%{http_code}' \
  -H 'Content-Type: application/json' -d "{\"token\":\"$token\"}" "$base/api/v1/ui/login")
[ "$code" = 200 ] || fail "ui login answered $code"
if grep -qF "$token" "$tmp/login.json"; then fail "the login response echoed the token"; fi
code=$(curl -s -o "$tmp/me.json" -b "$tmp/cookies" -w '%{http_code}' "$base/api/v1/ui/me")
[ "$code" = 200 ] || fail "ui/me with the session answered $code"

auth=(-H "Authorization: Bearer $token" -H 'Content-Type: application/json')
code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "${auth[@]}" -d '{"node":"smoke"}' "$base/nodes/smoke/spec")
[ "$code" = 200 ] || fail "node spec registration answered $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${auth[@]}" \
  -d '{"tag":"vpn","port":443,"network":"ws","path":"/ws","host":"smoke.test","route":"direct"}' "$base/inbounds/smoke")
[ "$code" = 201 ] || fail "ws inbound answered $code"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${auth[@]}" \
  -d '{"tag":"xh","port":8443,"network":"xhttp","path":"/xh","host":"smoke.test","route":"direct"}' "$base/inbounds/smoke")
[ "$code" = 201 ] || fail "xhttp inbound answered $code"
for inbound in vpn xh; do
  code=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "${auth[@]}" \
    -d "{\"node\":\"smoke\",\"inbound\":\"$inbound\",\"email\":\"alice\",\"uuid\":\"11111111-1111-1111-1111-111111111111\",\"enable\":true}" "$base/clients")
  [ "$code" = 201 ] || fail "client on $inbound answered $code"
done
code=$(curl -s -o "$tmp/sub.json" -w '%{http_code}' -X PUT "${auth[@]}" -d '{}' "$base/sub/alice")
[ "$code" = 200 ] || fail "subscription creation answered $code"
subtoken=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$tmp/sub.json")
[ -n "$subtoken" ] || fail "no subscription token"
code=$(curl -s -o "$tmp/sublinks" -D "$tmp/subheaders" -w '%{http_code}' "$base/sub/$subtoken?format=plain")
[ "$code" = 200 ] || fail "subscription fetch answered $code"
[ "$(grep -c 'vless://' "$tmp/sublinks")" = 2 ] || fail "the subscription does not carry both links"
grep -q 'type=ws' "$tmp/sublinks" || fail "no ws link"
grep -q 'type=xhttp' "$tmp/sublinks" || fail "no xhttp link"
grep -qi '^subscription-userinfo:' "$tmp/subheaders" || fail "no subscription-userinfo header"

python3 - "$tmp/upstream.db" <<'PY'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.executescript("""
CREATE TABLE inbounds (id INTEGER PRIMARY KEY, tag TEXT, remark TEXT, port INTEGER,
  protocol TEXT, settings TEXT, stream_settings TEXT, enable INTEGER);
INSERT INTO inbounds VALUES(1,'up-ws','Imported ws',443,'vless',
  '{"clients":[{"id":"77777777-7777-7777-7777-777777777777","email":"imp1","enable":true,"totalGB":1000,"expiryTime":0,"limitIp":1},{"id":"88888888-8888-8888-8888-888888888888","email":"imp2","enable":true,"totalGB":0,"expiryTime":0,"limitIp":0}]}',
  '{"network":"ws","wsSettings":{"path":"/up"}}',1);
INSERT INTO inbounds VALUES(2,'up-trojan','Imported trojan',8443,'trojan',
  '{"clients":[{"password":"pw","email":"imp3","enable":true}]}','{}',1);
""")
c.commit()
PY
if ! "$root/dist/native/fbmaster" import -db "$tmp/upstream.db" -local-node imp \
  -apply -verify -master "$base" -token "$token" >"$tmp/import.out" 2>"$tmp/import.log"; then
  cat "$tmp/import.out" "$tmp/import.log" >&2
  fail "import verify failed"
fi
grep -q 'imported: 1 inbound(s)' "$tmp/import.out" || fail "the importer did not create the inbound"
grep -q '"missing": 0' "$tmp/import.out" || fail "the importer verify reported a gap"
grep -q '1 client(s) stay in the old panel' "$tmp/import.out" || fail "the trojan client was not reported"
code=$(curl -s -o "$tmp/imp-inbounds.json" -w '%{http_code}' -H "Authorization: Bearer $token" "$base/inbounds/imp")
[ "$code" = 200 ] || fail "imported inbound listing answered $code"
grep -q 'up-ws' "$tmp/imp-inbounds.json" || fail "the imported inbound is not on the master"
code=$(curl -s -o "$tmp/imp-clients.json" -w '%{http_code}' -H "Authorization: Bearer $token" "$base/clients")
[ "$code" = 200 ] || fail "imported client listing answered $code"
grep -q 'imp1' "$tmp/imp-clients.json" || fail "the imported client is not on the master"

code=$(curl -s -o "$tmp/enroll.json" -w '%{http_code}' -X POST "${auth[@]}" \
  -d '{"node":"smoke-node","ttl":"1h"}' "$base/enroll/tokens")
[ "$code" = 200 ] || fail "enroll token answered $code"
enroll_token=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$tmp/enroll.json")
"$root/dist/native/fbnode" enroll -master "$base" -token "$enroll_token" -node smoke-node \
  -masters "smoke=tls://127.0.0.1:$edge" -profile relay -dir "$tmp/node" -xray "$tmp/no-xray" >/dev/null 2>&1
grep -q '"profile": "relay"' "$tmp/node/config.json" || fail "enroll did not write a relay config"

node_listen=$(free_port)
python3 - "$tmp/node/config.json" "$node_listen" <<'PY'
import json, sys
path, port = sys.argv[1], int(sys.argv[2])
cfg = json.load(open(path))
cfg["listen"] = f"127.0.0.1:{port}"
cfg["pullEvery"] = "1s"
cfg["heartbeat"] = "1s"
json.dump(cfg, open(path, "w"), indent=2)
PY
"$root/dist/native/fbnode" -c "$tmp/node/config.json" >"$tmp/node.log" 2>&1 &
node_pid=$!
sleep 3
kill -0 "$node_pid" 2>/dev/null || { tail -20 "$tmp/node.log" >&2; fail "the enrolled relay exited"; }
grep -q 'node smoke-node (relay)' "$tmp/node.log" || fail "the relay did not start"

echo "smoke: OK (master health/auth/UI/session, ws+xhttp subscription, importer apply+verify, relay enroll/start)"
