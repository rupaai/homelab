from __future__ import annotations

import datetime
import hashlib
import io
import json
from pathlib import Path
from typing import Any, List

import pytest

from homelab import File, Homelab, HomelabError
from conftest import ULID, FakeServer, file_json

DUP = {"X-Homelab-Duplicate": "true"}


# -- upload ---------------------------------------------------------------

def test_upload_bytes(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, file_json())
    res = hl.files.upload("posters", b"\x00\x01raw", name="poster.jpg", tags=["dune", "poster"], sha256="ab" * 32)
    r = server.last
    assert (r.method, r.raw_path) == ("POST", "/api/files/posters")
    assert r.query == {"name": ["poster.jpg"], "tags": ["dune,poster"], "sha256": ["ab" * 32]}
    assert r.body == b"\x00\x01raw"  # raw bytes, not JSON or multipart
    assert r.headers["content-type"] == "application/octet-stream"
    assert r.headers["content-length"] == "5"
    assert res.duplicate is False
    assert res.file.id == ULID and res.file.meta.width == 10
    file, dup = res  # tuple unpacking
    assert (file.name, dup) == ("poster.jpg", False)


def test_upload_minimal_has_no_query(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, file_json())
    hl.files.upload("posters", bytearray(b"abc"))
    assert server.last.raw_query == "" and server.last.body == b"abc"


def test_upload_empty_body(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, file_json())
    hl.files.upload("posters", b"")
    assert server.last.body == b"" and server.last.headers["content-length"] == "0"


def test_upload_duplicate_header(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, file_json(), headers=DUP)
    res = hl.files.upload("posters", b"x", name="a")
    assert res.duplicate is True and res.file.id == ULID


def test_upload_200_without_header_is_not_duplicate(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, file_json())
    assert hl.files.upload("posters", b"x").duplicate is False


def test_upload_tags_string_and_empty(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, file_json())
    server.reply(201, file_json())
    hl.files.upload("p", b"x", tags="a,b")
    assert server.last.query["tags"] == ["a,b"]
    hl.files.upload("p", b"x", tags=[])
    assert "tags" not in server.last.query


def test_upload_tag_with_space_encoded(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, file_json())
    hl.files.upload("p", b"x", name="my file&x=1.jpg", tags=["big cat", "a+b"])
    assert server.last.query == {"name": ["my file&x=1.jpg"], "tags": ["big cat,a+b"]}


@pytest.mark.parametrize("as_path", [str, Path])
def test_upload_path_streams_with_content_length(server: FakeServer, hl: Homelab, tmp_path: Path, as_path: Any) -> None:
    payload = bytes(range(256)) * 4096  # 1 MiB
    p = tmp_path / "big photo.bin"
    p.write_bytes(payload)
    server.reply(201, file_json())
    hl.files.upload("posters", as_path(p))
    assert server.last.headers["content-length"] == str(len(payload))
    assert "transfer-encoding" not in server.last.headers
    assert hashlib.sha256(server.last.body).digest() == hashlib.sha256(payload).digest()
    assert server.last.query["name"] == ["big photo.bin"]  # defaults to the file name


def test_upload_path_explicit_name_wins(server: FakeServer, hl: Homelab, tmp_path: Path) -> None:
    p = tmp_path / "a.txt"
    p.write_bytes(b"hi")
    server.reply(201, file_json())
    hl.files.upload("p", p, name="other.txt")
    assert server.last.query["name"] == ["other.txt"]


def test_upload_missing_path(hl: Homelab, tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError):
        hl.files.upload("p", tmp_path / "nope")


class CountingReader(io.RawIOBase):
    """Binary reader that records how much is read per call (and cannot seek)."""

    def __init__(self, data: bytes) -> None:
        self._b = io.BytesIO(data)
        self.reads: List[int] = []

    def readable(self) -> bool:
        return True

    def seekable(self) -> bool:
        return False

    def readinto(self, buf: Any) -> int:
        n = self._b.readinto(buf)
        self.reads.append(n)
        return n


def test_upload_file_object_seekable(server: FakeServer, hl: Homelab) -> None:
    f = io.BytesIO(b"0123456789")
    f.seek(4)  # only what is left is sent
    server.reply(201, file_json())
    hl.files.upload("p", f, name="x")
    assert server.last.body == b"456789" and server.last.headers["content-length"] == "6"


def test_upload_file_object_not_seekable_is_chunked_and_streamed(server: FakeServer, hl: Homelab) -> None:
    data = b"z" * (600 * 1024)
    reader = CountingReader(data)
    server.reply(201, file_json())
    hl.files.upload("p", reader, name="x")  # type: ignore[arg-type]
    assert server.last.body == data
    assert server.last.headers["transfer-encoding"] == "chunked"
    assert max(reader.reads) < len(data)  # never read in one gulp


