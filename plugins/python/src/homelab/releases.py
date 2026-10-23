"""``homelab.releases``: the app's own versions.

An app's version is the ``HOMELAB_VERSION`` setting Homelab gives it; ``version()`` reads it.
``Releases.list()`` lists every release of the app (dev builds are pre-releases).
"""

from __future__ import annotations

import os
from typing import Any, Dict, List, Optional

from ._http import Transport
from .errors import HomelabError
from .types import Release


def version() -> Optional[str]:
    """This app's version (``HOMELAB_VERSION``), or ``None`` when Homelab has not given it one."""
    v = os.environ.get("HOMELAB_VERSION", "").strip()
    return v or None


class Releases:
    """Thin wrapper over ``GET /api/releases``."""

    def __init__(self, transport: Transport) -> None:
        self._t = transport

    def __repr__(self) -> str:
        return "Releases()"

    def list(self, *, limit: Optional[int] = None) -> List[Release]:
        """The app's releases, newest first. ``limit`` is a whole number from 1."""
        query: Optional[Dict[str, str]] = None
        if limit is not None:
            if isinstance(limit, bool) or not isinstance(limit, int) or limit < 1:
                raise HomelabError(0, "The limit must be a whole number from 1.")
            query = {"limit": str(limit)}
        status, _, payload = self._t.request_json("GET", "/api/releases", query=query)
        items: Any = payload.get("releases") if isinstance(payload, dict) else None
        if not isinstance(items, list):
            raise HomelabError(status, "Unexpected answer from the server.")
        return [Release.from_dict(i) for i in items if isinstance(i, dict)]
