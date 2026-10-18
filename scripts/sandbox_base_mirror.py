"""Mirror each pinned base image into its own `base-<fingerprint>` release.

Publishing runs on one serialized CI job per repository. A release is staged
as a draft, verified as uploaded, and only then published; a published mirror
is never changed. A draft is resumed from its own verified assets when
possible, so a pin that upstream already pruned isn't lost.
"""

from collections import namedtuple
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

from sandbox_artifacts import ARCHITECTURES
from sandbox_base_image import (ASSETS, IntegrityError, Unavailable, cached_pair, combined_digest, fetch,
                                fingerprint, install_pair, mirror_release, release_shape, tag, verify_pair)
from sandbox_releases import repository


INDEX = "https://images.linuxcontainers.org/streams/v1/images.json"
UPSTREAM = "https://images.linuxcontainers.org/"
# The simplestreams item that holds each mirrored asset.
ITEMS = {"incus.tar.xz": "incus.tar.xz", "rootfs.squashfs": "root.squashfs"}
REFRESH = "run scripts/base-image-refresh.py and merge the new pins"


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def file_sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def upstream_index():
    try:
        with urlopen(INDEX, timeout=60) as response:
            return json.load(response)
    except HTTPError as error:
        raise Unavailable(f"{INDEX}: HTTP {error.code}") from error
    except (URLError, OSError, ValueError) as error:
        raise Unavailable(f"{INDEX}: {error}") from error


def product(index, architecture):
    return index.get("products", {}).get(f"ubuntu:noble:{ARCHITECTURES[architecture]}:default", {})


def upstream_entry(index, fp):
    """(architecture, serial, files) for the upstream build of fp, or None
    once upstream no longer lists it."""
    for architecture in ARCHITECTURES:
        for serial, version in product(index, architecture).get("versions", {}).items():
            items = version.get("items", {})
            if items.get("incus.tar.xz", {}).get("combined_squashfs_sha256") != fp:
                continue
            files = {name: {"path": items[item]["path"], "sha256": items[item]["sha256"]}
                     for name, item in ITEMS.items()}
            return architecture, serial, files
    return None


def find_release(repo, fp):
    """The release tagged base-<fp>, drafts included, or None."""
    return find_tagged(repo, tag(fp))


def find_tagged(repo, name):
    """The release tagged name, drafts included, or None."""
    listing = run(["gh", "api", "--paginate", f"repos/{repository(repo)}/releases",
                   "--jq", f'.[] | select(.tag_name == "{name}")'], capture_output=True, text=True).stdout
    matches = listed(listing, name)
    if len(matches) > 1:
        raise IntegrityError(f"More than one release is tagged {name}")
    return listed_release(matches[0], name) if matches else None


def listed(listing, name):
    try:
        return [json.loads(line) for line in listing.splitlines() if line.strip()]
    except ValueError as error:
        raise Unavailable(f"Unexpected release listing for {name}: {error}") from error


def listed_release(release, name):
    """A listed release, checked to have the fields the publisher relies on."""
    if not isinstance(release, dict):
        raise Unavailable(f"Unexpected release listing for {name}")
    release_shape(release, ("name",))
    if not all(isinstance(asset.get("id"), int) for asset in release["assets"]):
        raise Unavailable(f"Unexpected release listing for {name}")
    return release


def release_assets(release):
    assets = {}
    for asset in release["assets"]:
        if asset["name"] not in ASSETS or asset["name"] in assets:
            raise IntegrityError(f"Release {release.get('tag_name')} has an unexpected asset {asset.get('name')}")
        assets[asset["name"]] = asset
    return assets


def pull_asset(repo, asset, destination):
    """Download a release asset through the API, which also works for drafts."""
    with Path(destination).open("xb") as output:
        run(["gh", "api", "-H", "Accept: application/octet-stream",
             f"repos/{repository(repo)}/releases/assets/{asset['id']}"], stdout=output)


def pull_release(repo, release, directory):
    """Download the release's assets into directory; the names it pulled."""
    pulled = []
    for name, asset in release_assets(release).items():
        pull_asset(repo, asset, Path(directory) / name)
        pulled.append(name)
    return pulled


def pair_matches(directory, fp):
    try:
        verify_pair(directory, fp)
    except IntegrityError:
        return False
    return True


def fetch_upstream(entry, directory, names):
    """Download names from upstream, each checked against the index."""
    _, _, files = entry
    for name in names:
        target = Path(directory) / name
        target.unlink(missing_ok=True)
        fetch(UPSTREAM + files[name]["path"], target)
        if file_sha256(target) != files[name]["sha256"]:
            raise IntegrityError(f"Upstream {name} doesn't match its published checksum")


Pin = namedtuple("Pin", ["repo", "architecture", "fp", "load_index"])


def lazy_index(index=None):
    """A loader that fetches the upstream index at most once, when first needed."""
    loaded = [index]

    def load():
        if loaded[0] is None:
            loaded[0] = upstream_index()
        return loaded[0]
    return load


def publish(repo, lock, index=None):
    """Make sure every pinned base image has a verified, published mirror."""
    load_index, results = lazy_index(index), {}
    for architecture, entry in lock["architectures"].items():
        fp = fingerprint(entry["base_image"])
        results[fp] = publish_one(repo, architecture, fp, load_index)
    return results


