#!/bin/bash
# usage: tools/netlab/run.sh [go test flags] — runs the netlab tests in an
# isolated user+net+mount namespace; nothing on the host network changes.
set -euo pipefail
cd "$(dirname "$0")/../.."
bin=$(mktemp -d)/netlab.test
go test -c -tags netlab -o "$bin" ./internal/native/netlab/
exec unshare --user --map-root-user --net --mount --propagation private \
	env NETLAB=1 NETLAB_XRAY="${NETLAB_XRAY:-/usr/local/x-ui/bin/xray-linux-amd64}" "$bin" -test.v "$@"
