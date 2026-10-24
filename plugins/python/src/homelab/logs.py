"""``homelab.logs``: search the logs, and print your own logs the way Homelab reads them.

``Homelab().logs.search(...)`` calls ``GET /api/logs``. An app key sees its own app and
environment only.

``homelab.logs.setup()`` needs no token. It makes Python's ``logging`` print one JSON line
per record to stdout, so Homelab can tell the level, the message and the extra fields.
"""

from __future__ import annotations

import datetime
import json
import logging
import sys
from typing import Any, Dict, Iterable, List, Optional, Union

from ._http import Transport
from .errors import HomelabError
from .types import LOG_LEVELS, LogSearchResult

Time = Union[datetime.datetime, str, int]


def _level(value: str) -> str:
    if value not in LOG_LEVELS:
        raise HomelabError(0, f"Unknown log level {value!r}. Use one of: {', '.join(LOG_LEVELS)}.")
    return value


def _time(name: str, value: Time) -> str:
    if isinstance(value, datetime.datetime):
        if value.tzinfo is None:
            value = value.replace(tzinfo=datetime.timezone.utc)
        return value.astimezone(datetime.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
    if isinstance(value, bool) or not isinstance(value, (str, int)):
        raise HomelabError(0, f"{name} must be a datetime, an RFC 3339 text or unix milliseconds.")
    return str(value)


def _id(name: str, value: Union[int, str]) -> str:
    try:
        n = int(value)
    except (TypeError, ValueError):
        n = -1
    if isinstance(value, bool) or n < 0:
        raise HomelabError(0, f"{name} must be a line id (a whole number).")
    return str(n)


class Logs:
    """Thin wrapper over ``GET /api/logs``."""

    def __init__(self, transport: Transport) -> None:
        self._t = transport

    def __repr__(self) -> str:
        return "Logs()"

    def search(
        self,
        q: Optional[str] = None,
        level: Union[str, Iterable[str], None] = None,
        *,
        min_level: Optional[str] = None,
        app: Optional[str] = None,
        env: Optional[str] = None,
        service: Optional[str] = None,
        since: Optional[Time] = None,
        until: Optional[Time] = None,
        before: Union[int, str, None] = None,
        after: Union[int, str, None] = None,
        limit: Optional[int] = None,
    ) -> LogSearchResult:
        """Searches the logs, newest lines first.

        ``level`` is one level or a list (exact match); ``min_level`` keeps that level and
        louder. ``before`` / ``after`` are line ids (use ``result.next`` to page back).
        ``limit`` is 1 to 500 (default 100).
        """
        query: Dict[str, str] = {}
        if q is not None and q.strip():
            query["q"] = q
        for key, value in (("app", app), ("env", env), ("service", service)):
            if value:
                query[key] = value
        if level is not None:
            levels: List[str] = [level] if isinstance(level, str) else list(level)
            if levels:
                query["level"] = ",".join(_level(x) for x in levels)
        if min_level is not None:
            query["min_level"] = _level(min_level)
        if since is not None:
            query["since"] = _time("since", since)
        if until is not None:
            query["until"] = _time("until", until)
        if before is not None:
            query["before"] = _id("before", before)
        if after is not None:
            query["after"] = _id("after", after)
        if limit is not None:
            if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 500:
                raise HomelabError(0, "The limit must be a whole number from 1 to 500.")
            query["limit"] = str(limit)
        status, _, payload = self._t.request_json("GET", "/api/logs", query=query or None)
        if not isinstance(payload, dict):
            raise HomelabError(status, "Unexpected answer from the server.")
        return LogSearchResult.from_dict(payload)


# --- setup(): JSON lines on stdout -------------------------------------------------------

_LEVEL_NAMES = {"DEBUG": "debug", "INFO": "info", "WARNING": "warn", "ERROR": "error", "CRITICAL": "fatal"}

# The attributes every LogRecord has; anything else on a record came from ``extra=``.
_STANDARD = set(vars(logging.LogRecord("", 0, "", 0, "", None, None))) | {"message", "asctime", "taskName"}

_OWN_KEYS = ("time", "level", "msg", "logger", "exception")


def _level_name(levelno: int) -> str:
    """DEBUG -> debug, INFO -> info, WARNING -> warn, ERROR -> error, CRITICAL -> fatal. Custom levels go to the nearest one below."""
    name = _LEVEL_NAMES.get(logging.getLevelName(levelno))
    if name:
        return name
    if levelno >= logging.CRITICAL:
        return "fatal"
    if levelno >= logging.ERROR:
        return "error"
    if levelno >= logging.WARNING:
        return "warn"
    if levelno >= logging.INFO:
        return "info"
    return "debug"


class JsonFormatter(logging.Formatter):
    """One JSON object per record: time, level, msg, logger, exception (when there is one), and the ``extra=`` fields."""

    def format(self, record: logging.LogRecord) -> str:
        try:
            msg = record.getMessage()
        except Exception:  # a bad format string must not lose the line
            msg = str(record.msg)
        when = datetime.datetime.fromtimestamp(record.created, datetime.timezone.utc)
        out: Dict[str, Any] = {
            "time": when.isoformat(timespec="milliseconds").replace("+00:00", "Z"),
            "level": _level_name(record.levelno),
            "msg": msg,
            "logger": record.name,
        }
        if record.exc_info and record.exc_info[0] is not None:
            out["exception"] = self.formatException(record.exc_info)
        elif record.exc_text:
            out["exception"] = record.exc_text
        for key, value in vars(record).items():
            if key in _STANDARD or key.startswith("_"):
                continue
            out[f"extra_{key}" if key in _OWN_KEYS else key] = value
        try:
            return json.dumps(out, default=str)
        except (TypeError, ValueError):  # for example a value that contains itself
            safe = {k: v if k in _OWN_KEYS else repr(v) for k, v in out.items()}
            return json.dumps(safe, default=str)


class _StdoutHandler(logging.StreamHandler):  # type: ignore[type-arg]
    """Writes to whatever ``sys.stdout`` is right now (so it follows redirects)."""

    _homelab_json = True

    def __init__(self) -> None:
        super().__init__(sys.stdout)

    @property
    def stream(self) -> Any:
        return sys.stdout

    @stream.setter
    def stream(self, value: Any) -> None:
        pass


def setup(level: Union[str, int] = "DEBUG") -> None:
    """Makes Python's ``logging`` print one JSON line per record to stdout.

    Needs no token. Safe to call twice: the second call only changes the level.
    ``level`` is a name like ``"INFO"`` or a number.
    """
    root = logging.getLogger()
    root.setLevel(level.upper() if isinstance(level, str) else level)
    for h in root.handlers:
        if getattr(h, "_homelab_json", False):
            return
    handler = _StdoutHandler()
    handler.setFormatter(JsonFormatter())
    root.addHandler(handler)
