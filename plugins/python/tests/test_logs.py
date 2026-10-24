from __future__ import annotations

import datetime
import io
import json
import logging
import sys
from typing import Any, Dict, Iterator, List

import pytest

import homelab
from homelab import Homelab, HomelabError, LogLine, LogSearchResult
from conftest import TOKEN, FakeServer

LINE = {"id": 42, "time": "2026-10-23T22:00:00.123Z", "level": "error", "msg": "boom", "app": "a", "env": "prod",
        "service": "app", "stream": "stderr", "extra": {"code": 7}, "x": 1}
COUNTS = {"debug": 0, "info": 3, "warn": 1, "error": 2, "fatal": 0, "unknown": 0}


def test_search_parses_the_answer(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"lines": [LINE], "counts": COUNTS, "next": "41"})
    out = hl.logs.search()
    assert isinstance(out, LogSearchResult)
    assert out.lines == [LogLine(42, "2026-10-23T22:00:00.123Z", "error", "boom", "a", "prod", "app", "stderr", {"code": 7})]
    assert out.lines[0].raw["x"] == 1
    assert out.counts["error"] == 2
    assert out.next == "41"
    assert (server.last.method, server.last.raw_path, server.last.raw_query) == ("GET", "/api/logs", "")
    assert server.last.headers["authorization"] == f"Bearer {TOKEN}"


def test_search_sends_every_option(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"lines": [], "counts": COUNTS, "next": None})
    out = hl.logs.search(
        "disk full", ["error", "fatal"], min_level="warn", app="a", env="prod", service="app",
        since=datetime.datetime(2026, 10, 23, 10, 0, tzinfo=datetime.timezone.utc), until=1730000000000,
        before=41, after="7", limit=50,
    )
    assert out.next is None
    assert server.last.query == {
        "q": ["disk full"], "level": ["error,fatal"], "min_level": ["warn"], "app": ["a"], "env": ["prod"],
        "service": ["app"], "since": ["2026-10-23T10:00:00.000Z"], "until": ["1730000000000"],
        "before": ["41"], "after": ["7"], "limit": ["50"],
    }


def test_one_level_and_empty_options(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, {"lines": [], "counts": COUNTS, "next": None})
    hl.logs.search(level="error")
    assert server.last.query == {"level": ["error"]}
    server.reply(200, {"lines": []})
    out = hl.logs.search("  ", [], app="")
    assert server.last.raw_query == ""
    assert out.counts == {"unknown": 0, "debug": 0, "info": 0, "warn": 0, "error": 0, "fatal": 0}


def test_bad_options_are_never_sent(server: FakeServer, hl: Homelab) -> None:
    bad: List[Dict[str, Any]] = [{"limit": 0}, {"limit": 501}, {"limit": True}, {"level": "loud"}, {"min_level": "loud"},
                                 {"before": "abc"}, {"after": -1}, {"since": 1.5}]
    for kw in bad:
        with pytest.raises(HomelabError) as e:
            hl.logs.search(**kw)
        assert e.value.status == 0
    assert server.requests == []


def test_errors_are_homelab_errors(server: FakeServer, hl: Homelab) -> None:
    server.reply(403, {"error": "This key can't do that. It needs the logs:read scope."})
    with pytest.raises(HomelabError) as e:
        hl.logs.search()
    assert e.value.status == 403
    assert "logs:read" in str(e.value)
    assert TOKEN not in repr(e.value)
    server.reply(404, {"error": "Homelab doesn't know that app."})
    with pytest.raises(HomelabError) as e2:
        hl.logs.search(app="other")
    assert e2.value.status == 404


def test_an_odd_answer_is_an_error(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, [1])
    with pytest.raises(HomelabError):
        hl.logs.search()


# --- setup() ---

