#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
capture_file="${1:-}"
if [[ -z "$capture_file" ]]; then
  capture_file="$(find "$script_dir/captures" -maxdepth 1 -type f -name 'iphone-*.pcapng' -print0 | xargs -0 ls -t | head -n 1)"
fi

if [[ -z "$capture_file" || ! -f "$capture_file" ]]; then
  echo "未找到 pcapng 文件。用法: $0 [capture.pcapng]" >&2
  exit 1
fi

exec uv run --with scapy python "$script_dir/analyze-pcap.py" "$capture_file"
