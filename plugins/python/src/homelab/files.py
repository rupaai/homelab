"""``homelab.files``: folders and files."""

from __future__ import annotations

import datetime
import io
import os
from contextlib import contextmanager
from pathlib import Path
from typing import (
    Any,
    BinaryIO,
    Dict,
    Iterator,
    List,
    Optional,
    Sequence,
    Tuple,
    Union,
)

from ._http import CHUNK, Body, Transport, quote_part
from .errors import HomelabError
from .types import File, FileList, Folder, UploadResult, _opt_str

PathLike = Union[str, "os.PathLike[str]"]
Data = Union[bytes, bytearray, memoryview, str, "os.PathLike[str]", BinaryIO]
When = Union[str, datetime.date, datetime.datetime]


def _when(v: Optional[When]) -> Optional[str]:
    if v is None or isinstance(v, str):
        return v
    return v.isoformat()


def _expect_dict(status: int, payload: Any) -> Dict[str, Any]:
    if not isinstance(payload, dict):
        raise HomelabError(status, "Unexpected answer from the server.")
    return payload


def _remaining(f: Any) -> Optional[int]:
    """Bytes left in a seekable file object, or None when it cannot be known."""
    try:
        if not f.seekable():
            return None
        pos = f.tell()
        end = f.seek(0, io.SEEK_END)
        f.seek(pos)
        return int(end - pos)
    except (AttributeError, OSError, ValueError):
        return None


@contextmanager
def _body(data: Data) -> Iterator[Tuple[Body, Optional[int], Optional[str]]]:
    """Yield (body, length, default name) for bytes, a path or a binary file object."""
    if isinstance(data, (bytes, bytearray, memoryview)):
        raw = bytes(data) if not isinstance(data, bytes) else data
        yield raw, len(raw), None
    elif isinstance(data, (str, os.PathLike)):
        path = os.fspath(data)
        with open(path, "rb") as f:
            yield f, os.fstat(f.fileno()).st_size, os.path.basename(os.fsdecode(path)) or None
    elif hasattr(data, "read"):
        if isinstance(data, io.TextIOBase):
            raise TypeError("upload() needs a file opened in binary mode ('rb').")
        yield data, _remaining(data), None
    else:
        raise TypeError("upload() data must be bytes, a file path or a binary file object.")


