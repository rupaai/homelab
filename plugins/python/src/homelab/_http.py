"""Minimal HTTP transport on urllib. Knows the token; never prints it."""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from contextlib import closing
from email.message import Message
from http.client import HTTPException, HTTPResponse
from typing import Any, BinaryIO, Dict, Mapping, Optional, Tuple, Union, cast

from .errors import HomelabError

USER_AGENT = "homelab-sdk-python/0.1.0"
CHUNK = 1024 * 1024


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Never follow redirects: the token must not travel to another place."""

    def redirect_request(self, *args: Any, **kwargs: Any) -> None:
        return None


def quote_part(part: str) -> str:
    """URL-encode one path segment (``/`` is encoded too)."""
    return urllib.parse.quote(str(part), safe="")


def _origin(url: str) -> str:
    s = urllib.parse.urlsplit(url)
    host = s.hostname or ""
    if ":" in host:
        host = f"[{host}]"
    if s.port:
        host += f":{s.port}"
    return f"{s.scheme}://{host}{s.path}".rstrip("/")


def network_error(exc: BaseException, base: str) -> HomelabError:
    reason: Any = exc.reason if isinstance(exc, urllib.error.URLError) else exc
    text = str(reason) or type(reason).__name__
    return HomelabError(0, f"Could not reach Homelab at {_origin(base)}: {text}")


def _http_error(exc: urllib.error.HTTPError) -> HomelabError:
    status = exc.code
    message = ""
    try:
        raw = exc.read(65536)
        parsed = json.loads(raw.decode("utf-8", "replace"))
        if isinstance(parsed, dict) and isinstance(parsed.get("error"), str):
            message = parsed["error"]
    except (ValueError, OSError, HTTPException):
        pass
    finally:
        exc.close()
    if not message:
        message = f"HTTP {status} {exc.reason}".strip()
    return HomelabError(status, message)


Body = Union[bytes, BinaryIO]


class Transport:
    def __init__(self, base_url: str, token: str, timeout: Optional[float]) -> None:
        self._base = base_url.rstrip("/")
        self._token = token
        self.timeout = timeout
        self._opener = urllib.request.build_opener(_NoRedirect)

    def __repr__(self) -> str:
        return f"Transport(url={_origin(self._base)!r})"

    def open(
        self,
        method: str,
        path: str,
        *,
        query: Optional[Mapping[str, Any]] = None,
        headers: Optional[Mapping[str, str]] = None,
        data: Optional[Body] = None,
        length: Optional[int] = None,
        timeout: Optional[float],
    ) -> HTTPResponse:
        """Send a request; return the open response (caller closes) or raise HomelabError."""
        url = self._base + path
        if query:
            pairs = [(k, str(v)) for k, v in query.items() if v is not None]
            if pairs:
                url += "?" + urllib.parse.urlencode(pairs, quote_via=urllib.parse.quote, safe=",")
        hdrs: Dict[str, str] = {
            "Authorization": f"Bearer {self._token}",
            "User-Agent": USER_AGENT,
        }
        if headers:
            hdrs.update(headers)
        if data is not None and length is not None:
            hdrs["Content-Length"] = str(length)
        req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
        try:
            return cast(HTTPResponse, self._opener.open(req, timeout=timeout))
        except urllib.error.HTTPError as e:
            raise _http_error(e) from None
        except (urllib.error.URLError, OSError, HTTPException) as e:
            raise network_error(e, self._base) from None

    def read_all(self, resp: HTTPResponse, amt: Optional[int] = None) -> bytes:
        try:
            return resp.read(amt)
        except (OSError, HTTPException) as e:
            raise network_error(e, self._base) from None

    def request_json(
        self,
        method: str,
        path: str,
        *,
        query: Optional[Mapping[str, Any]] = None,
        json_body: Optional[Any] = None,
        data: Optional[Body] = None,
        length: Optional[int] = None,
        content_type: Optional[str] = None,
        no_timeout: bool = False,
    ) -> Tuple[int, Message, Any]:
        """Returns (status, headers, parsed JSON or None for an empty body)."""
        headers = {"Accept": "application/json"}
        if json_body is not None:
            data = json.dumps(json_body).encode("utf-8")
            length = len(data)
            content_type = "application/json"
        if content_type:
            headers["Content-Type"] = content_type
        resp = self.open(
            method,
            path,
            query=query,
            headers=headers,
            data=data,
            length=length,
            timeout=None if no_timeout else self.timeout,
        )
        with closing(resp):
            status = resp.status
            raw = self.read_all(resp)
            hdrs = resp.headers
        if not raw.strip():
            return status, hdrs, None
        try:
            return status, hdrs, json.loads(raw.decode("utf-8"))
        except ValueError:
            raise HomelabError(status, "The server answered with something that is not JSON.") from None
