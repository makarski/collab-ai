"""The pinned Ubuntu base image, from the Incus store, a verified host cache,
our own mirror release, or upstream as the last resort.

Upstream keeps only a few daily builds, so each pinned base image is mirrored
once into a `base-<fingerprint>` release. The fingerprint is the trust root:
sha256 over incus.tar.xz then rootfs.squashfs, as Incus computes it.
"""

from collections import namedtuple
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

from sandbox_releases import api_headers, repository


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_REPO = "makarski/collab-ai"
CACHE = ROOT / "dist/base"
ASSETS = ("incus.tar.xz", "rootfs.squashfs")  # combined digest order

Base = namedtuple("Base", ["ref", "source"])


class IntegrityError(ValueError):
    """Bytes or release contents don't match the pinned fingerprint. Never falls back."""


class Unavailable(ValueError):
    """A source couldn't be asked: network, server, auth or Incus failure. Never falls back."""


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def tag(fp):
    return "base-" + fingerprint(fp)


def fingerprint(value):
    if not re.fullmatch(r"[0-9a-f]{64}", value or ""):
        raise ValueError("Expected a 64-hex base image fingerprint")
    return value


def combined_digest(meta, rootfs):
    digest = hashlib.sha256()
    for path in (meta, rootfs):
        with Path(path).open("rb") as stream:
            while chunk := stream.read(1024 * 1024):
                digest.update(chunk)
    return digest.hexdigest()


def verify_pair(directory, fp):
    if combined_digest(*(Path(directory) / name for name in ASSETS)) != fp:
        raise IntegrityError(f"Base image files in {directory} don't match {fp}")


def in_store(remote, project, fp):
    """Whether the project's image store holds exactly fp. Only a successful,
    well-formed listing can say no."""
    try:
        listing = run(["incus", "--project", project, "image", "list", f"{remote}:", "--format=json"],
                      capture_output=True, text=True).stdout
        images = json.loads(listing)
    except (subprocess.SubprocessError, OSError, ValueError) as error:
        raise Unavailable(f"Cannot list images in {remote}:{project}: {error}") from error
    if not isinstance(images, list) or not all(isinstance(image, dict) and isinstance(image.get("fingerprint"), str)
                                                for image in images):
        raise Unavailable(f"Unexpected image listing from {remote}:{project}")
    return any(image["fingerprint"] == fp for image in images)


def get_object(url):
    """The JSON object at url, or None for HTTP 404; any other reply is fatal."""
    try:
        with urlopen(Request(url, headers=api_headers()), timeout=30) as response:
            value = json.load(response)
    except HTTPError as error:
        if error.code == 404:
            return None
        raise Unavailable(f"{url}: HTTP {error.code}") from error
    except (URLError, OSError, ValueError) as error:
        raise Unavailable(f"{url}: {error}") from error
    if not isinstance(value, dict):
        raise Unavailable(f"{url}: expected a JSON object")
    return value


def release_shape(release, fields):
    """Fail unless release has a boolean draft flag and assets with string fields."""
    assets = release.get("assets")
    if not isinstance(release.get("draft"), bool) or not isinstance(assets, list) or not all(
            isinstance(asset, dict) and all(isinstance(asset.get(field), str) for field in fields) for asset in assets):
        raise Unavailable(f"Unexpected release response for {release.get('tag_name')}")
    return release


def mirror_release(repo, fp):
    """The published mirror release for fp, or None when there is none yet.
    A draft is still being written and is never used."""
    release = get_object(f"https://api.github.com/repos/{repository(repo)}/releases/tags/{quote(tag(fp), safe='')}")
    if release is None:
        return None
    if release.get("tag_name") != tag(fp):
        raise IntegrityError(f"Release lookup for {tag(fp)} returned {release.get('tag_name')}")
    release_shape(release, ("name",))
    if release["draft"]:
        return None
    names = sorted(asset["name"] for asset in release["assets"])
    if names != sorted(ASSETS):
        raise IntegrityError(f"Release {tag(fp)} must hold exactly {', '.join(ASSETS)}; it has {names}")
    return release_shape(release, ("name", "browser_download_url"))


def fetch(url, destination):
    try:
        with urlopen(url, timeout=120) as response, Path(destination).open("xb") as output:
            shutil.copyfileobj(response, output, length=1024 * 1024)
    except HTTPError as error:
        raise Unavailable(f"{url}: HTTP {error.code}") from error
    except (URLError, OSError) as error:
        raise Unavailable(f"{url}: {error}") from error


def asset_urls(release):
    return {asset["name"]: asset["browser_download_url"] for asset in release["assets"]}


def cached_pair(cache_root, fp, release=None):
    """The verified cache directory for fp, downloading it from release on a
    miss; None when it is neither cached nor released."""
    cache_root = Path(cache_root)
    final = cache_root / fp
    if final.exists():
        verify_pair(final, fp)
        return final
    if release is None:
        return None
    urls = asset_urls(release)
    return install_pair(cache_root, fp, lambda staging: [fetch(urls[name], staging / name) for name in ASSETS])


def install_pair(cache_root, fp, populate):
    """Fill a private staging directory with populate, verify it, and publish
    it as the complete cache directory for fp."""
    cache_root = Path(cache_root)
    final = cache_root / fp
    cache_root.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=".staging-", dir=cache_root))
    try:
        populate(staging)
        verify_pair(staging, fp)
        try:
            os.rename(staging, final)
        except OSError:
            if not final.is_dir():
                raise
            # Another process published the pair first; use theirs once verified.
            verify_pair(final, fp)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return final


def import_pair(remote, project, fp, directory):
    run(["incus", "--project", project, "image", "import",
         *(str(Path(directory) / name) for name in ASSETS), f"{remote}:"])
    if not in_store(remote, project, fp):
        raise IntegrityError(f"Importing the base image into {remote}:{project} didn't produce {fp}")


def ensure_base(remote, project, fp, repo=DEFAULT_REPO, cache_root=CACHE):
    """The image reference to create an instance from, and where it came from.
    Only confirmed absence moves on to the next source."""
    fingerprint(fp)
    if in_store(remote, project, fp):
        base = Base(f"{remote}:{fp}", "store")
    else:
        directory, source = cached_pair(cache_root, fp), "cache"
        if directory is None:
            release = mirror_release(repo, fp)
            directory, source = cached_pair(cache_root, fp, release), "mirror"
        if directory is None:
            base = Base(f"images:{fp}", "upstream")
        else:
            import_pair(remote, project, fp, directory)
            base = Base(f"{remote}:{fp}", source)
    print(f"Base image {fp[:12]} from {base.source}", flush=True)
    return base
