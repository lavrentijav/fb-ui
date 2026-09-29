#!/bin/bash
# usage: tools/netlab/run.sh [go test flags] — runs the netlab tests in an
# isolated user+net+mount namespace; nothing on the host network changes.
set -euo pipefail
cd "$(dirname "$0")/../.."
tmp=$(mktemp -d)
bin=$tmp/netlab.test
go test -c -tags netlab -o "$bin" ./internal/native/netlab/
CGO_ENABLED=0 go build -o "$tmp/fbnode" ./cmd/fbnode
exec unshare --user --map-root-user --net --mount --propagation private \
	env NETLAB=1 NETLAB_FBNODE="$tmp/fbnode" NETLAB_XRAY="${NETLAB_XRAY:-/usr/local/x-ui/bin/xray-linux-amd64}" "$bin" -test.v "$@"
