#!/usr/bin/env python3
"""Sync the latest attendance Authorization and device ID into jj/.env."""

from __future__ import annotations

import argparse
import os
import shutil
import tempfile
from pathlib import Path

from scapy.all import IP, IPv6, Raw, TCP  # type: ignore
from scapy.utils import RawPcapNgReader  # type: ignore


TARGET = b"/api/mob/signIn/onlineSigns/listByDay?"


def latest_headers(capture: Path) -> dict[str, str]:
    result: dict[str, str] | None = None
    with tempfile.TemporaryDirectory(prefix="iphone-auth-") as directory:
        snapshot = Path(directory) / capture.name
        shutil.copyfile(capture, snapshot)
        for raw, _ in RawPcapNgReader(str(snapshot)):
            if len(raw) < 15:
                continue
            network_frame = raw[14:]
            version = network_frame[0] >> 4
            try:
                packet = (
                    IP(network_frame)
                    if version == 4
                    else IPv6(network_frame) if version == 6 else None
                )
            except Exception:
                continue
            if packet is None or TCP not in packet or Raw not in packet:
                continue
            payload = bytes(packet[Raw].load)
            if not payload.startswith(b"GET ") or TARGET not in payload:
                continue
            headers: dict[str, str] = {}
            for line in payload.split(b"\r\n\r\n", 1)[0].split(b"\r\n")[1:]:
                if b":" not in line:
                    continue
                key, value = line.split(b":", 1)
                headers[key.decode("latin1").lower()] = value.strip().decode("latin1")
            result = headers
    if result is None:
        raise RuntimeError("抓包中未找到 listByDay 请求")
    return result


def update_env(path: Path, updates: dict[str, str]) -> None:
    lines = path.read_text(encoding="utf-8").splitlines() if path.exists() else []
    found: set[str] = set()
    output: list[str] = []
    for line in lines:
        if "=" not in line or line.lstrip().startswith("#"):
            output.append(line)
            continue
        key = line.split("=", 1)[0].strip()
        if key in updates:
            output.append(f"{key}={updates[key]}")
            found.add(key)
        else:
            output.append(line)
    for key, value in updates.items():
        if key not in found:
            output.append(f"{key}={value}")

    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write("\n".join(output) + "\n")
        os.chmod(temporary_name, 0o600)
        os.replace(temporary_name, path)
    except Exception:
        try:
            os.unlink(temporary_name)
        except FileNotFoundError:
            pass
        raise


def main() -> None:
    root = Path(__file__).resolve().parent.parent
    parser = argparse.ArgumentParser()
    parser.add_argument("capture", nargs="?", type=Path)
    parser.add_argument("--env", type=Path, default=root / "jj" / ".env")
    args = parser.parse_args()

    capture = args.capture
    if capture is None:
        candidates = list((root / "iphone-capture" / "captures").glob("iphone-*.pcapng"))
        if not candidates:
            raise SystemExit("未找到 iPhone pcapng 抓包")
        capture = max(candidates, key=lambda item: item.stat().st_mtime)
    capture = capture.expanduser().resolve()
    headers = latest_headers(capture)
    authorization = headers.get("authorization", "")
    device_sn = headers.get("device-sn", "")
    if not authorization:
        raise SystemExit("listByDay 请求中没有 Authorization")
    if not device_sn:
        raise SystemExit("listByDay 请求中没有 device-sn")

    env_path = args.env.expanduser().resolve()
    update_env(
        env_path,
        {"JJ_AUTH_TOKEN": authorization, "JJ_DEVICE_SN": device_sn},
    )
    print(f"来源抓包: {capture}")
    print(f"已更新: {env_path}")
    print(f"Authorization: 已写入（{len(authorization)} 字符，不回显）")
    print(f"device-sn: 已写入（{len(device_sn)} 字符，不回显）")


if __name__ == "__main__":
    main()
