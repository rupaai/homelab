"""A small in-process fake Homelab server (http.server in a thread)."""

from __future__ import annotations

import json
import threading
import time
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, Iterator, List, Optional
from urllib.parse import parse_qs, urlsplit

import pytest

from homelab import Homelab

TOKEN = "hlk_" + "S3cretTokenValue0123456789abcdefghijklmnopq"[:43]


@dataclass
class Recorded:
    method: str
    raw_path: str  # path exactly as sent, still percent-encoded
    raw_query: str
    query: Dict[str, List[str]]
    headers: Dict[str, str]  # lower-case names
    body: bytes


@dataclass
class Canned:
    status: int = 200
    json: Any = None
    body: Optional[bytes] = None
    headers: Dict[str, str] = field(default_factory=dict)
    delay: float = 0.0


class FakeServer:
    def __init__(self) -> None:
        self.requests: List[Recorded] = []
        self.queue: List[Canned] = []
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args: Any) -> None:
                pass

            def _read_body(self) -> bytes:
                if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
                    out = b""
                    while True:
                        size = int(self.rfile.readline().split(b";")[0], 16)
                        if size == 0:
                            self.rfile.readline()
                            return out
                        out += self.rfile.read(size)
                        self.rfile.readline()
                n = int(self.headers.get("Content-Length") or 0)
                return self.rfile.read(n) if n else b""

            def _handle(self) -> None:
                parts = urlsplit(self.path)
                owner.requests.append(
                    Recorded(
                        method=self.command,
                        raw_path=parts.path,
                        raw_query=parts.query,
                        query=parse_qs(parts.query, keep_blank_values=True),
                        headers={k.lower(): v for k, v in self.headers.items()},
                        body=self._read_body(),
                    )
                )
                c = owner.queue.pop(0) if owner.queue else Canned(status=500, json={"error": "nothing queued"})
                if c.delay:
                    time.sleep(c.delay)
                body = c.body if c.body is not None else (b"" if c.json is None else json.dumps(c.json).encode())
                self.send_response(c.status)
                for k, v in c.headers.items():
                    self.send_header(k, v)
                if c.status != 204:
                    self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                if c.status != 204 and self.command != "HEAD":
                    try:
                        self.wfile.write(body)
                    except OSError:
                        pass

            do_GET = do_POST = do_PATCH = do_DELETE = _handle

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.httpd.daemon_threads = True
        self.url = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()

    def reply(self, status: int = 200, json: Any = None, body: Optional[bytes] = None,
              headers: Optional[Dict[str, str]] = None, delay: float = 0.0) -> None:
        self.queue.append(Canned(status, json, body, headers or {}, delay))

    @property
    def last(self) -> Recorded:
        return self.requests[-1]

    def close(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()


@pytest.fixture(autouse=True)
def _no_proxy(monkeypatch: pytest.MonkeyPatch) -> None:
    for k in ("http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY"):
        monkeypatch.delenv(k, raising=False)
    monkeypatch.setenv("NO_PROXY", "127.0.0.1,localhost")
    monkeypatch.setenv("no_proxy", "127.0.0.1,localhost")
    monkeypatch.delenv("HOMELAB_API_URL", raising=False)
    monkeypatch.delenv("HOMELAB_TOKEN", raising=False)


@pytest.fixture
def server() -> Iterator[FakeServer]:
    s = FakeServer()
    yield s
    s.close()


@pytest.fixture
def hl(server: FakeServer) -> Homelab:
    return Homelab(server.url, TOKEN)


ULID = "01J9Z3X5K8Q2W4E6R8T0Y1U2I3"


def file_json(**over: Any) -> Dict[str, Any]:
    d: Dict[str, Any] = {
        "id": ULID, "folder": "posters", "name": "poster.jpg", "content_type": "image/jpeg",
        "size": 5, "sha256": "ab" * 32, "created_at": 1700000000, "created_by": "app:movies/prod",
        "tags": ["dune"], "info": {"k": 1}, "meta": {"width": 10, "height": 20},
    }
    d.update(over)
    return d


def folder_json(**over: Any) -> Dict[str, Any]:
    d: Dict[str, Any] = {"name": "posters", "disk": "main", "app": "movies", "env": "prod",
                         "files": 0, "bytes": 0, "created_at": 1700000000}
    d.update(over)
    return d
