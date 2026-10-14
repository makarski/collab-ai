"""Verified host cache of pinned tool artifacts, reused across rollouts."""

import os
from pathlib import Path
import sys
import tempfile
from urllib.parse import urlparse

from sandbox_artifacts import fetch, sha256

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from tool_lock import artifact  # noqa: E402

CACHE = ROOT / "dist/tools"


def resolve(lock, machine, names):
    """The artifacts named, resolved for one machine architecture."""
    return [artifact(lock, name, machine) for name in names]


def filename(item):
    return Path(urlparse(item["url"]).path).name


def cache_artifact(item, cache=CACHE, obtain=fetch):
    """Return the verified local file for item.

    The cache is keyed by digest, so a hit is re-verified and never trusted by
    name; a corrupt entry is replaced. Nothing is downloaded for a hit. Each
    download goes to its own temporary file, so concurrent callers (the cache
    is shared by every deployment on the host) can never publish each other's
    unverified bytes; the verified file is published with one atomic rename."""
    directory = cache / item["sha256"]
    target = directory / filename(item)
    if target.exists():
        if sha256(target) == item["sha256"]:
            return target
        target.unlink(missing_ok=True)
    directory.mkdir(parents=True, exist_ok=True)
    handle, name = tempfile.mkstemp(prefix=".download-", dir=directory)
    partial = Path(name)
    try:
        os.close(handle)
        partial.unlink()  # obtain() creates the file itself; the unique name is what matters.
        obtain(item["url"], partial)
        if sha256(partial) != item["sha256"]:
            raise ValueError(f"SHA256 mismatch for {item['name']} {item['version']}; nothing was cached")
        partial.replace(target)
    finally:
        partial.unlink(missing_ok=True)
    return target