def test_upload_text_file_rejected(hl: Homelab) -> None:
    with pytest.raises(TypeError):
        hl.files.upload("p", io.StringIO("text"))  # type: ignore[arg-type]
    with pytest.raises(TypeError):
        hl.files.upload("p", 123)  # type: ignore[arg-type]


def test_upload_errors(server: FakeServer, hl: Homelab) -> None:
    server.reply(400, {"error": "sha256 does not match"})
    with pytest.raises(HomelabError) as e:
        hl.files.upload("p", b"x", sha256="00" * 32)
    assert (e.value.status, e.value.message) == (400, "sha256 does not match")


def test_folder_and_id_encoding(server: FakeServer, hl: Homelab) -> None:
    for _ in range(5):
        server.reply(200, file_json())
    server.reply(204)
    hl.files.upload("a b/c", b"x")
    assert server.last.raw_path == "/api/files/a%20b%2Fc"
    hl.files.list("a b/c")
    assert server.last.raw_path == "/api/files/a%20b%2Fc"
    hl.files.info("f/o", "i d/../x")
    assert server.last.raw_path == "/api/files/f%2Fo/i%20d%2F..%2Fx/info"
    hl.files.update("f?", "i#", tags=[])
    assert server.last.raw_path == "/api/files/f%3F/i%23/info"
    server.reply(200, body=b"x")
    hl.files.download("f/o", "i%d")
    assert server.last.raw_path == "/api/files/f%2Fo/i%25d"
    hl.files.delete("f/o", "i/d")
    assert server.last.raw_path == "/api/files/f%2Fo/i%2Fd"


# -- list / all -----------------------------------------------------------

def test_list_plain(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [file_json(), file_json(id="B" * 26, future=True)], "next": ULID})
    page = hl.files.list("posters")
    assert (server.last.method, server.last.raw_path, server.last.raw_query) == ("GET", "/api/files/posters", "")
    assert [f.id for f in page.files] == [ULID, "B" * 26] and page.next == ULID
    assert page.files[1].raw["future"] is True
    files, nxt = page
    assert nxt == ULID and len(files) == 2


def test_list_last_page_next_none(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [], "next": None})
    page = hl.files.list("posters")
    assert page.files == [] and page.next is None


def test_list_all_filters(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [], "next": None})
    hl.files.list("posters", tag="dune", type="image/", taken_after="2024-05-01",
                  taken_before="2024-05-01T10:00:00+02:00", camera="canon eos", after=ULID, limit=25)
    assert server.last.query == {
        "tag": ["dune"], "type": ["image/"], "taken_after": ["2024-05-01"],
        "taken_before": ["2024-05-01T10:00:00+02:00"], "camera": ["canon eos"],
        "after": [ULID], "limit": ["25"],
    }
    assert "+02:00" not in server.last.raw_query  # plus sign is percent-encoded


def test_list_date_objects(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [], "next": None})
    hl.files.list("p", taken_after=datetime.date(2024, 5, 1),
                  taken_before=datetime.datetime(2024, 6, 1, 8, 30, tzinfo=datetime.timezone.utc))
    assert server.last.query["taken_after"] == ["2024-05-01"]
    assert server.last.query["taken_before"] == ["2024-06-01T08:30:00+00:00"]


def test_all_follows_next(server: FakeServer, hl: Homelab) -> None:
    ids = ["C" * 26, "B" * 26, "A" * 26]
    server.reply(200, {"files": [file_json(id=ids[0]), file_json(id=ids[1])], "next": ids[1]})
    server.reply(200, {"files": [file_json(id=ids[2])], "next": None})
    out = list(hl.files.all("posters", tag="dune", limit=2))
    assert [f.id for f in out] == ids
    assert all(isinstance(f, File) for f in out)
    assert len(server.requests) == 2
    assert server.requests[0].query == {"tag": ["dune"], "limit": ["2"]}
    assert server.requests[1].query == {"tag": ["dune"], "limit": ["2"], "after": [ids[1]]}


def test_all_is_lazy(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [file_json()], "next": "B" * 26})
    it = hl.files.all("posters")
    assert server.requests == []
    assert next(it).id == ULID
    assert len(server.requests) == 1  # second page not fetched yet


def test_all_empty_and_start_after(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [], "next": None})
    assert list(hl.files.all("posters", after=ULID)) == []
    assert server.last.query == {"after": [ULID]}


def test_all_stops_if_server_repeats_cursor(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"files": [file_json()], "next": "X" * 26})
    server.reply(200, {"files": [file_json()], "next": "X" * 26})
    assert len(list(hl.files.all("posters"))) == 2


# -- info / update / delete ----------------------------------------------

