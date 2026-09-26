#!/usr/bin/env bash
# Build the panel binary the way release.yml does: static, against musl from a
# Bootlin toolchain. A static glibc build crashes in getaddrinfo on hosts whose
# glibc differs from the build host's (seen on Debian 13 with a Debian 12 build).
#
# usage: deploy/build/release-static.sh [out] [arch]   (arch: amd64|arm64, default amd64)
set -euo pipefail
out=${1:-build/x-ui}
arch=${2:-amd64}
case "$arch" in
  amd64) bootlin=x86-64 ;;
  arm64) bootlin=aarch64 ;;
  *) echo "unsupported arch $arch" >&2; exit 1 ;;
esac
cache=${XDG_CACHE_HOME:-$HOME/.cache}/fbui-toolchains
mkdir -p "$cache"
dir=$(find "$cache" -maxdepth 1 -type d -name "${bootlin}--musl--stable-*" | sort -r | head -n1)
if [[ -z "$dir" ]]; then
  base="https://toolchains.bootlin.com/downloads/releases/toolchains/$bootlin/tarballs/"
  tarball=$(curl -fsSL "$base" | grep -oE "${bootlin}--musl--stable-[^\"]+\\.tar\\.xz" | sort -r | head -n1)
  [[ -n "$tarball" ]] || { echo "no Bootlin musl toolchain for $bootlin" >&2; exit 1; }
  curl -fL -sS -o "$cache/$tarball" "$base$tarball"
  tar -xf "$cache/$tarball" -C "$cache"
  rm -f "$cache/$tarball"
  dir=$(find "$cache" -maxdepth 1 -type d -name "${bootlin}--musl--stable-*" | sort -r | head -n1)
fi
cc=$(find "$dir/bin" -name '*-gcc.br_real' -type f -executable | head -n1)
[[ -n "$cc" ]] || { echo "no gcc.br_real in $dir" >&2; exit 1; }
root=$(cd "$(dirname "$0")/../.." && pwd)
commit=$(git -C "$root" rev-parse --short=8 HEAD)
ldflags="-w -s -linkmode external -extldflags '-static'"
ldflags+=" -X github.com/mhsanaei/3x-ui/v3/internal/config.buildCommit=$commit"
ldflags+=" -X github.com/mhsanaei/3x-ui/v3/internal/config.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cd "$root"
PATH="$dir/bin:$PATH" CC="$cc" CGO_ENABLED=1 GOOS=linux GOARCH="$arch" \
  go build -buildvcs=true -ldflags "$ldflags" -o "$out" .
echo "$out $(file -b "$out" | cut -d, -f1-2)"
