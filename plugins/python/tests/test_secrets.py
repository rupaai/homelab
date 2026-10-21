from __future__ import annotations

import json
import pickle
from pathlib import Path
from typing import Any

import pytest

from homelab import Homelab, HomelabError, SecretInfo, SecretSaved, SecretVersion
from conftest import TOKEN, FakeServer

VALUE = "ya29.super-secret-value"


@pytest.fixture
def sdir(tmp_path: Path) -> Path:
    d = tmp_path / "secrets"
    d.mkdir()
    return d


@pytest.fixture
def hs(server: FakeServer, sdir: Path) -> Homelab:
    return Homelab(server.url, TOKEN, secrets_dir=sdir)


def test_list(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"secrets": [{"name": "a", "kind": "app", "version": 2, "updated_at": 5, "x": 1}]})
    out = hs.secrets.list()
    assert out == [SecretInfo(name="a", kind="app", version=2, updated_at=5)]
    assert out[0].raw["x"] == 1
    assert (server.last.method, server.last.raw_path) == ("GET", "/api/secrets")
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"


def test_get_fresh(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"name": "tok", "value": VALUE, "version": 3})
    assert hs.secrets.get("tok", fresh=True) == VALUE
    assert (server.last.method, server.last.raw_path, server.last.raw_query) == ("GET", "/api/secrets/tok", "")


def test_get_version_asks_homelab(server: FakeServer, hs: Homelab, sdir: Path) -> None:
    (sdir / "tok").write_text("from-file")
    server.reply(200, {"name": "tok", "value": "old", "version": 2})
    assert hs.secrets.get("tok", version=2) == "old"
    assert server.last.raw_path == "/api/secrets/tok"
    assert server.last.query == {"version": ["2"]}


def test_set(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"name": "tok", "version": 4})
    assert hs.secrets.set("tok", VALUE) == SecretSaved(name="tok", version=4)
    r = server.last
    assert (r.method, r.raw_path) == ("PUT", "/api/secrets/tok")
    assert r.headers["content-type"] == "application/json"
    assert json.loads(r.body) == {"value": VALUE}


def test_delete(server: FakeServer, hs: Homelab) -> None:
    server.reply(204)
    assert hs.secrets.delete("tok") is None
    assert (server.last.method, server.last.raw_path) == ("DELETE", "/api/secrets/tok")


def test_versions(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"versions": [{"version": 2, "set_at": 9}, {"version": 1, "set_at": 3}]})
    assert hs.secrets.versions("tok") == [SecretVersion(2, 9), SecretVersion(1, 3)]
    assert server.last.raw_path == "/api/secrets/tok/versions"


def test_path_is_encoded(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"name": "x", "value": "v", "version": 1})
    hs.secrets.get("google_token:user.42-a", fresh=True)
    assert server.last.raw_path == "/api/secrets/google_token%3Auser.42-a"


# -- file first ---------------------------------------------------------------


def test_file_first(server: FakeServer, hs: Homelab, sdir: Path) -> None:
    (sdir / "API_KEY").write_text("from-file")
    assert hs.secrets.get("API_KEY") == "from-file"
    assert server.requests == []


def test_falls_back_to_api(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"name": "API_KEY", "value": "from-api", "version": 1})
    assert hs.secrets.get("API_KEY") == "from-api"
    assert len(server.requests) == 1


def test_fresh_skips_file(server: FakeServer, hs: Homelab, sdir: Path) -> None:
    (sdir / "API_KEY").write_text("from-file")
    server.reply(200, {"name": "API_KEY", "value": "from-api", "version": 2})
    assert hs.secrets.get("API_KEY", fresh=True) == "from-api"


def test_non_identifier_names_never_touch_disk(server: FakeServer, hs: Homelab, sdir: Path) -> None:
    for n in ("a.b", "a:b", "1abc"):
        (sdir / n).write_text("from-file")
        server.reply(200, {"name": n, "value": "from-api", "version": 1})
        assert hs.secrets.get(n) == "from-api"
    assert len(server.requests) == 3


