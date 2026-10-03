#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
exec uv run --with scapy python "$script_dir/sync-attendance-auth.py" "$@"
