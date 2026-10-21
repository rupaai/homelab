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
