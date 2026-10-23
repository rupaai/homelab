from __future__ import annotations

import json

import pytest

from homelab import DbInfo, DbQueryResult, DbRows, DbTable, Homelab, HomelabError
from conftest import TOKEN, FakeServer

INFO = {"app": "shop", "env": "prod", "engine": "postgres", "status": "up",
        "size_bytes": 10, "connections": 2, "tables": 3, "x": 1}


def test_list(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"databases": [INFO]})
    out = hl.db.list()
    assert out == [DbInfo("shop", "prod", "postgres", "up", 10, 2, 3)]
    assert out[0].raw["x"] == 1
    assert (server.last.method, server.last.raw_path) == ("GET", "/api/db")
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"


def test_info(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, INFO)
    assert hl.db.info("shop", "prod").engine == "postgres"
    assert (server.last.method, server.last.raw_path) == ("GET", "/api/db/shop/prod")


def test_tables(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"tables": [{"schema": "public", "name": "orders", "rows": 7}]})
    assert hl.db.tables("shop", "prod") == [DbTable("public", "orders", 7)]
    assert server.last.raw_path == "/api/db/shop/prod/tables"


def test_rows_with_options(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"columns": ["id"], "rows": [[1], [2]], "truncated": True})
    out = hl.db.rows("my table", app="shop", env="prod", schema="app", limit=20, offset=40)
    assert out == DbRows(["id"], [[1], [2]], True)
    assert server.last.raw_path == "/api/db/shop/prod/tables/my%20table/rows"
    assert server.last.query == {"schema": ["app"], "limit": ["20"], "offset": ["40"]}


def test_rows_minimal_has_no_query(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"columns": [], "rows": [], "truncated": False})
    hl.db.rows("orders", app="shop", env="prod")
    assert server.last.raw_query == ""


def test_rows_bad_limit_or_offset_never_sent(server: FakeServer, hl: Homelab) -> None:
    for kw in ({"limit": 0}, {"limit": 201}, {"offset": -1}, {"limit": True}):
        with pytest.raises(HomelabError) as e:
            hl.db.rows("t", app="shop", env="prod", **kw)  # type: ignore[arg-type]
        assert e.value.status == 0
    assert server.requests == []


def test_query_sends_only_sql(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"columns": ["n"], "rows": [[1]], "row_count": 1, "truncated": False, "command": "SELECT", "ms": 3})
    out = hl.db.query("select 1 as n", app="shop", env="prod")
    assert out == DbQueryResult(["n"], [[1]], False, 1, "SELECT", 3)
    assert (server.last.method, server.last.raw_path) == ("POST", "/api/db/shop/prod/query")
    assert server.last.headers["content-type"] == "application/json"
    assert json.loads(server.last.body) == {"sql": "select 1 as n"}
    assert b"write" not in server.last.body


def test_defaults_from_environment(server: FakeServer, hl: Homelab, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_APP", "blog")
    monkeypatch.setenv("HOMELAB_ENV", "dev")
    server.reply(200, {"tables": []})
    hl.db.tables()
    assert server.last.raw_path == "/api/db/blog/dev/tables"
    server.reply(200, {"columns": [], "rows": [], "truncated": False})
    hl.db.rows("posts")
    assert server.last.raw_path == "/api/db/blog/dev/tables/posts/rows"
    server.reply(200, {})
    hl.db.query("select 1")
    assert server.last.raw_path == "/api/db/blog/dev/query"


def test_explicit_wins_over_environment(server: FakeServer, hl: Homelab, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("HOMELAB_APP", "blog")
    monkeypatch.setenv("HOMELAB_ENV", "dev")
    server.reply(200, INFO)
    hl.db.info("shop", "prod")
    assert server.last.raw_path == "/api/db/shop/prod"


def test_missing_app_or_env(server: FakeServer, hl: Homelab, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("HOMELAB_APP", raising=False)
    monkeypatch.delenv("HOMELAB_ENV", raising=False)
    with pytest.raises(HomelabError, match="Set HOMELAB_APP or pass app"):
        hl.db.info()
    monkeypatch.setenv("HOMELAB_APP", "blog")
    with pytest.raises(HomelabError, match="Set HOMELAB_ENV or pass env"):
        hl.db.info()
    assert server.requests == []


def test_dot_segments_refused(server: FakeServer, hl: Homelab) -> None:
    for call in (
        lambda: hl.db.info(".", "prod"),
        lambda: hl.db.tables("shop", ".."),
        lambda: hl.db.rows("..", app="shop", env="prod"),
        lambda: hl.db.query("select 1", app="..", env="prod"),
    ):
        with pytest.raises(HomelabError) as e:
            call()
        assert e.value.status == 0
    assert server.requests == []


def test_errors_map(server: FakeServer, hl: Homelab) -> None:
    server.reply(404, {"error": "There's no such database."})
    with pytest.raises(HomelabError) as e:
        hl.db.info("x", "dev")
    assert (e.value.status, e.value.message) == (404, "There's no such database.")
    server.reply(403, {"error": "This key can't do that. It needs the db:read scope."})
    with pytest.raises(HomelabError) as e2:
        hl.db.list()
    assert e2.value.status == 403


def test_token_not_in_error(server: FakeServer, hl: Homelab) -> None:
    server.reply(401, {"error": "nope"})
    with pytest.raises(HomelabError) as e:
        hl.db.list()
    assert TOKEN not in str(e.value) and TOKEN not in repr(hl.db)
