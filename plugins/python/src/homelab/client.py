"""The ``Homelab`` client."""

from __future__ import annotations

import os
import re
import urllib.parse
from typing import Optional

from ._http import Transport, _origin
from .db import Db
from .errors import HomelabError
from .files import Files
from .secrets import DEFAULT_SECRETS_DIR, PathLike, Secrets

# Layout: every service is one module with one class that takes the Transport
# (files.py -> Files). `homelab.data` will be added the same way.


class Homelab:
    """Client for the Homelab API.

    >>> homelab = Homelab()            # HOMELAB_API_URL and HOMELAB_TOKEN
    >>> homelab.files.folders()

    ``timeout`` (seconds, default 60) applies to normal calls; uploads and
    downloads have no timeout. ``secrets_dir`` (default ``/run/secrets``) is where
    ``secrets.get`` looks for a secret's file first.
    """

    db: Db
    files: Files
    secrets: Secrets

    def __init__(
        self,
        url: Optional[str] = None,
        token: Optional[str] = None,
        *,
        timeout: Optional[float] = 60.0,
        secrets_dir: PathLike = DEFAULT_SECRETS_DIR,
    ) -> None:
        url = (url or os.environ.get("HOMELAB_API_URL") or "").strip()
        token = (token or os.environ.get("HOMELAB_TOKEN") or "").strip()
        if not url:
            raise HomelabError(0, "Set HOMELAB_API_URL or pass url")
        if not token:
            raise HomelabError(0, "Set HOMELAB_TOKEN or pass token")
        if urllib.parse.urlsplit(url).scheme not in ("http", "https"):
            raise HomelabError(0, "The Homelab url must start with http:// or https://")
        if re.search(r"[\x00-\x20\x7f]", token):
            raise HomelabError(0, "The Homelab token contains spaces or control characters")
        self._url = url.rstrip("/")
        self._transport = Transport(self._url, token, timeout)
        self.files = Files(self._transport)
        self.secrets = Secrets(self._transport, secrets_dir)
        self.db = Db(self._transport)

    @property
    def url(self) -> str:
        return self._url

    def __repr__(self) -> str:
        return f"Homelab(url={_origin(self._url)!r})"
