"""Result types. Every object keeps the server's raw dict in ``raw`` so fields
this SDK does not know about yet are never lost (and never an error)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, NamedTuple, Optional


def _str(v: Any, default: str = "") -> str:
    return v if isinstance(v, str) else default


def _int(v: Any, default: int = 0) -> int:
    return v if isinstance(v, int) and not isinstance(v, bool) else default


def _opt_str(v: Any) -> Optional[str]:
    return v if isinstance(v, str) else None


def _opt_int(v: Any) -> Optional[int]:
    return v if isinstance(v, int) and not isinstance(v, bool) else None


def _opt_num(v: Any) -> Optional[float]:
    if isinstance(v, (int, float)) and not isinstance(v, bool):
        return float(v)
    return None


@dataclass(frozen=True)
class Folder:
    name: str
    disk: str
    app: str
    env: str
    files: int
    bytes: int
    created_at: int
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Folder":
        return cls(
            name=_str(d.get("name")),
            disk=_str(d.get("disk")),
            app=_str(d.get("app")),
            env=_str(d.get("env")),
            files=_int(d.get("files")),
            bytes=_int(d.get("bytes")),
            created_at=_int(d.get("created_at")),
            raw=d,
        )


@dataclass(frozen=True)
class Gps:
    lat: float
    lon: float
    alt: Optional[float] = None


@dataclass(frozen=True)
class Meta:
    """What the server found inside the file. Every field is ``None`` when absent."""

    width: Optional[int] = None
    height: Optional[int] = None
    taken_at: Optional[str] = None
    camera_make: Optional[str] = None
    camera_model: Optional[str] = None
    lens: Optional[str] = None
    orientation: Optional[int] = None
    gps: Optional[Gps] = None
    duration_s: Optional[float] = None
    title: Optional[str] = None
    artist: Optional[str] = None
    album: Optional[str] = None
    year: Optional[int] = None
    pages: Optional[int] = None
    author: Optional[str] = None
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Meta":
        gps: Optional[Gps] = None
        g = d.get("gps")
        if isinstance(g, dict):
            lat, lon = _opt_num(g.get("lat")), _opt_num(g.get("lon"))
            if lat is not None and lon is not None:
                gps = Gps(lat=lat, lon=lon, alt=_opt_num(g.get("alt")))
        return cls(
            width=_opt_int(d.get("width")),
            height=_opt_int(d.get("height")),
            taken_at=_opt_str(d.get("taken_at")),
            camera_make=_opt_str(d.get("camera_make")),
            camera_model=_opt_str(d.get("camera_model")),
            lens=_opt_str(d.get("lens")),
            orientation=_opt_int(d.get("orientation")),
            gps=gps,
            duration_s=_opt_num(d.get("duration_s")),
            title=_opt_str(d.get("title")),
            artist=_opt_str(d.get("artist")),
            album=_opt_str(d.get("album")),
            year=_opt_int(d.get("year")),
            pages=_opt_int(d.get("pages")),
            author=_opt_str(d.get("author")),
            raw=d,
        )


@dataclass(frozen=True)
class File:
    id: str
    folder: str
    name: str
    content_type: str
    size: int
    sha256: str
    created_at: int
    created_by: str
    tags: List[str]
    info: Dict[str, Any]
    meta: Meta
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "File":
        tags = d.get("tags")
        info = d.get("info")
        meta = d.get("meta")
        return cls(
            id=_str(d.get("id")),
            folder=_str(d.get("folder")),
            name=_str(d.get("name")),
            content_type=_str(d.get("content_type")),
            size=_int(d.get("size")),
            sha256=_str(d.get("sha256")),
            created_at=_int(d.get("created_at")),
            created_by=_str(d.get("created_by")),
            tags=[t for t in tags if isinstance(t, str)] if isinstance(tags, list) else [],
            info=dict(info) if isinstance(info, dict) else {},
            meta=Meta.from_dict(meta if isinstance(meta, dict) else {}),
            raw=d,
        )


class UploadResult(NamedTuple):
    """``file, duplicate = homelab.files.upload(...)`` works too."""

    file: File
    duplicate: bool


class FileList(NamedTuple):
    """One page of files. ``next`` is the ``after`` for the next page, or None."""

    files: List[File]
    next: Optional[str]


@dataclass(frozen=True)
class SecretInfo:
    """One secret in the list. Never holds the value."""

    name: str
    kind: str  # "config" (set by the owner) or "app" (saved by the app)
    version: int
    updated_at: int  # unix seconds
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "SecretInfo":
        return cls(
            name=_str(d.get("name")),
            kind=_str(d.get("kind")),
            version=_int(d.get("version")),
            updated_at=_int(d.get("updated_at")),
            raw=d,
        )


@dataclass(frozen=True)
class SecretVersion:
    """One kept version of a secret. Never holds the value."""

    version: int
    set_at: int  # unix seconds
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "SecretVersion":
        return cls(version=_int(d.get("version")), set_at=_int(d.get("set_at")), raw=d)


@dataclass(frozen=True)
class SecretSaved:
    """The answer to ``secrets.set``."""

    name: str
    version: int
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "SecretSaved":
        return cls(name=_str(d.get("name")), version=_int(d.get("version")), raw=d)


@dataclass(frozen=True)
class DbInfo:
    """One database."""

    app: str
    env: str
    engine: str  # "postgres" or "sqlite"
    status: str  # for example "up" or "down"
    size_bytes: int
    connections: int
    tables: int
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "DbInfo":
        return cls(
            app=_str(d.get("app")),
            env=_str(d.get("env")),
            engine=_str(d.get("engine")),
            status=_str(d.get("status")),
            size_bytes=_int(d.get("size_bytes")),
            connections=_int(d.get("connections")),
            tables=_int(d.get("tables")),
            raw=d,
        )


@dataclass(frozen=True)
class DbTable:
    """One table. ``rows`` is an estimate."""

    schema: str
    name: str
    rows: int
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "DbTable":
        return cls(schema=_str(d.get("schema")), name=_str(d.get("name")), rows=_int(d.get("rows")), raw=d)


@dataclass(frozen=True)
class DbRows:
    """One page of a table. Each row is a list in the order of ``columns``."""

    columns: List[str]
    rows: List[List[Any]]
    truncated: bool
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "DbRows":
        return cls(**_rows_fields(d), raw=d)


@dataclass(frozen=True)
class DbQueryResult:
    """The answer to ``db.query`` (read-only SQL)."""

    columns: List[str]
    rows: List[List[Any]]
    truncated: bool
    row_count: int
    command: str  # for example "SELECT"
    ms: int  # how long it took
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "DbQueryResult":
        return cls(
            **_rows_fields(d),
            row_count=_int(d.get("row_count")),
            command=_str(d.get("command")),
            ms=_int(d.get("ms")),
            raw=d,
        )


def _rows_fields(d: Dict[str, Any]) -> Dict[str, Any]:
    columns = d.get("columns")
    rows = d.get("rows")
    return {
        "columns": [c for c in columns if isinstance(c, str)] if isinstance(columns, list) else [],
        "rows": [list(r) for r in rows if isinstance(r, list)] if isinstance(rows, list) else [],
        "truncated": d.get("truncated") is True,
    }


@dataclass(frozen=True)
class Release:
    """One release of the app. Dev builds are pre-releases (``prerelease`` is true)."""

    tag: str  # for example "v0.4.0" or "v0.4.0-dev.2"
    prerelease: bool
    commit: str  # the full commit it was made from
    date: str  # ISO 8601
    summary: str  # for example "2 new, 1 fix"; empty without notes
    notes: str  # markdown
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Release":
        return cls(
            tag=_str(d.get("tag")),
            prerelease=d.get("prerelease") is True,
            commit=_str(d.get("commit")),
            date=_str(d.get("date")),
            summary=_str(d.get("summary")),
            notes=_str(d.get("notes")),
            raw=d,
        )


LOG_LEVELS = ("unknown", "debug", "info", "warn", "error", "fatal")


@dataclass(frozen=True)
class LogLine:
    """One log line."""

    id: int
    time: str  # RFC 3339
    level: str  # one of LOG_LEVELS
    msg: str
    app: str
    env: str
    service: str
    stream: str  # "stdout", "stderr" or "event"
    extra: Dict[str, Any] = field(default_factory=dict)  # extra fields of a JSON log line
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "LogLine":
        extra = d.get("extra")
        return cls(
            id=_int(d.get("id")),
            time=_str(d.get("time")),
            level=_str(d.get("level"), "unknown"),
            msg=_str(d.get("msg")),
            app=_str(d.get("app")),
            env=_str(d.get("env")),
            service=_str(d.get("service")),
            stream=_str(d.get("stream")),
            extra=extra if isinstance(extra, dict) else {},
            raw=d,
        )


@dataclass(frozen=True)
class LogSearchResult:
    """The answer to ``logs.search``: newest lines first."""

    lines: List[LogLine]
    counts: Dict[str, int]  # lines per level under the same search, ignoring level and min_level
    next: Optional[str]  # pass as ``before`` for the next older page; None when there is none
    raw: Dict[str, Any] = field(default_factory=dict, repr=False, compare=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "LogSearchResult":
        counts = {k: 0 for k in LOG_LEVELS}
        raw_counts = d.get("counts")
        if isinstance(raw_counts, dict):
            counts.update({k: v for k, v in raw_counts.items() if isinstance(k, str) and _int(v, -1) >= 0})
        items = d.get("lines")
        nxt = d.get("next")
        return cls(
            lines=[LogLine.from_dict(i) for i in items if isinstance(i, dict)] if isinstance(items, list) else [],
            counts=counts,
            next=nxt if isinstance(nxt, str) else None,
            raw=d,
        )