def test_info_parses_everything(server: FakeServer, hl: Homelab) -> None:
    meta = {"width": 4000, "height": 3000, "taken_at": "2023-07-09T20:36:33", "camera_make": "Canon",
            "camera_model": "EOS R5", "lens": "RF50", "orientation": 6,
            "gps": {"lat": 1.5, "lon": -2.5, "alt": 10}, "duration_s": 3, "title": "T", "artist": "A",
            "album": "Al", "year": 1999, "pages": 2, "author": "Au", "new_key": [1]}
    server.reply(200, file_json(meta=meta, unknown_top="y"))
    f = hl.files.info("posters", ULID)
    assert (server.last.method, server.last.raw_path) == ("GET", f"/api/files/posters/{ULID}/info")
    assert (f.name, f.content_type, f.size, f.created_by, f.tags, f.info) == (
        "poster.jpg", "image/jpeg", 5, "app:movies/prod", ["dune"], {"k": 1})
    m = f.meta
    assert (m.width, m.height, m.taken_at, m.camera_make, m.camera_model, m.lens, m.orientation) == (
        4000, 3000, "2023-07-09T20:36:33", "Canon", "EOS R5", "RF50", 6)
    assert m.gps is not None and (m.gps.lat, m.gps.lon, m.gps.alt) == (1.5, -2.5, 10.0)
    assert (m.duration_s, m.title, m.artist, m.album, m.year, m.pages, m.author) == (3.0, "T", "A", "Al", 1999, 2, "Au")
    assert m.raw["new_key"] == [1] and f.raw["unknown_top"] == "y"


def test_info_empty_meta(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, file_json(meta={}))
    m = hl.files.info("p", ULID).meta
    assert m.width is None and m.gps is None and m.raw == {}


def test_info_404(server: FakeServer, hl: Homelab) -> None:
    server.reply(404, {"error": "No such file."})
    with pytest.raises(HomelabError) as e:
        hl.files.info("p", ULID)
    assert e.value.status == 404


def test_update_both(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, file_json(tags=["x"], info={"a": 1}))
    f = hl.files.update("posters", ULID, tags=["x"], info={"a": 1})
    r = server.last
    assert (r.method, r.raw_path) == ("PATCH", f"/api/files/posters/{ULID}/info")
    assert r.headers["content-type"] == "application/json"
    assert json.loads(r.body) == {"tags": ["x"], "info": {"a": 1}}
    assert f.tags == ["x"]


def test_update_leaves_out_what_is_not_given(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, file_json())
    server.reply(200, file_json())
    server.reply(200, file_json())
    hl.files.update("p", ULID, tags=("a", "b"))
    assert json.loads(server.last.body) == {"tags": ["a", "b"]}
    hl.files.update("p", ULID, info={})
    assert json.loads(server.last.body) == {"info": {}}
    hl.files.update("p", ULID, tags=[])  # empty list replaces, it is not "left out"
    assert json.loads(server.last.body) == {"tags": []}


def test_delete(server: FakeServer, hl: Homelab) -> None:
    server.reply(204)
    assert hl.files.delete("posters", ULID) is None
    assert (server.last.method, server.last.raw_path) == ("DELETE", f"/api/files/posters/{ULID}")


def test_delete_403(server: FakeServer, hl: Homelab) -> None:
    server.reply(403, {"error": "Read only."})
    with pytest.raises(HomelabError) as e:
        hl.files.delete("posters", ULID)
    assert e.value.status == 403


# -- download -------------------------------------------------------------

def test_download(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, body=b"\x89PNG\r\n", headers={"Content-Type": "image/png"})
    assert hl.files.download("posters", ULID) == b"\x89PNG\r\n"
    assert (server.last.method, server.last.raw_path) == ("GET", f"/api/files/posters/{ULID}")
    assert "range" not in server.last.headers


def test_download_range(server: FakeServer, hl: Homelab) -> None:
    server.reply(206, body=b"ab", headers={"Content-Range": "bytes 0-1/10"})
    assert hl.files.download("posters", ULID, range="bytes=0-1") == b"ab"
    assert server.last.headers["range"] == "bytes=0-1"


def test_download_416(server: FakeServer, hl: Homelab) -> None:
    server.reply(416, {"error": "Bad range."}, headers={"Content-Range": "bytes */10"})
    with pytest.raises(HomelabError) as e:
        hl.files.download("posters", ULID, range="bytes=99-")
    assert e.value.status == 416


def test_download_to_streams_file(server: FakeServer, hl: Homelab, tmp_path: Path) -> None:
    payload = bytes(range(256)) * 12000  # ~3 MiB, more than one chunk
    server.reply(200, body=payload)
    out = hl.files.download_to("posters", ULID, tmp_path / "out.bin")
    assert out == tmp_path / "out.bin"
    assert out.read_bytes() == payload
    assert server.last.raw_path == f"/api/files/posters/{ULID}"


def test_download_to_accepts_str_and_range(server: FakeServer, hl: Homelab, tmp_path: Path) -> None:
    server.reply(206, body=b"xy")
    hl.files.download_to("p", ULID, str(tmp_path / "r.bin"), range="bytes=2-3")
    assert (tmp_path / "r.bin").read_bytes() == b"xy"
    assert server.last.headers["range"] == "bytes=2-3"
