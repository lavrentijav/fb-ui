#!/bin/sh
# Start the staging master on ru2 and import the cleaned old panel DB.
# The bootstrap token is read from the log and never printed.
set -eu

DIR=/opt/fbui-staging
LOG=$DIR/master.log

if ! pgrep -f 'fbmaster -c ./config.json' >/dev/null 2>&1; then
  cd "$DIR"
  setsid ./fbmaster -c ./config.json -create-ca >>"$LOG" 2>&1 </dev/null &
fi
for _ in $(seq 1 40); do
  curl -fsS http://127.0.0.1:18081/healthz >/dev/null 2>&1 && break
  sleep 0.5
done

token=""
n=0
while [ "$n" -lt 120 ]; do
  token=$(sed -n 's/.*first admin token (shown once): //p' "$LOG" | tail -1)
  [ -n "$token" ] && break
  sleep 1
  n=$((n + 1))
done
if [ -z "$token" ]; then
  echo "no bootstrap token" >&2
  tail -30 "$LOG" >&2
  exit 1
fi

"$DIR/fbmaster" import -db /etc/x-ui/x-ui.db -local-node ru2 \
  -node-map 14=ru1,13=ru,1=de2,12=de3,8=fi1,15=nl3 \
  -apply -verify -master http://127.0.0.1:18081 -token "$token" \
  >"$DIR/verify.json" 2>"$DIR/import.log"

python3 - "$DIR/verify.json" <<'PY'
import json
import sys

text = open(sys.argv[1], encoding="utf-8").read()
data = json.loads(text[text.find("{"):])
for key in ("inbounds", "clients", "groups"):
    print(key, data.get(key, {}))
print("problems", len(data.get("problems") or []))
PY
