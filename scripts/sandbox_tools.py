"""Verified host cache of pinned tool artifacts, reused across rollouts."""

from pathlib import Path
import sys
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
    name; a corrupt entry is replaced. Nothing is downloaded for a hit."""
    directory = cache / item["sha256"]
    target = directory / filename(item)
    if target.exists():
        if sha256(target) == item["sha256"]:
            return target
        target.unlink()
    directory.mkdir(parents=True, exist_ok=True)
    partial = target.with_name(target.name + ".partial")
    partial.unlink(missing_ok=True)
    obtain(item["url"], partial)
    if sha256(partial) != item["sha256"]:
        partial.unlink()
        raise ValueError(f"SHA256 mismatch for {item['name']} {item['version']}; nothing was cached")
    partial.replace(target)
    return target
