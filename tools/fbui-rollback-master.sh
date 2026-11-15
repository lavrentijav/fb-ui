#!/bin/sh
# Roll ru2 back to the pre-cutover state: old x-ui on :443, fbmaster on :1443.
# Run as: ssh ru2 'sh /root/fbui-rollback.sh'
set -e
pkill -x fbmaster 2>/dev/null || true
sleep 1
cp -f /opt/fbui-staging/config.alt.json.pre-cutover /opt/fbui-staging/config.alt.json
systemctl enable --now x-ui
cd /opt/fbui-staging && setsid ./fbmaster -c ./config.alt.json >> master.alt.log 2>&1 < /dev/null &
echo "rolled back: x-ui :443, fbmaster :1443 (config.alt.json.pre-cutover restored)"