class Files:
    """Folders and files. Reach it as ``homelab.files``."""

    def __init__(self, transport: Transport) -> None:
        self._t = transport

    def __repr__(self) -> str:
        return f"Files({self._t!r})"

    # -- folders -----------------------------------------------------------

    def folders(self) -> List[Folder]:
        """The folders you can see."""
        status, _, payload = self._t.request_json("GET", "/api/files/folders")
        items = _expect_dict(status, payload).get("folders")
        if not isinstance(items, list):
            return []
        return [Folder.from_dict(i) for i in items if isinstance(i, dict)]

    def create_folder(self, name: str, *, disk: Optional[str] = None) -> Folder:
        """Make a folder. Raises HomelabError(409) when the name is taken."""
        body: Dict[str, Any] = {"name": name}
        if disk is not None:
            body["disk"] = disk
        status, _, payload = self._t.request_json("POST", "/api/files/folders", json_body=body)
        return Folder.from_dict(_expect_dict(status, payload))

    def ensure_folder(self, name: str, *, disk: Optional[str] = None) -> Folder:
        """Make the folder, or return the existing one when it is already there."""
        try:
            return self.create_folder(name, disk=disk)
        except HomelabError as e:
            if e.status != 409:
                raise
            for folder in self.folders():
                if folder.name == name:
                    return folder
            raise

    def delete_folder(self, name: str) -> None:
        """Delete an empty folder (409 while it still has files)."""
        self._t.request_json("DELETE", f"/api/files/folders/{quote_part(name)}")

    # -- files -------------------------------------------------------------

    def upload(
        self,
        folder: str,
        data: Data,
        *,
        name: Optional[str] = None,
        tags: Optional[Union[str, Sequence[str]]] = None,
        sha256: Optional[str] = None,
    ) -> UploadResult:
        """Save a file. ``data`` is bytes, a path (str or Path, streamed from disk)
        or a binary file object. When the folder already holds the same bytes the
        existing file comes back with ``duplicate=True``."""
        tag_text = tags if isinstance(tags, str) else (",".join(tags) if tags else None)
        with _body(data) as (body, length, default_name):
            query = {"name": name if name is not None else default_name, "tags": tag_text or None, "sha256": sha256}
            status, headers, payload = self._t.request_json(
                "POST",
                f"/api/files/{quote_part(folder)}",
                query=query,
                data=body,
                length=length,
                content_type="application/octet-stream",
                no_timeout=True,
            )
        duplicate = (headers.get("X-Homelab-Duplicate") or "").strip().lower() == "true"
        return UploadResult(File.from_dict(_expect_dict(status, payload)), duplicate)

    def list(
        self,
        folder: str,
        *,
        tag: Optional[str] = None,
        type: Optional[str] = None,
        taken_after: Optional[When] = None,
        taken_before: Optional[When] = None,
        camera: Optional[str] = None,
        after: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> FileList:
        """One page of files, newest first. Pass ``next`` back as ``after`` for more."""
        query = {
            "tag": tag,
            "type": type,
            "taken_after": _when(taken_after),
            "taken_before": _when(taken_before),
            "camera": camera,
            "after": after,
            "limit": limit,
        }
        status, _, payload = self._t.request_json("GET", f"/api/files/{quote_part(folder)}", query=query)
        d = _expect_dict(status, payload)
        items = d.get("files")
        files = [File.from_dict(i) for i in items if isinstance(i, dict)] if isinstance(items, list) else []
        return FileList(files, _opt_str(d.get("next")))

    def all(
        self,
        folder: str,
        *,
        tag: Optional[str] = None,
        type: Optional[str] = None,
        taken_after: Optional[When] = None,
        taken_before: Optional[When] = None,
        camera: Optional[str] = None,
        after: Optional[str] = None,
        limit: Optional[int] = None,
    ) -> Iterator[File]:
        """Every matching file, fetching pages as needed (follows ``next``)."""
        cursor = after
        while True:
            page = self.list(
                folder,
                tag=tag,
                type=type,
                taken_after=taken_after,
                taken_before=taken_before,
                camera=camera,
                after=cursor,
                limit=limit,
            )
            yield from page.files
            if not page.next or page.next == cursor:
                return
            cursor = page.next

    def info(self, folder: str, id: str) -> File:
        """The file's details."""
        status, _, payload = self._t.request_json("GET", self._file_path(folder, id) + "/info")
        return File.from_dict(_expect_dict(status, payload))

    def update(
        self,
        folder: str,
        id: str,
        *,
        tags: Optional[Sequence[str]] = None,
        info: Optional[Dict[str, Any]] = None,
    ) -> File:
        """Replace ``tags`` and/or ``info``. A field left out (None) stays as it is."""
        body: Dict[str, Any] = {}
        if tags is not None:
            body["tags"] = list(tags)
        if info is not None:
            body["info"] = info
        status, _, payload = self._t.request_json("PATCH", self._file_path(folder, id) + "/info", json_body=body)
        return File.from_dict(_expect_dict(status, payload))

    def download(self, folder: str, id: str, *, range: Optional[str] = None) -> bytes:
        """The file's bytes. ``range`` is a header value such as ``"bytes=0-99"``."""
        resp = self._t.open(
            "GET", self._file_path(folder, id), headers=self._range(range), timeout=None
        )
        try:
            return self._t.read_all(resp)
        finally:
            resp.close()

    def download_to(self, folder: str, id: str, path: PathLike, *, range: Optional[str] = None) -> Path:
        """Stream the file to ``path`` without holding it in memory. Returns the path."""
        target = Path(path)
        resp = self._t.open(
            "GET", self._file_path(folder, id), headers=self._range(range), timeout=None
        )
        try:
            with open(target, "wb") as out:
                try:
                    while True:
                        chunk = self._t.read_all(resp, CHUNK)
                        if not chunk:
                            break
                        out.write(chunk)
                except BaseException:
                    out.close()
                    target.unlink(missing_ok=True)
                    raise
        finally:
            resp.close()
        return target

    def delete(self, folder: str, id: str) -> None:
        """Move the file to the trash."""
        self._t.request_json("DELETE", self._file_path(folder, id))

    # -- helpers -----------------------------------------------------------

    @staticmethod
    def _file_path(folder: str, id: str) -> str:
        return f"/api/files/{quote_part(folder)}/{quote_part(id)}"

    @staticmethod
    def _range(value: Optional[str]) -> Optional[Dict[str, str]]:
        return {"Range": value} if value else None


__all__ = ["Files"]
