#!/usr/bin/env python3
"""Extract HTTP requests, DNS queries, and TLS SNI from an iPhone pcapng."""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import shutil
import tempfile
from collections import Counter, defaultdict
from datetime import datetime
from pathlib import Path
from typing import Any

from scapy.all import DNS, IP, IPv6, Raw, TCP, UDP  # type: ignore
from scapy.utils import RawPcapNgReader  # type: ignore


PROCESS_COMMENT = re.compile(
    rb"PID: (\d+), ProcName: ([^,]*), EPID: (\d+), EProcName: ([^,]*)"
)
HTTP_METHODS = (b"GET ", b"POST ", b"PUT ", b"DELETE ", b"PATCH ", b"HEAD ", b"OPTIONS ")
SENSITIVE_HEADERS = {
    "authorization",
    "cookie",
    "proxy-authorization",
    "set-cookie",
    "x-api-key",
    "x-auth-token",
    "token",
}
TEXT_CONTENT_TYPES = (
    "application/json",
    "application/xml",
    "application/x-www-form-urlencoded",
    "multipart/form-data",
    "text/",
)


def packet_time(metadata: Any) -> str:
    timestamp = ((metadata.tshigh << 32) + metadata.tslow) / metadata.tsresol
    return datetime.fromtimestamp(timestamp).astimezone().isoformat()


def process_name(metadata: Any) -> str:
    comment = (metadata.comments or [b""])[0]
    match = PROCESS_COMMENT.search(comment)
    if not match:
        return "unknown"
    return (match.group(2) or match.group(4)).decode("utf-8", "replace") or "kernel/unknown"


def tls_sni(data: bytes) -> str | None:
    """Read SNI from a single-segment TLS ClientHello."""
    try:
        if len(data) < 9 or data[0] != 22 or data[5] != 1:
            return None
        position = 9 + 2 + 32
        position += 1 + data[position]
        position += 2 + int.from_bytes(data[position : position + 2], "big")
        position += 1 + data[position]
        extensions_end = position + 2 + int.from_bytes(
            data[position : position + 2], "big"
        )
        position += 2
        while position + 4 <= min(extensions_end, len(data)):
            extension_type = int.from_bytes(data[position : position + 2], "big")
            length = int.from_bytes(data[position + 2 : position + 4], "big")
            value = data[position + 4 : position + 4 + length]
            position += 4 + length
            if extension_type == 0 and len(value) >= 5:
                name_length = int.from_bytes(value[3:5], "big")
                return value[5 : 5 + name_length].decode("idna")
    except (IndexError, UnicodeError, ValueError):
        return None
    return None


def parse_headers(block: bytes) -> tuple[str, dict[str, str]]:
    lines = block.split(b"\r\n")
    request_line = lines[0].decode("latin1", "replace")
    headers: dict[str, str] = {}
    for line in lines[1:]:
        if not line:
            break
        if b":" not in line:
            continue
        key, value = line.split(b":", 1)
        name = key.decode("latin1", "replace").lower()
        headers[name] = value.strip().decode("latin1", "replace")
    return request_line, headers


def safe_headers(headers: dict[str, str]) -> dict[str, str]:
    return {
        key: "<redacted>" if key.lower() in SENSITIVE_HEADERS else value
        for key, value in headers.items()
    }


def encoded_body(body: bytes, content_type: str) -> dict[str, Any]:
    result: dict[str, Any] = {"size": len(body)}
    if not body:
        return result
    if any(kind in content_type.lower() for kind in TEXT_CONTENT_TYPES):
        result["text"] = body.decode("utf-8", "replace")
    else:
        result["base64"] = base64.b64encode(body).decode("ascii")
    return result


def contiguous_chunks(segments: dict[int, tuple[bytes, str]]) -> list[tuple[int, bytes]]:
    """Merge TCP segments, removing retransmits and preserving capture gaps."""
    chunks: list[tuple[int, bytes]] = []
    chunk_start: int | None = None
    chunk = bytearray()
    current_end: int | None = None
    for sequence, (payload, _) in sorted(segments.items()):
        if chunk_start is None or current_end is None or sequence > current_end:
            if chunk_start is not None:
                chunks.append((chunk_start, bytes(chunk)))
            chunk_start = sequence
            chunk = bytearray(payload)
            current_end = sequence + len(payload)
            continue
        overlap = current_end - sequence
        if overlap < len(payload):
            chunk.extend(payload[overlap:])
            current_end += len(payload) - overlap
    if chunk_start is not None:
        chunks.append((chunk_start, bytes(chunk)))
    return chunks


