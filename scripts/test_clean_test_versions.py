#!/usr/bin/env python3
"""Tests for clean_test_versions.py. Script style: run it, it exits 1 if anything fails."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from clean_test_versions import stale_tags  # noqa: E402

failures = []


def check(name, ok, detail=""):
    print(("ok   " if ok else "FAIL ") + name + ("" if ok else f"  {detail}"))
    if not ok:
        failures.append(name)


got = stale_tags(["pr-29-9e3cc12"], set())
check("closed PR's tag is deleted", got == ["pr-29-9e3cc12"], got)
got = stale_tags(["pr-29-9e3cc12"], {29})
check("open PR's tag is kept", got == [], got)
got = stale_tags(["v0.25.0-pr.7.abc1234", "v0.25.0-pr.8.abc1234"], {8})
check("dotted format works", got == ["v0.25.0-pr.7.abc1234"], got)
got = stale_tags(["pr-1-aaa", "v1.2.3-pr.2.bbb"], set())
check("both formats deleted", got == ["pr-1-aaa", "v1.2.3-pr.2.bbb"], got)
got = stale_tags(["v0.24.0", "build-26", "tv-app", "v0.1.0-rc.1"], set())
check("release tags are never deleted", got == [], got)
got = stale_tags(["pr-x-abc", "v1.2-pr.3.abc", "pr-0-abc", "pr-5-", "v1.2.3-pr.4", "v1.2.3-pr.4.a.b"], set())
check("odd tags are ignored", got == [], got)
got = stale_tags(["pr-5-abc", "pr-6-abc"], {5, 6}, closed=[5])
check("--closed overrides an open list", got == ["pr-5-abc"], got)

if failures:
    sys.exit(1)
