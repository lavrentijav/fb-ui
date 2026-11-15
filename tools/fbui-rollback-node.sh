#!/bin/sh
# Roll a node back to the pre-cutover state: old x-ui on :443, fbnode on :444.
# Run as: ssh <node> 'sh /root/fbui-rollback.sh'
set -e
cp -f /etc/fbnode/config.json.pre-cutover /etc/fbnode/config.json
systemctl restart fbnode
systemctl enable --now x-ui
echo "rolled back: x-ui :443, fbnode :444 (config.json.pre-cutover restored)"
