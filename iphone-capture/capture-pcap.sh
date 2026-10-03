#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
capture_dir="$script_dir/captures"
mkdir -p "$capture_dir"
umask 077

capture_interface="${IPHONE_INTERFACE:-pdp_ip}"
capture_process="${IPHONE_PROCESS:-}"
capture_file="${1:-$capture_dir/iphone-$(date +%Y%m%d-%H%M%S).pcapng}"

args=(pcap --out "$capture_file")
if [[ "$capture_interface" != "all" ]]; then
  args+=(--interface "$capture_interface")
fi
if [[ -n "$capture_process" ]]; then
  args+=(--process "$capture_process")
fi

echo "正在抓取 iPhone 流量到: $capture_file"
echo "接口: ${capture_interface}；按 Ctrl-C 停止"
echo "提示: 抓蜂窝数据前请在 iPhone 关闭 Wi-Fi，并产生一些网络请求。"
exec uvx --from pymobiledevice3 pymobiledevice3 "${args[@]}"
