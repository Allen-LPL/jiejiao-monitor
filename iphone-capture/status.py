#!/usr/bin/env python3
"""Report the connected iPhone and local capture service status."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parent


def run_json(command: list[str]) -> Any:
    result = subprocess.run(command, capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return json.loads(result.stdout)


def mobile_command(*args: str) -> list[str]:
    return ["uvx", "--from", "pymobiledevice3", "pymobiledevice3", *args]


def first_value(data: dict[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in data:
            return data[key]
    return None


def process_listening(port: int, protocol: str) -> bool:
    result = subprocess.run(
        ["lsof", "-nP", f"-i{protocol.upper()}:{port}"],
        capture_output=True,
        text=True,
        timeout=5,
    )
    return result.returncode == 0 and "mitm" in result.stdout.lower()


def collect() -> dict[str, Any]:
    devices = run_json(mobile_command("usbmux", "list"))
    if not devices:
        return {
            "connected": False,
            "wireguard_listener": process_listening(51820, "udp"),
            "web_ui": process_listening(8081, "tcp"),
        }

    device = devices[0]
    info = run_json(mobile_command("lockdown", "info"))
    battery = run_json(mobile_command("diagnostics", "battery", "single"))

    try:
        wifi = run_json(mobile_command("diagnostics", "battery", "wifi"))
    except RuntimeError:
        wifi = {}

    battery_data = battery.get("BatteryData", {})
    wifi_power = wifi.get("IOPowerManagement", {})
    wifi_active = bool(
        wifi.get("IO80211RSNDone")
        or wifi.get("IO80211Channel")
        or wifi_power.get("CurrentPowerState", 0) > 1
    )

    return {
        "connected": True,
        "connection": device.get("ConnectionType"),
        "name": device.get("DeviceName"),
        "model": device.get("ProductType"),
        "ios": device.get("ProductVersion"),
        "build": device.get("BuildVersion"),
        "udid": device.get("UniqueDeviceID"),
        "activated": info.get("ActivationState") == "Activated",
        "paired_and_trusted": bool(info.get("TrustedHostAttached")),
        "locked": bool(info.get("PasswordProtected")),
        "telephony_capable": bool(info.get("TelephonyCapability")),
        "battery_percent": first_value(
            battery, "CurrentCapacity", "AbsoluteCapacity"
        ),
        "charging": bool(
            first_value(battery, "ExternalConnected", "AppleRawExternalConnected")
        ),
        "battery_cycle_count": battery_data.get("CycleCount"),
        "wifi_active": wifi_active,
        "wifi_interface": wifi.get("IOInterfaceName"),
        "wireguard_listener": process_listening(51820, "udp"),
        "web_ui": process_listening(8081, "tcp"),
        "jsonl_log": str(ROOT / "captures" / "http.jsonl"),
    }


def print_human(status: dict[str, Any]) -> None:
    if not status["connected"]:
        print("iPhone: 未连接或尚未信任此电脑")
        print(
            f"WireGuard: {'运行中' if status['wireguard_listener'] else '未运行'}"
        )
        return

    udid = status.get("udid") or ""
    short_udid = f"{udid[:8]}…{udid[-6:]}" if len(udid) > 14 else udid
    print(
        f"iPhone: 已连接（{status['connection']}），{status['name']} / "
        f"{status['model']} / iOS {status['ios']} ({status['build']})"
    )
    print(
        f"配对: {'已信任' if status['paired_and_trusted'] else '未信任'}；"
        f"激活: {'是' if status['activated'] else '否'}；UDID: {short_udid}"
    )
    print(
        f"电池: {status['battery_percent']}%；"
        f"{'正在充电' if status['charging'] else '未充电'}；"
        f"循环 {status['battery_cycle_count']} 次"
    )
    print(
        f"Wi-Fi: {'活跃' if status['wifi_active'] else '未活跃'}"
        + (f"（{status['wifi_interface']}）" if status.get("wifi_interface") else "")
    )
    print(
        f"mitmproxy WireGuard: "
        f"{'UDP 51820 监听中' if status['wireguard_listener'] else '未运行'}"
    )
    print(
        f"Web UI: {'http://127.0.0.1:8081（运行中）' if status['web_ui'] else '未运行'}"
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--json", action="store_true", help="输出 JSON")
    args = parser.parse_args()
    try:
        status = collect()
    except (RuntimeError, subprocess.TimeoutExpired, json.JSONDecodeError) as exc:
        raise SystemExit(f"读取 iPhone 状态失败: {exc}") from exc
    if args.json:
        print(json.dumps(status, ensure_ascii=False, indent=2))
    else:
        print_human(status)


if __name__ == "__main__":
    main()
