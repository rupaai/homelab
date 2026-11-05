#!/usr/bin/env python3
"""Delete the test versions (pre-releases) of pull requests that are not open any more.

Needs the `gh` CLI and GITHUB_REPOSITORY. Python 3, no packages.
  python3 scripts/clean_test_versions.py [--dry-run] [--closed PR]
--closed PR: this pull request counts as closed even if the list still shows it open.
Test version tags look like pr-<N>-<commit> or vX.Y.Z-pr.<N>.<commit>
(see testPR in internal/adapters/selfupdate/version.go)."""
import argparse
import json
import os
import re
import subprocess
import sys

TEST_TAG = re.compile(r"^(?:pr-(\d+)-[^-\s]+|v\d+\.\d+\.\d+-pr\.(\d+)\.[^.\s]+)$")


def pr_of(tag):
    """The pull request number of a test version tag, or None."""
    m = TEST_TAG.match(tag)
    if not m:
        return None
    n = int(m.group(1) or m.group(2))
    return n if n > 0 else None


def stale_tags(tags, open_prs, closed=()):
    """The test version tags whose pull request is not open."""
    keep = set(open_prs) - set(closed)
    out = []
    for tag in tags:
        n = pr_of(tag)
        if n is not None and n not in keep:
            out.append(tag)
    return out


def gh_json(*args):
    return json.loads(subprocess.run(["gh", *args], check=True, capture_output=True, text=True).stdout)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--dry-run", action="store_true", help="only print what would be deleted")
    ap.add_argument("--closed", type=int, action="append", default=[], help="a PR number to treat as closed")
    args = ap.parse_args(argv)
    repo = os.environ.get("GITHUB_REPOSITORY")
    if not repo:
        print("GITHUB_REPOSITORY is not set", file=sys.stderr)
        return 2
    releases = gh_json("release", "list", "-R", repo, "--limit", "200", "--json", "tagName,isPrerelease")
    prs = gh_json("pr", "list", "-R", repo, "--state", "open", "--limit", "200", "--json", "number")
    tags = [r["tagName"] for r in releases if r.get("isPrerelease")]
    doomed = stale_tags(tags, {p["number"] for p in prs}, args.closed)
    if not doomed:
        print("No old test versions.")
        return 0
    failed = 0
    for tag in doomed:
        if args.dry_run:
            print(f"would delete {tag}")
            continue
        r = subprocess.run(["gh", "release", "delete", tag, "-R", repo, "--cleanup-tag", "--yes"],
                           capture_output=True, text=True)
        if r.returncode:
            failed += 1
            print(f"could not delete {tag}: {r.stderr.strip()}", file=sys.stderr)
        else:
            print(f"deleted {tag}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
