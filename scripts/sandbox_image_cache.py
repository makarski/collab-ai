"""Keep verified images immutable behind a convenient current-image link."""

import json
import os
from pathlib import Path
import tempfile
import uuid

from sandbox_artifacts import fetch_bundles, fetch_image, fetch_manifest, image_variables, install_artifact, sha256


def prepare_image(architecture, obtain, output=None, cache=Path("dist/images"), bundles_base=None):
    if output is not None:
        output = output.resolve()
        output.mkdir(parents=True, exist_ok=False)
        manifest = install_artifact(output, architecture, obtain)
        fetch_bundles(output, architecture, obtain, bundles_base)
        return output, manifest
    cache.mkdir(parents=True, exist_ok=True)
    current = cache / "current"
    if current.exists() and not current.is_symlink():
        raise ValueError(f"Refusing to replace {current}; it must be a managed image link")
    with tempfile.TemporaryDirectory(prefix=".download-", dir=cache) as temporary:
        staging = Path(temporary)
        manifest = fetch_manifest(staging, architecture, obtain)
        destination = (cache / manifest["image_fingerprint"]).absolute()
        if destination.exists():
            verify_cached(destination, manifest)
            print(f"Reusing cached image {destination.name}", flush=True)
        else:
            fetch_image(staging, architecture, manifest, obtain)
            image_variables(staging, manifest["image_fingerprint"], destination)
            staging.rename(destination)
    if not (destination / "bundle-source.json").exists():
        fetch_bundles(destination, architecture, obtain, bundles_base)
    select_image(current, destination)
    return current.absolute(), manifest


def verify_cached(destination, manifest):
    """A cached image is reused only when it is exactly what the release names."""
    if destination.is_symlink():
        raise ValueError(f"Refusing symlinked cached image: {destination}")
    if json.loads((destination / "manifest.json").read_text()) != manifest:
        raise ValueError(f"Cached image metadata differs: {destination}")
    if sha256(destination / "workspace.tar.gz") != manifest["image_fingerprint"]:
        raise ValueError(f"Cached image checksum mismatch: {destination}")


def select_image(current, destination):
    temporary = current.with_name(".current-" + uuid.uuid4().hex)
    try:
        temporary.symlink_to(destination.name, target_is_directory=True)
        os.replace(temporary, current)
    finally:
        temporary.unlink(missing_ok=True)
