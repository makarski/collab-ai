"""Native image release files and checksum-verified local downloads."""

import hashlib
import json
from pathlib import Path
import shutil
import urllib.error
import urllib.request
import zipfile

from sandbox_versions import fingerprint


ARCHITECTURES = {"aarch64": "arm64", "x86_64": "amd64"}


class MissingAsset(FileNotFoundError, ValueError):
    """An expected asset is not in the given source: a usage error for the
    image, and confirmed absence for the optional bundle manifest."""


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def asset_name(architecture):
    return f"collab-ai-workspace-linux-{ARCHITECTURES[architecture]}"


def image_variables(directory, digest, image_directory=None):
    image = (image_directory or directory) / "workspace.tar.gz"
    values = {"image_file": str(image.resolve()), "image_fingerprint": digest}
    (directory / "image.tfvars.json").write_text(json.dumps(values, indent=2) + "\n")


def release_assets(directory, manifest):
    destination = directory / "release"
    destination.mkdir()
    name = asset_name(manifest["architecture"])
    shutil.copyfile(directory / "workspace.tar.gz", destination / f"{name}.tar.gz")
    (destination / f"{name}.json").write_text(json.dumps(manifest, indent=2) + "\n")


def fetch(url, destination):
    with urllib.request.urlopen(url, timeout=120) as response, destination.open("xb") as output:
        shutil.copyfileobj(response, output, length=1024 * 1024)


def from_zip(archive, name, destination):
    # Copy only the two exact expected assets; never extract archive paths.
    with zipfile.ZipFile(archive) as bundle:
        matches = [item for item in bundle.infolist() if item.filename == name]
        if not matches:
            raise MissingAsset(f"{name} is not in {archive}; select the matching architecture ZIP")
        if len(matches) > 1:
            raise ValueError(f"Expected exactly one {name} in {archive}; select the matching architecture ZIP")
        with bundle.open(matches[0]) as source, destination.open("xb") as output:
            shutil.copyfileobj(source, output, length=1024 * 1024)


def install_artifact(output, architecture, obtain):
    manifest = fetch_manifest(output, architecture, obtain)
    fetch_image(output, architecture, manifest, obtain)
    return manifest


def fetch_manifest(output, architecture, obtain):
    """The small manifest names the image fingerprint, so a cached image can be
    recognised before the large download starts."""
    obtain(f"{asset_name(architecture)}.json", output / "manifest.json")
    manifest = json.loads((output / "manifest.json").read_text())
    if manifest["architecture"] != architecture:
        raise ValueError("Image manifest architecture does not match the Incus server")
    fingerprint(manifest["image_fingerprint"])
    return manifest


def fetch_image(output, architecture, manifest, obtain):
    expected = manifest["image_fingerprint"]
    partial = output / "workspace.tar.gz.partial"
    obtain(f"{asset_name(architecture)}.tar.gz", partial)
    if sha256(partial) != expected:
        partial.unlink()
        raise ValueError("Workspace image checksum mismatch; nothing was imported")
    partial.rename(output / "workspace.tar.gz")
    image_variables(output, expected)


def bundles_name(architecture):
    return f"collab-ai-bundles-linux-{ARCHITECTURES[architecture]}.json"


def confirmed_absent(error):
    """Only a missing file or an HTTP 404 proves a release has no bundles;
    any other failure must be retried, never remembered."""
    if isinstance(error, urllib.error.HTTPError):
        return error.code == 404
    return isinstance(error, FileNotFoundError)


def fresh(obtain, name, target):
    """Obtain name into target, removing a partial file from an earlier attempt."""
    target.unlink(missing_ok=True)
    try:
        obtain(name, target)
    except BaseException:
        target.unlink(missing_ok=True)
        raise


def fetch_bundles(output, architecture, obtain, base=None):
    """Record where this image's optional tool bundles come from.

    The small bundle manifest is kept next to the image. With `base` (a URL
    prefix the bundle files can be fetched from on demand) nothing else is
    downloaded; without it, the bundle files are copied here and `base` becomes
    this directory. A release without bundles leaves nothing behind."""
    manifest_path = output / bundles_name(architecture)
    source = output / "bundle-source.json"
    try:
        fresh(obtain, bundles_name(architecture), manifest_path)
    except (FileNotFoundError, urllib.error.HTTPError) as error:
        if not confirmed_absent(error):
            raise
        source.write_text(json.dumps({"base": None}, indent=2) + "\n")  # remembered: no bundles
        return None
    manifest = json.loads(manifest_path.read_text())
    if base is None:
        for details in manifest["bundles"].values():
            target = output / details["file"]
            fresh(obtain, details["file"], target)
            if sha256(target) != details["sha256"]:
                target.unlink()
                raise ValueError(f"Bundle {details['file']} does not match its digest")
        base = output.resolve().as_uri() + "/"
    source.write_text(json.dumps({"base": base}, indent=2) + "\n")
    return manifest