@pytest.fixture
def root() -> Iterator[logging.Logger]:
    r = logging.getLogger()
    saved, level = list(r.handlers), r.level
    for h in saved:
        if getattr(h, "_homelab_json", False):
            r.removeHandler(h)
    yield r
    for h in list(r.handlers):
        if getattr(h, "_homelab_json", False):
            r.removeHandler(h)
    r.setLevel(level)


def lines(capsys: pytest.CaptureFixture[str]) -> List[Dict[str, Any]]:
    return [json.loads(x) for x in capsys.readouterr().out.splitlines()]


def test_import_homelab_then_logs_setup(root: logging.Logger) -> None:
    assert homelab.logs.setup is not None


def test_setup_prints_one_json_line(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup()
    logging.getLogger("shop.cart").info("added %s", "tea")
    (rec,) = lines(capsys)
    assert rec["msg"] == "added tea"
    assert rec["level"] == "info"
    assert rec["logger"] == "shop.cart"
    assert "exception" not in rec
    assert rec["time"].endswith("Z")
    datetime.datetime.fromisoformat(rec["time"].replace("Z", "+00:00"))


def test_levels_are_mapped(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup("DEBUG")
    log = logging.getLogger("t")
    log.debug("a"); log.info("b"); log.warning("c"); log.error("d"); log.critical("e"); log.log(25, "f")
    assert [r["level"] for r in lines(capsys)] == ["debug", "info", "warn", "error", "fatal", "info"]


def test_setup_level_filters(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup(level="warning")
    logging.getLogger("t").info("quiet")
    logging.getLogger("t").error("loud")
    assert [r["msg"] for r in lines(capsys)] == ["loud"]


def test_extra_fields_are_kept(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup()
    logging.getLogger("t").info("paid", extra={"order": 7, "who": {"name": "Ana"}, "when": datetime.date(2026, 1, 2), "level": "x"})
    (rec,) = lines(capsys)
    assert rec["order"] == 7
    assert rec["who"] == {"name": "Ana"}
    assert rec["when"] == "2026-01-02"
    assert rec["level"] == "info"  # an extra can't change the real level
    assert rec["extra_level"] == "x"


def test_exceptions_are_text(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup()
    try:
        raise ValueError("bad thing")
    except ValueError:
        logging.getLogger("t").exception("it failed")
    (rec,) = lines(capsys)
    assert rec["level"] == "error"
    assert isinstance(rec["exception"], str)
    assert rec["exception"].startswith("Traceback")
    assert "ValueError: bad thing" in rec["exception"]


def test_setup_twice_does_not_double_the_lines(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup("INFO")
    homelab.logs.setup("DEBUG")
    logging.getLogger("t").debug("once")
    assert [r["msg"] for r in lines(capsys)] == ["once"]
    assert len([h for h in root.handlers if getattr(h, "_homelab_json", False)]) == 1


def test_setup_needs_no_token(root: logging.Logger, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]) -> None:
    monkeypatch.delenv("HOMELAB_TOKEN", raising=False)
    monkeypatch.delenv("HOMELAB_API_URL", raising=False)
    homelab.logs.setup()
    logging.getLogger("t").info("fine")
    assert len(lines(capsys)) == 1


def test_odd_values_still_print(root: logging.Logger, capsys: pytest.CaptureFixture[str]) -> None:
    homelab.logs.setup()
    loop: List[Any] = []
    loop.append(loop)
    logging.getLogger("t").info("loop", extra={"loop": loop})
    logging.getLogger("t").info("fine %s", extra={"obj": object()})
    out = lines(capsys)
    assert [r["msg"] for r in out] == ["loop", "fine %s"]
    assert isinstance(out[0]["loop"], str)


def test_output_follows_a_redirect_of_stdout(root: logging.Logger, monkeypatch: pytest.MonkeyPatch) -> None:
    homelab.logs.setup()
    buf = io.StringIO()
    monkeypatch.setattr(sys, "stdout", buf)
    logging.getLogger("t").info("here")
    assert json.loads(buf.getvalue())["msg"] == "here"