@pytest.mark.parametrize("bad", ["../etc/passwd", "a/b", ".", "..", "", "a b", "x" * 201, "a\\b", "ok\n", "é"])
def test_bad_names_refused_before_anything(server: FakeServer, hs: Homelab, bad: str) -> None:
    for call in (
        lambda: hs.secrets.get(bad),
        lambda: hs.secrets.get_or_none(bad),
        lambda: hs.secrets.set(bad, "v"),
        lambda: hs.secrets.delete(bad),
        lambda: hs.secrets.versions(bad),
    ):
        with pytest.raises(HomelabError) as e:
            call()
        assert e.value.status == 0
    assert server.requests == []


def test_bad_version_refused(server: FakeServer, hs: Homelab) -> None:
    for v in (0, -1, True):
        with pytest.raises(HomelabError):
            hs.secrets.get("a", version=v)
    assert server.requests == []


def test_default_dir_is_run_secrets(server: FakeServer) -> None:
    server.reply(200, {"name": "NOPE_NOT_THERE", "value": "api", "version": 1})
    assert Homelab(server.url, TOKEN).secrets.get("NOPE_NOT_THERE") == "api"


# -- 404 and errors -----------------------------------------------------------


def test_get_404_raises(server: FakeServer, hs: Homelab) -> None:
    server.reply(404, {"error": "There's no such secret."})
    with pytest.raises(HomelabError) as e:
        hs.secrets.get("nope", fresh=True)
    assert (e.value.status, e.value.message) == (404, "There's no such secret.")


def test_get_or_none(server: FakeServer, hs: Homelab) -> None:
    server.reply(404, {"error": "There's no such secret."})
    assert hs.secrets.get_or_none("nope") is None
    server.reply(403, {"error": "This key can't do that."})
    with pytest.raises(HomelabError) as e:
        hs.secrets.get_or_none("nope")
    assert e.value.status == 403


def test_get_or_none_returns_value(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"name": "a", "value": "v", "version": 1})
    assert hs.secrets.get_or_none("a") == "v"


@pytest.mark.parametrize("status", [403, 503])
def test_other_statuses(server: FakeServer, hs: Homelab, status: int) -> None:
    server.reply(status, {"error": "A sentence."})
    with pytest.raises(HomelabError) as e:
        hs.secrets.set("a", "v")
    assert e.value.status == status


# -- no leaks -----------------------------------------------------------------


def test_failed_set_never_has_value_in_error(server: FakeServer, hs: Homelab) -> None:
    server.reply(400, {"error": f"bad value {VALUE}"})
    with pytest.raises(HomelabError) as e:
        hs.secrets.set("a", VALUE)
    for text in (str(e.value), repr(e.value), e.value.message, repr(e.value.args)):
        assert VALUE not in text
        assert TOKEN not in text


def test_reprs_do_not_print_values_or_token(server: FakeServer, hs: Homelab) -> None:
    server.reply(200, {"name": "a", "version": 1})
    saved = hs.secrets.set("a", VALUE)
    server.reply(200, {"secrets": [{"name": "a", "kind": "app", "version": 1, "updated_at": 1}]})
    info = hs.secrets.list()
    for obj in (hs, hs.secrets, saved, info, info[0]):
        for text in (repr(obj), str(obj)):
            assert VALUE not in text and TOKEN not in text


def test_token_not_in_error_or_pickle(server: FakeServer, hs: Homelab) -> None:
    server.reply(401, {"error": "No key."})
    with pytest.raises(HomelabError) as e:
        hs.secrets.list()
    assert TOKEN not in str(e.value) and TOKEN not in repr(e.value)
    assert TOKEN.encode() not in pickle.dumps(e.value)


def test_unreadable_file_error_has_no_contents(server: FakeServer, hs: Homelab, sdir: Path) -> None:
    (sdir / "BIN").write_bytes(b"\xff\xfe" + VALUE.encode())
    with pytest.raises(HomelabError) as e:
        hs.secrets.get("BIN")
    assert VALUE not in str(e.value)


def test_no_redirects(server: FakeServer, hs: Homelab) -> None:
    server.reply(302, headers={"Location": "http://127.0.0.1:1/steal"})
    with pytest.raises(HomelabError) as e:
        hs.secrets.get("a", fresh=True)
    assert e.value.status == 302
    assert len(server.requests) == 1


def test_secrets_is_a_sibling_of_files(hs: Homelab) -> None:
    assert hs.files is not None and hs.secrets is not None
    _: Any = hs.secrets.list  # exists
