#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
capture_dir="$script_dir/captures"
mkdir -p "$capture_dir"
umask 077

wg_port="${WG_PORT:-51820}"
web_port="${WEB_PORT:-8081}"
wg_keys="${WG_KEYS:-$HOME/.mitmproxy/wireguard.conf}"

if lsof -nP -iUDP:"$wg_port" 2>/dev/null | grep -qi mitm; then
  echo "mitmproxy 已在 UDP $wg_port 运行；先停止旧进程，或设置不同的 WG_PORT。" >&2
  exit 1
fi

echo "WireGuard: UDP $wg_port"
echo "Web UI: http://127.0.0.1:$web_port（浏览器会自动打开带临时令牌的地址）"
echo "解析日志: $capture_dir/http.jsonl"
echo "原始流: $capture_dir/flows-$(date +%Y%m%d).mitm"

exec /opt/homebrew/bin/mitmweb \
  --mode "wireguard:$wg_keys@$wg_port" \
  --web-host 127.0.0.1 \
  --web-port "$web_port" \
  --web-open-browser \
  --scripts "$script_dir/mitm_addon.py" \
  --save-stream-file "+$capture_dir/flows-%Y%m%d.mitm" \
  --set "iphone_jsonl=$capture_dir/http.jsonl"
