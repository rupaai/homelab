from __future__ import annotations

import json

import pytest

from homelab import Folder, Homelab, HomelabError
from conftest import FakeServer, folder_json


def test_folders(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"folders": [folder_json(files=3, bytes=99, future="x"), folder_json(name="b")]})
    out = hl.files.folders()
    assert (server.last.method, server.last.raw_path) == ("GET", "/api/files/folders")
    assert server.last.body == b""
    assert [f.name for f in out] == ["posters", "b"]
    f = out[0]
    assert isinstance(f, Folder)
    assert (f.disk, f.app, f.env, f.files, f.bytes, f.created_at) == ("main", "movies", "prod", 3, 99, 1700000000)
    assert f.raw["future"] == "x"  # unknown field kept


def test_folder_tolerates_missing_and_odd_fields(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"folders": [{"name": "x", "files": "many"}, "junk"], "extra": 1})
    (f,) = hl.files.folders()
    assert f.name == "x" and f.files == 0 and f.disk == ""


def test_create_folder(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, folder_json())
    f = hl.files.create_folder("posters")
    r = server.last
    assert (r.method, r.raw_path) == ("POST", "/api/files/folders")
    assert r.headers["content-type"] == "application/json"
    assert json.loads(r.body) == {"name": "posters"}
    assert f.name == "posters"


def test_create_folder_with_disk(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, folder_json(disk="big"))
    hl.files.create_folder("posters", disk="big")
    assert json.loads(server.last.body) == {"name": "posters", "disk": "big"}


def test_create_folder_409_raises(server: FakeServer, hl: Homelab) -> None:
    server.reply(409, {"error": "That folder exists."})
    with pytest.raises(HomelabError) as e:
        hl.files.create_folder("posters")
    assert e.value.status == 409 and e.value.message == "That folder exists."


def test_ensure_folder_creates(server: FakeServer, hl: Homelab) -> None:
    server.reply(201, folder_json())
    assert hl.files.ensure_folder("posters").name == "posters"
    assert len(server.requests) == 1


def test_ensure_folder_409_returns_existing(server: FakeServer, hl: Homelab) -> None:
    server.reply(409, {"error": "exists"})
    server.reply(200, {"folders": [folder_json(name="other"), folder_json(files=7)]})
    f = hl.files.ensure_folder("posters", disk="main")
    assert [(r.method, r.raw_path) for r in server.requests] == [
        ("POST", "/api/files/folders"), ("GET", "/api/files/folders")]
    assert json.loads(server.requests[0].body) == {"name": "posters", "disk": "main"}
    assert f.files == 7


def test_ensure_folder_409_but_not_listed_reraises(server: FakeServer, hl: Homelab) -> None:
    server.reply(409, {"error": "exists"})
    server.reply(200, {"folders": []})
    with pytest.raises(HomelabError) as e:
        hl.files.ensure_folder("posters")
    assert e.value.status == 409


def test_ensure_folder_other_errors_propagate(server: FakeServer, hl: Homelab) -> None:
    server.reply(403, {"error": "no"})
    with pytest.raises(HomelabError) as e:
        hl.files.ensure_folder("posters")
    assert e.value.status == 403 and len(server.requests) == 1


def test_delete_folder(server: FakeServer, hl: Homelab) -> None:
    server.reply(204)
    assert hl.files.delete_folder("posters") is None
    assert (server.last.method, server.last.raw_path) == ("DELETE", "/api/files/folders/posters")


def test_delete_folder_409(server: FakeServer, hl: Homelab) -> None:
    server.reply(409, {"error": "Folder still has files."})
    with pytest.raises(HomelabError, match="still has files"):
        hl.files.delete_folder("posters")


def test_folder_name_encoded(server: FakeServer, hl: Homelab) -> None:
    server.reply(204)
    hl.files.delete_folder("a b/c?d#e")
    assert server.last.raw_path == "/api/files/folders/a%20b%2Fc%3Fd%23e"
