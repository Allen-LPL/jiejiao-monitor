"""Write a privacy-conscious, machine-readable HTTP request log."""

from __future__ import annotations

import base64
import json
import time
from pathlib import Path
from typing import Any

from mitmproxy import ctx, http


SENSITIVE_HEADERS = {
    "authorization",
    "cookie",
    "proxy-authorization",
    "set-cookie",
    "x-api-key",
}
TEXT_TYPES = (
    "application/json",
    "application/javascript",
    "application/xml",
    "application/x-www-form-urlencoded",
    "text/",
)


def safe_headers(headers: http.Headers) -> dict[str, str]:
    return {
        key: "<redacted>" if key.lower() in SENSITIVE_HEADERS else value
        for key, value in headers.items(multi=True)
    }


def body_preview(message: http.Message, limit: int = 64 * 1024) -> dict[str, Any]:
    raw = message.raw_content or b""
    if not raw:
        return {"body_size": 0}
    result: dict[str, Any] = {
        "body_size": len(raw),
        "body_truncated": len(raw) > limit,
    }
    sample = raw[:limit]
    content_type = message.headers.get("content-type", "").lower()
    if any(kind in content_type for kind in TEXT_TYPES):
        result["body_text"] = sample.decode("utf-8", "replace")
    else:
        result["body_base64"] = base64.b64encode(sample).decode("ascii")
    return result


class IPhoneRequestLogger:
    def load(self, loader: Any) -> None:
        loader.add_option(
            "iphone_jsonl",
            str,
            "",
            "Path for parsed iPhone HTTP request/response JSONL output.",
        )

    def _write(self, record: dict[str, Any]) -> None:
        configured = ctx.options.iphone_jsonl
        if not configured:
            return
        path = Path(configured).expanduser()
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")

    def request(self, flow: http.HTTPFlow) -> None:
        request = flow.request
        peer = flow.client_conn.peername
        self._write(
            {
                "event": "request",
                "time": time.time(),
                "flow_id": flow.id,
                "client": peer[0] if peer else None,
                "method": request.method,
                "url": request.pretty_url,
                "http_version": request.http_version,
                "headers": safe_headers(request.headers),
                **body_preview(request),
            }
        )

    def response(self, flow: http.HTTPFlow) -> None:
        response = flow.response
        if response is None:
            return
        self._write(
            {
                "event": "response",
                "time": time.time(),
                "flow_id": flow.id,
                "url": flow.request.pretty_url,
                "status": response.status_code,
                "reason": response.reason,
                "headers": safe_headers(response.headers),
                **body_preview(response),
            }
        )

    def error(self, flow: http.HTTPFlow) -> None:
        self._write(
            {
                "event": "error",
                "time": time.time(),
                "flow_id": flow.id,
                "url": flow.request.pretty_url if flow.request else None,
                "error": str(flow.error) if flow.error else "unknown error",
            }
        )


addons = [IPhoneRequestLogger()]
