#!/usr/bin/env python3
"""Export mitmproxy's iPhone WireGuard profile and a QR code."""

from __future__ import annotations

import argparse
import base64
import json
import os
import subprocess
from pathlib import Path

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey


def public_key(private_key_b64: str) -> str:
    private_key = X25519PrivateKey.from_private_bytes(
        base64.b64decode(private_key_b64)
    )
    raw = private_key.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    return base64.b64encode(raw).decode("ascii")


def main() -> None:
    parser = argparse.ArgumentParser(
        description="生成供 iPhone 导入的 mitmproxy WireGuard 配置和二维码"
    )
    parser.add_argument(
        "endpoint", help="iPhone 蜂窝网络可访问的地址，例如 vpn.example.com:51820"
    )
    parser.add_argument(
        "--keys",
        type=Path,
        default=Path.home() / ".mitmproxy" / "wireguard.conf",
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=Path(__file__).resolve().parent / "captures" / "iphone-wireguard.conf",
    )
    parser.add_argument(
        "--ipv6",
        action="store_true",
        help="同时路由 IPv6；mitmproxy 对透明 IPv6 的支持仍有限",
    )
    args = parser.parse_args()

    if ":" not in args.endpoint:
        parser.error("endpoint 必须包含端口，例如 vpn.example.com:51820")
    keys = json.loads(args.keys.expanduser().read_text(encoding="utf-8"))
    allowed_ips = "0.0.0.0/0, ::/0" if args.ipv6 else "0.0.0.0/0"
    config = f"""[Interface]
PrivateKey = {keys['client_key']}
Address = 10.0.0.1/32
DNS = 10.0.0.53

[Peer]
PublicKey = {public_key(keys['server_key'])}
AllowedIPs = {allowed_ips}
Endpoint = {args.endpoint}
PersistentKeepalive = 25
"""

    output = args.output.expanduser().resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(config, encoding="utf-8")
    os.chmod(output, 0o600)
    qr_path = output.with_suffix(".png")
    subprocess.run(
        ["qrencode", "-o", str(qr_path), "-r", str(output)], check=True
    )
    os.chmod(qr_path, 0o600)
    print(f"配置: {output}")
    print(f"二维码: {qr_path}")
    print("注意: 配置包含私钥，不要发送给其他人。")


if __name__ == "__main__":
    main()
