#!/usr/bin/env bash
# 交叉编译全平台二进制，产物放在项目根目录
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log() { printf '\033[0;32m[build]\033[0m %s\n' "$*"; }

build() {
  local os=$1 arch=$2 out=$3
  log "编译 $out ..."
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$out" .
}

build linux  amd64 jj-linux-amd64
build linux  arm64 jj-linux-arm64
build darwin arm64 jj-darwin-arm64
build darwin amd64 jj-darwin-amd64

log "完成。产物："
ls -lh jj-linux-* jj-darwin-* | awk '{print $NF, $5}'
