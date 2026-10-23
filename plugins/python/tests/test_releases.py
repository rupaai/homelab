from __future__ import annotations

import pytest

from homelab import Homelab, HomelabError, Release, version
from conftest import TOKEN, FakeServer

ONE = {"tag": "v0.4.0", "prerelease": False, "commit": "9f3c2ab0", "date": "2026-10-01T10:00:00Z",
       "summary": "2 new, 1 fix", "notes": "## New\n\n- a\n", "x": 1}


def test_list(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"releases": [ONE, {**ONE, "tag": "v0.4.0-dev.2", "prerelease": True}]})
    out = hl.releases.list()
    assert [r.tag for r in out] == ["v0.4.0", "v0.4.0-dev.2"]
    assert out[0] == Release("v0.4.0", False, "9f3c2ab0", "2026-10-01T10:00:00Z", "2 new, 1 fix", "## New\n\n- a\n")
    assert out[1].prerelease is True
    assert out[0].raw["x"] == 1
    assert (server.last.method, server.last.raw_path, server.last.raw_query) == ("GET", "/api/releases", "")
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"


def test_list_with_a_limit(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"releases": []})
    assert hl.releases.list(limit=5) == []
    assert server.last.query == {"limit": ["5"]}


def test_a_bad_limit_is_never_sent(server: FakeServer, hl: Homelab) -> None:
    for limit in (0, -1, True, 1.5):
        with pytest.raises(HomelabError) as e:
            hl.releases.list(limit=limit)  # type: ignore[arg-type]
        assert e.value.status == 0
    assert server.requests == []


def test_errors_are_homelab_errors(server: FakeServer, hl: Homelab) -> None:
    server.reply(403, {"error": "This key can't do that. It needs the releases:read scope."})
    with pytest.raises(HomelabError) as e:
        hl.releases.list()
    assert e.value.status == 403
    assert "releases:read" in str(e.value)
    assert TOKEN not in repr(e.value)


def test_an_odd_answer_is_an_error(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"nope": 1})
    with pytest.raises(HomelabError):
        hl.releases.list()


def test_version_is_homelab_version(monkeypatch: pytest.MonkeyPatch, hl: Homelab) -> None:
    monkeypatch.setenv("HOMELAB_VERSION", "v0.4.0-dev.2")
    assert hl.version() == "v0.4.0-dev.2"
    assert version() == "v0.4.0-dev.2"


def test_version_is_none_when_missing_or_empty(monkeypatch: pytest.MonkeyPatch, hl: Homelab) -> None:
    monkeypatch.delenv("HOMELAB_VERSION", raising=False)
    assert hl.version() is None
    monkeypatch.setenv("HOMELAB_VERSION", "  ")
    assert version() is None
