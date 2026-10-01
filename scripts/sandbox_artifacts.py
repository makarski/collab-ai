"""Native image release files and checksum-verified local downloads."""

import hashlib
import json
from pathlib import Path
import shutil
import urllib.request
import zipfile

from sandbox_versions import fingerprint


ARCHITECTURES = {"aarch64": "arm64", "x86_64": "amd64"}


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
        if len(matches) != 1:
            raise ValueError(f"Expected exactly one {name} in {archive}; select the matching architecture ZIP")
        with bundle.open(matches[0]) as source, destination.open("xb") as output:
            shutil.copyfileobj(source, output, length=1024 * 1024)


def install_artifact(output, architecture, obtain):
    name = asset_name(architecture)
    obtain(f"{name}.json", output / "manifest.json")
    manifest = json.loads((output / "manifest.json").read_text())
    if manifest["architecture"] != architecture:
        raise ValueError("Image manifest architecture does not match the Incus server")
    expected = fingerprint(manifest["image_fingerprint"])
    partial = output / "workspace.tar.gz.partial"
    obtain(f"{name}.tar.gz", partial)
    if sha256(partial) != expected:
        partial.unlink()
        raise ValueError("Workspace image checksum mismatch; nothing was imported")
    partial.rename(output / "workspace.tar.gz")
    image_variables(output, expected)
    return manifest
