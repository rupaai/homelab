from __future__ import annotations

import pytest

from homelab import Homelab, HomelabError
from conftest import ULID, TOKEN, FakeServer, file_json


def test_missing_url(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_TOKEN", TOKEN)
    with pytest.raises(HomelabError) as e:
        Homelab()
    assert e.value.status == 0
    assert "Set HOMELAB_API_URL or pass url" in e.value.message


def test_missing_token(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_API_URL", "http://x")
    with pytest.raises(HomelabError, match="HOMELAB_TOKEN"):
        Homelab()


def test_empty_env_counts_as_missing(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_API_URL", "")
    monkeypatch.setenv("HOMELAB_TOKEN", TOKEN)
    with pytest.raises(HomelabError):
        Homelab()


def test_reads_env(server: FakeServer, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_API_URL", server.url)
    monkeypatch.setenv("HOMELAB_TOKEN", TOKEN + "\n")  # trailing newline is trimmed
    server.reply(200, {"folders": []})
    assert Homelab().files.folders() == []
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"


def test_explicit_beats_env(server: FakeServer, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_API_URL", "http://nowhere.invalid")
    monkeypatch.setenv("HOMELAB_TOKEN", "env-token")
    server.reply(200, {"folders": []})
    Homelab(server.url, "explicit").files.folders()
    assert server.last.headers["authorization"] == "Bearer explicit"


def test_trailing_slash_in_url(server: FakeServer) -> None:
    server.reply(200, {"folders": []})
    Homelab(server.url + "/", TOKEN).files.folders()
    assert server.last.raw_path == "/api/files/folders"


def test_bad_scheme_and_bad_token_do_not_leak() -> None:
    with pytest.raises(HomelabError, match="http"):
        Homelab("ftp://x", TOKEN)
    with pytest.raises(HomelabError) as e:
        Homelab("http://x", "bad token\r\nX: y")
    assert "bad token" not in str(e.value)


def test_every_request_sends_bearer_and_user_agent(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"folders": []})
    server.reply(200, file_json())
    hl.files.folders()
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"
    assert server.last.headers["user-agent"].startswith("homelab-sdk-python/")
    hl.files.info("posters", ULID)
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"


def test_token_not_in_repr_or_str(server: FakeServer, hl: Homelab) -> None:
    for obj in (hl, hl.files, hl._transport, vars(hl)):
        assert TOKEN not in repr(obj) and TOKEN not in str(obj)
    server.reply(401, {"error": "Bad key."})
    with pytest.raises(HomelabError) as e:
        hl.files.folders()
    for text in (str(e.value), repr(e.value), repr(e.value.args)):
        assert TOKEN not in text
    assert e.value.__cause__ is None


def test_userinfo_in_url_not_in_repr() -> None:
    assert "secret" not in repr(Homelab("http://user:secret@host:1/", "t"))
