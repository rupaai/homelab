from __future__ import annotations

import pickle
import socket

import pytest

from homelab import Homelab, HomelabError
from conftest import TOKEN, FakeServer


@pytest.mark.parametrize("status", [400, 401, 403, 404, 409, 416, 500, 503])
def test_json_error_mapped(server: FakeServer, hl: Homelab, status: int) -> None:
    server.reply(status, {"error": "A sentence."})
    with pytest.raises(HomelabError) as e:
        hl.files.folders()
    assert e.value.status == status
    assert e.value.message == "A sentence."
    assert str(e.value) == f"A sentence. (HTTP {status})"


def test_non_json_413_falls_back(server: FakeServer, hl: Homelab) -> None:
    server.reply(413, body=b"<html>too big</html>", headers={"Content-Type": "text/html"})
    with pytest.raises(HomelabError) as e:
        hl.files.upload("p", b"x")
    assert e.value.status == 413
    assert e.value.message.startswith("HTTP 413")


def test_empty_error_body_falls_back(server: FakeServer, hl: Homelab) -> None:
    server.reply(502)
    with pytest.raises(HomelabError) as e:
        hl.files.folders()
    assert e.value.status == 502 and "502" in e.value.message


def test_json_without_error_key_falls_back(server: FakeServer, hl: Homelab) -> None:
    server.reply(500, {"oops": 1})
    with pytest.raises(HomelabError) as e:
        hl.files.folders()
    assert e.value.message.startswith("HTTP 500")


def test_download_error_is_homelab_error(server: FakeServer, hl: Homelab, tmp_path) -> None:  # type: ignore[no-untyped-def]
    server.reply(404, {"error": "No such file."})
    with pytest.raises(HomelabError) as e:
        hl.files.download_to("p", "id", tmp_path / "out.bin")
    assert e.value.status == 404
    assert not (tmp_path / "out.bin").exists()


def test_network_error_status_zero() -> None:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()  # nothing listens here now
    with pytest.raises(HomelabError) as e:
        Homelab(f"http://127.0.0.1:{port}", TOKEN).files.folders()
    assert e.value.status == 0
    assert TOKEN not in str(e.value)


def test_timeout_status_zero(server: FakeServer) -> None:
    server.reply(200, {"folders": []}, delay=1.0)
    with pytest.raises(HomelabError) as e:
        Homelab(server.url, TOKEN, timeout=0.2).files.folders()
    assert e.value.status == 0


def test_redirect_not_followed(server: FakeServer, hl: Homelab) -> None:
    server.reply(302, headers={"Location": "http://example.invalid/steal"})
    with pytest.raises(HomelabError) as e:
        hl.files.folders()
    assert e.value.status == 302
    assert len(server.requests) == 1


def test_success_body_not_json(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, body=b"<html>")
    with pytest.raises(HomelabError):
        hl.files.folders()


def test_success_body_wrong_shape(server: FakeServer, hl: Homelab) -> None:
    server.reply(200, json=[1, 2])
    with pytest.raises(HomelabError):
        hl.files.info("p", "id")


def test_error_pickles_and_repr() -> None:
    e = pickle.loads(pickle.dumps(HomelabError(404, "nope")))
    assert (e.status, e.message) == (404, "nope")
    assert repr(e) == "HomelabError(status=404, message='nope')"