def extract_http_requests(
    streams: dict[tuple[str, int, str, int], dict[int, tuple[bytes, str]]],
    stream_processes: dict[tuple[str, int, str, int], Counter[str]],
) -> list[dict[str, Any]]:
    requests: list[dict[str, Any]] = []
    request_pattern = re.compile(
        rb"(?:GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS) [^\r\n ]+ HTTP/1\.[01]\r\n"
    )
    for stream, segments in streams.items():
        source_ip, source_port, destination_ip, destination_port = stream
        process = stream_processes[stream].most_common(1)[0][0]
        for chunk_start, chunk in contiguous_chunks(segments):
            position = 0
            while True:
                match = request_pattern.search(chunk, position)
                if match is None:
                    break
                request_start = match.start()
                header_end = chunk.find(b"\r\n\r\n", request_start)
                if header_end < 0:
                    break
                header_block = chunk[request_start:header_end]
                request_line, headers = parse_headers(header_block)
                parts = request_line.split(" ", 2)
                if len(parts) != 3:
                    position = header_end + 4
                    continue
                try:
                    content_length = int(headers.get("content-length", "0"))
                except ValueError:
                    content_length = 0
                body_start = header_end + 4
                body_end = min(body_start + content_length, len(chunk))
                body = chunk[body_start:body_end]
                method, target, http_version = parts
                host = headers.get("host", destination_ip)
                segment_sequence = chunk_start + request_start
                segment_time = max(
                    (
                        captured_at
                        for sequence, (_, captured_at) in segments.items()
                        if sequence <= segment_sequence
                    ),
                    default="unknown",
                )
                requests.append(
                    {
                        "time": segment_time,
                        "process": process,
                        "source": f"{source_ip}:{source_port}",
                        "destination": f"{destination_ip}:{destination_port}",
                        "method": method,
                        "url": f"http://{host}{target}",
                        "http_version": http_version,
                        "headers": safe_headers(headers),
                        "body": {
                            **encoded_body(body, headers.get("content-type", "")),
                            "complete": len(body) == content_length,
                            "expected_size": content_length,
                        },
                    }
                )
                position = max(body_end, header_end + 4)
    return sorted(requests, key=lambda request: request["time"])


def parse_capture(path: Path) -> dict[str, Any]:
    process_counts: Counter[str] = Counter()
    protocol_counts: Counter[str] = Counter()
    dns_queries: Counter[tuple[str, str]] = Counter()
    tls_hosts: Counter[tuple[str, str]] = Counter()
    streams: dict[
        tuple[str, int, str, int], dict[int, tuple[bytes, str]]
    ] = defaultdict(dict)
    stream_processes: dict[tuple[str, int, str, int], Counter[str]] = defaultdict(
        Counter
    )
    packet_count = 0
    byte_count = 0

    for raw, metadata in RawPcapNgReader(str(path)):
        packet_count += 1
        byte_count += len(raw)
        process = process_name(metadata)
        process_counts[process] += 1

        # pymobiledevice3 prepends a synthetic 14-byte Ethernet header to pdp_ip.
        if len(raw) < 15:
            continue
        network_frame = raw[14:]
        version = network_frame[0] >> 4
        try:
            if version == 4:
                packet = IP(network_frame)
                protocol_counts["IPv4"] += 1
            elif version == 6:
                packet = IPv6(network_frame)
                protocol_counts["IPv6"] += 1
            else:
                protocol_counts["non-IP"] += 1
                continue
        except Exception:
            protocol_counts["malformed"] += 1
            continue

        transport = None
        if TCP in packet:
            transport = packet[TCP]
            protocol_counts["TCP"] += 1
        elif UDP in packet:
            transport = packet[UDP]
            protocol_counts["UDP"] += 1

        if DNS in packet and packet[DNS].qr == 0:
            try:
                question = packet[DNS].qd
                domain = question.qname.decode("utf-8", "replace").rstrip(".")
                dns_queries[(domain, process)] += 1
            except (AttributeError, UnicodeError):
                pass

        if transport is None or Raw not in packet:
            continue
        payload = bytes(packet[Raw].load)
        hostname = tls_sni(payload)
        if hostname:
            tls_hosts[(hostname, process)] += 1

        if TCP in packet:
            stream = (packet.src, transport.sport, packet.dst, transport.dport)
            sequence = int(transport.seq)
            previous = streams[stream].get(sequence)
            if previous is None or len(payload) > len(previous[0]):
                streams[stream][sequence] = (payload, packet_time(metadata))
            stream_processes[stream][process] += 1

    requests = extract_http_requests(streams, stream_processes)

    return {
        "source": str(path),
        "summary": {
            "packets": packet_count,
            "bytes": byte_count,
            "protocols": dict(protocol_counts),
            "processes": [
                {"process": process, "packets": count}
                for process, count in process_counts.most_common()
            ],
            "http_request_count": len(requests),
        },
        "http_requests": requests,
        "tls_sni": [
            {"hostname": host, "process": process, "count": count}
            for (host, process), count in tls_hosts.most_common()
        ],
        "dns_queries": [
            {"domain": domain, "process": process, "count": count}
            for (domain, process), count in dns_queries.most_common()
        ],
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("capture", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    source = args.capture.expanduser().resolve()
    output = (args.output or source.with_suffix(".requests.json")).expanduser().resolve()

    # Copy first so an actively-written final pcapng block cannot change under us.
    with tempfile.TemporaryDirectory(prefix="iphone-pcap-") as directory:
        snapshot = Path(directory) / source.name
        shutil.copyfile(source, snapshot)
        report = parse_capture(snapshot)
    report["source"] = str(source)

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    os.chmod(output, 0o600)
    summary = report["summary"]
    print(f"数据包: {summary['packets']}，HTTP 请求: {summary['http_request_count']}")
    print(f"报告: {output}")


if __name__ == "__main__":
    main()
