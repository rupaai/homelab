"""The one error type raised by the SDK."""

from __future__ import annotations


class HomelabError(Exception):
    """A failed call.

    ``status`` is the HTTP status, or ``0`` when there was no HTTP answer (network
    error, timeout, missing configuration). ``message`` is the server's
    ``{"error": "..."}`` text, or a plain fallback.
    """

    def __init__(self, status: int, message: str) -> None:
        super().__init__(status, message)
        self.status = status
        self.message = message

    def __str__(self) -> str:
        if self.status:
            return f"{self.message} (HTTP {self.status})"
        return self.message

    def __repr__(self) -> str:
        return f"HomelabError(status={self.status!r}, message={self.message!r})"
