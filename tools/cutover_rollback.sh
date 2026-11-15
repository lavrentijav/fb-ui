#!/bin/sh
# Roll the 2026-11-14 :443 cutover back to the pre-cutover state on every host
# that was changed.  de2 / fi2 / firewalkbot were NOT touched and are excluded.
# Usage:  sh tools/cutover_rollback.sh
set -e
echo "== ru2 (master -> x-ui :443, fbmaster :1443) =="
ssh ru2 'sh /root/fbui-rollback.sh' || true
for h in fi1 de3 nl3 dns2 dns1; do
  echo "== $h (x-ui :443, fbnode :444) =="
  ssh "$h" 'sh /root/fbui-rollback.sh' || true
done
echo "== dns1 nginx reverse restore =="
ssh dns1 'cp -f /etc/nginx/conf.d/sub-proxy.conf.pre-cutover /etc/nginx/conf.d/sub-proxy.conf && systemctl reload nginx && echo dns1-nginx-restored' || true
echo "rollback complete"
