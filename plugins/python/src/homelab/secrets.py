"""``homelab.secrets``: an app's own secrets. Values are never logged or put in errors."""

from __future__ import annotations

import os
import re
from pathlib import Path
from typing import Any, Dict, List, Optional, Union

from ._http import Transport, quote_part
from .errors import HomelabError
from .types import SecretInfo, SecretSaved, SecretVersion

DEFAULT_SECRETS_DIR = "/run/secrets"

# The same rule as the server: 1 to 200 of letters, digits and _ . : - (never "." or "..").
# \Z (not $) so a trailing newline does not slip through.
_APP_NAME = re.compile(r"[A-Za-z0-9_.:-]{1,200}\Z")
# Only names that are safe file names are looked up on disk.
_FILE_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")

PathLike = Union[str, "os.PathLike[str]"]


def _check_name(name: str) -> None:
    if not isinstance(name, str) or name in (".", "..") or not _APP_NAME.match(name):
        raise HomelabError(0, "A secret name is 1 to 200 letters, digits and _ . : -")


def _check_version(version: int) -> None:
    if isinstance(version, bool) or not isinstance(version, int) or version < 1:
        raise HomelabError(0, "The version is a number from 1.")


class Secrets:
    def __init__(self, transport: Transport, secrets_dir: PathLike = DEFAULT_SECRETS_DIR) -> None:
        self._t = transport
        self._dir = Path(secrets_dir)

    def __repr__(self) -> str:
        return f"Secrets(dir={str(self._dir)!r})"

    def get(self, name: str, *, version: Optional[int] = None, fresh: bool = False) -> str:
        """The value of a secret.

        Without ``version``, a file ``<secrets_dir>/<name>`` is used when it exists (the
        contents are returned exactly as written). Otherwise, or with ``fresh=True``, it
        asks Homelab: ``GET /api/secrets/{name}[?version=n]``. A missing secret raises
        ``HomelabError`` with status 404; see :meth:`get_or_none`.
        """
        _check_name(name)
        if version is not None:
            _check_version(version)
        elif not fresh:
            from_file = self._read_file(name)
            if from_file is not None:
                return from_file
        query = {"version": version} if version is not None else None
        status, _, payload = self._t.request_json("GET", f"/api/secrets/{quote_part(name)}", query=query)
        value = payload.get("value") if isinstance(payload, dict) else None
        if not isinstance(value, str):
            raise HomelabError(status, "Unexpected answer from the server.")
        return value

    def get_or_none(self, name: str, *, version: Optional[int] = None, fresh: bool = False) -> Optional[str]:
        """Like :meth:`get`, but returns ``None`` when there is no such secret (404).

        Other errors still raise ``HomelabError``.
        """
        try:
            return self.get(name, version=version, fresh=fresh)
        except HomelabError as e:
            if e.status == 404:
                return None
            raise

    def set(self, name: str, value: str) -> SecretSaved:
        """``PUT /api/secrets/{name}``. Saves the value as the next version (needs ``secrets:write``)."""
        _check_name(name)
        try:
            status, _, payload = self._t.request_json(
                "PUT", f"/api/secrets/{quote_part(name)}", json_body={"value": value}
            )
        except HomelabError as e:
            # The server never echoes a value, but nothing we sent may come back in an error.
            if value and value in e.message:
                raise HomelabError(e.status, e.message.replace(value, "[redacted]")) from None
            raise
        return SecretSaved.from_dict(_expect_dict(status, payload))

    def delete(self, name: str) -> None:
        """``DELETE /api/secrets/{name}``. Deleting a secret that is not there is fine."""
        _check_name(name)
        self._t.request_json("DELETE", f"/api/secrets/{quote_part(name)}")

    def list(self) -> List[SecretInfo]:
        """``GET /api/secrets``: names, kinds and versions. No values."""
        status, _, payload = self._t.request_json("GET", "/api/secrets")
        items = _expect_dict(status, payload).get("secrets")
        return [SecretInfo.from_dict(i) for i in items if isinstance(i, dict)] if isinstance(items, list) else []

    def versions(self, name: str) -> List[SecretVersion]:
        """``GET /api/secrets/{name}/versions``: the kept versions, newest first. No values."""
        _check_name(name)
        status, _, payload = self._t.request_json("GET", f"/api/secrets/{quote_part(name)}/versions")
        items = _expect_dict(status, payload).get("versions")
        return [SecretVersion.from_dict(i) for i in items if isinstance(i, dict)] if isinstance(items, list) else []

    def _read_file(self, name: str) -> Optional[str]:
        """The file's contents, or None when there is no such file."""
        if not _FILE_NAME.match(name):
            return None
        path = self._dir / name
        try:
            return path.read_bytes().decode("utf-8")
        except (FileNotFoundError, NotADirectoryError, IsADirectoryError):
            return None
        except UnicodeDecodeError:
            raise HomelabError(0, f"The secret file for {name!r} is not valid UTF-8") from None
        except OSError as e:
            raise HomelabError(0, f"Could not read the secret file for {name!r} ({type(e).__name__})") from None


def _expect_dict(status: int, payload: Any) -> Dict[str, Any]:
    if not isinstance(payload, dict):
        raise HomelabError(status, "Unexpected answer from the server.")
    return payload
