"""``homelab.db``: look inside an app's database. Read-only.

Apps connect with ``DATABASE_URL`` and any normal driver; this is for listing databases,
tables and rows, and for read-only SQL. Writing is for people in the companion.
"""

from __future__ import annotations

import os
from typing import Any, Dict, List, Optional, Tuple

from ._http import Transport, quote_part
from .errors import HomelabError
from .types import DbInfo, DbQueryResult, DbRows, DbTable


def _place(app: Optional[str], env: Optional[str]) -> Tuple[str, str]:
    """The app and environment: as given, or from HOMELAB_APP and HOMELAB_ENV."""
    a = app if app is not None else os.environ.get("HOMELAB_APP")
    e = env if env is not None else os.environ.get("HOMELAB_ENV")
    if not a:
        raise HomelabError(0, "Set HOMELAB_APP or pass app")
    if not e:
        raise HomelabError(0, "Set HOMELAB_ENV or pass env")
    return a, e


def _seg(value: str) -> str:
    """Encode one path segment. "." and ".." are refused: URL parsing would change the path."""
    if value in (".", ".."):
        raise HomelabError(0, f"Invalid path segment: {value!r}")
    return quote_part(value)


def _whole(name: str, value: Any, low: int, high: Optional[int] = None) -> None:
    if isinstance(value, bool) or not isinstance(value, int) or value < low or (high is not None and value > high):
        span = f"{low} to {high}" if high is not None else f"{low}"
        raise HomelabError(0, f"The {name} must be a whole number from {span}.")


def _expect_dict(status: int, payload: Any) -> Dict[str, Any]:
    if not isinstance(payload, dict):
        raise HomelabError(status, "Unexpected answer from the server.")
    return payload


class Db:
    """Thin wrappers over ``/api/db``. ``app`` and ``env`` default to ``HOMELAB_APP`` and ``HOMELAB_ENV``."""

    def __init__(self, transport: Transport) -> None:
        self._t = transport

    def __repr__(self) -> str:
        return "Db()"

    def list(self) -> List[DbInfo]:
        """``GET /api/db``: the databases this key can see. An app key sees only its own."""
        status, _, payload = self._t.request_json("GET", "/api/db")
        items = _expect_dict(status, payload).get("databases")
        return [DbInfo.from_dict(i) for i in items if isinstance(i, dict)] if isinstance(items, list) else []

    def info(self, app: Optional[str] = None, env: Optional[str] = None) -> DbInfo:
        """``GET /api/db/{app}/{env}``."""
        a, e = _place(app, env)
        status, _, payload = self._t.request_json("GET", f"/api/db/{_seg(a)}/{_seg(e)}")
        return DbInfo.from_dict(_expect_dict(status, payload))

    def tables(self, app: Optional[str] = None, env: Optional[str] = None) -> List[DbTable]:
        """``GET /api/db/{app}/{env}/tables``."""
        a, e = _place(app, env)
        status, _, payload = self._t.request_json("GET", f"/api/db/{_seg(a)}/{_seg(e)}/tables")
        items = _expect_dict(status, payload).get("tables")
        return [DbTable.from_dict(i) for i in items if isinstance(i, dict)] if isinstance(items, list) else []

    def rows(
        self,
        table: str,
        *,
        app: Optional[str] = None,
        env: Optional[str] = None,
        schema: Optional[str] = None,
        limit: Optional[int] = None,
        offset: Optional[int] = None,
    ) -> DbRows:
        """``GET /api/db/{app}/{env}/tables/{table}/rows``: one page. ``limit`` is 1-200, ``offset`` from 0."""
        a, e = _place(app, env)
        path = f"/api/db/{_seg(a)}/{_seg(e)}/tables/{_seg(table)}/rows"
        if limit is not None:
            _whole("limit", limit, 1, 200)
        if offset is not None:
            _whole("offset", offset, 0)
        query = {"schema": schema, "limit": limit, "offset": offset}
        status, _, payload = self._t.request_json("GET", path, query=query)
        return DbRows.from_dict(_expect_dict(status, payload))

    def query(self, sql: str, *, app: Optional[str] = None, env: Optional[str] = None) -> DbQueryResult:
        """``POST /api/db/{app}/{env}/query``: read-only SQL. Never sends ``write``."""
        a, e = _place(app, env)
        status, _, payload = self._t.request_json(
            "POST", f"/api/db/{_seg(a)}/{_seg(e)}/query", json_body={"sql": sql}
        )
        return DbQueryResult.from_dict(_expect_dict(status, payload))