def publish_one(repo, architecture, fp, load_index):
    pin = Pin(repo, architecture, fp, load_index)
    release = find_release(repo, fp)
    if release is not None and not release["draft"]:
        return verify_published(pin, release)
    with tempfile.TemporaryDirectory(prefix="collab-base-") as temporary:
        directory = Path(temporary)
        if release is None:
            stage_new(pin, directory)
        else:
            resume_draft(pin, release, directory)
        verify_uploaded(repo, fp, directory / "uploaded")
        run(["gh", "release", "edit", tag(fp), "--repo", repo, "--draft=false", "--latest=false"])
    if mirror_release(repo, fp) is None:
        raise Unavailable(f"Release {tag(fp)} didn't publish")
    print(f"Base image {fp[:12]} ({architecture}): mirror published", flush=True)
    return "published"


def verify_published(pin, release):
    """A published mirror is only ever checked, never changed."""
    with tempfile.TemporaryDirectory(prefix="collab-base-") as temporary:
        directory = Path(temporary)
        if sorted(pull_release(pin.repo, release, directory)) != sorted(ASSETS):
            raise IntegrityError(f"Published release {tag(pin.fp)} is missing assets")
        verify_pair(directory, pin.fp)
    print(f"Base image {pin.fp[:12]} ({pin.architecture}): mirror verified", flush=True)
    return "verified"


def upstream_for(architecture, fp, load_index):
    entry = upstream_entry(load_index(), fp)
    if entry is None:
        raise ValueError(f"Base image {fp} ({architecture}) is neither mirrored nor available upstream; {REFRESH}")
    if entry[0] != architecture:
        raise IntegrityError(f"Base image {fp} is an {entry[0]} build, but the lock pins it for {architecture}")
    return entry


def stage_new(pin, directory):
    entry = upstream_for(pin.architecture, pin.fp, pin.load_index)
    fetch_upstream(entry, directory, ASSETS)
    verify_pair(directory, pin.fp)
    run(["gh", "release", "create", tag(pin.fp), "--repo", pin.repo, "--draft", "--latest=false",
         "--title", f"Base image {pin.fp[:12]} ({pin.architecture}, upstream {entry[1]})",
         "--notes", base_notes(pin.architecture, pin.fp, entry[1])])
    upload(pin, directory, ASSETS)


def upload(pin, directory, names):
    run(["gh", "release", "upload", tag(pin.fp), "--repo", pin.repo, *(str(directory / name) for name in names)])


def resume_draft(pin, release, directory):
    """Finish an interrupted draft. Its own complete, verified pair is used as
    it is; otherwise only its missing or wrong assets are replaced."""
    pulled = pull_release(pin.repo, release, directory)
    if sorted(pulled) == sorted(ASSETS) and pair_matches(directory, pin.fp):
        return
    entry = upstream_for(pin.architecture, pin.fp, pin.load_index)
    replace = [name for name in ASSETS if name not in pulled or not upstream_file(entry, directory, name)]
    assets = release_assets(release)
    for name in replace:
        if name in assets:
            run(["gh", "api", "-X", "DELETE", f"repos/{repository(pin.repo)}/releases/assets/{assets[name]['id']}"])
    fetch_upstream(entry, directory, replace)
    verify_pair(directory, pin.fp)
    upload(pin, directory, replace)


def upstream_file(entry, directory, name):
    """Whether a pulled draft asset is byte-identical to the upstream file."""
    return file_sha256(Path(directory) / name) == entry[2][name]["sha256"]


def verify_uploaded(repo, fp, directory):
    """Download what the draft now holds and check it against the pin."""
    directory.mkdir()
    release = find_release(repo, fp)
    if release is None or sorted(pull_release(repo, release, directory)) != sorted(ASSETS):
        raise IntegrityError(f"Draft {tag(fp)} doesn't hold both assets after upload")
    if combined_digest(*(directory / name for name in ASSETS)) != fp:
        raise IntegrityError(f"Uploaded assets of {tag(fp)} don't match the pin")


def base_notes(architecture, fp, serial):
    return (f"Input mirror, not a workspace release: the Ubuntu 24.04 container image {fp} "
            f"({architecture}, images.linuxcontainers.org build {serial}). Image builds and rollouts "
            "import it when upstream has pruned the build. Keep it while any workspace release pins it.")


def cache(repo, lock, architecture, cache_root):
    """Put the verified pair for one architecture's pin into the host cache,
    from the mirror or else upstream, without publishing anything."""
    fp = fingerprint(lock["architectures"][architecture]["base_image"])
    if cached_pair(cache_root, fp) is not None:
        return {fp: "cache"}
    release = mirror_release(repo, fp)
    if release is not None:
        cached_pair(cache_root, fp, release)
        return {fp: "mirror"}
    entry = upstream_for(architecture, fp, upstream_index)
    install_pair(cache_root, fp, lambda staging: fetch_upstream(entry, staging, ASSETS))
    return {fp: "upstream"}


def check(repo, lock, index=None):
    """Availability, not integrity: each pin has a published mirror with both
    assets, or is still listed upstream. Consumers verify the bytes."""
    load_index, results = lazy_index(index), {}
    for architecture, entry in lock["architectures"].items():
        fp = fingerprint(entry["base_image"])
        results[fp] = "mirror" if mirror_release(repo, fp) is not None else "upstream"
        if results[fp] == "upstream":
            upstream_for(architecture, fp, load_index)
    return results
