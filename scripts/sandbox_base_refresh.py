"""Pin every architecture to the newest upstream Ubuntu 24.04 container build."""

import json
import os
from pathlib import Path
import tempfile

from sandbox_base_image import fingerprint
from sandbox_base_mirror import product


def newest_pins(index, architectures):
    """{architecture: (serial, fingerprint)} for the newest complete build of
    each architecture; fails unless every one resolves."""
    pins = {}
    for architecture in architectures:
        for serial, version in sorted(product(index, architecture).get("versions", {}).items(), reverse=True):
            items = version.get("items", {})
            fp = items.get("incus.tar.xz", {}).get("combined_squashfs_sha256")
            if fp and "root.squashfs" in items:
                pins[architecture] = (serial, fingerprint(fp))
                break
        else:
            raise ValueError(f"No upstream Ubuntu 24.04 container build for {architecture}")
    return pins


def refresh(lock_path, index):
    """Rewrite only the base image pins, atomically, once all resolved."""
    lock_path = Path(lock_path)
    lock = json.loads(lock_path.read_text())
    pins = newest_pins(index, lock["architectures"])
    for architecture, (_, fp) in pins.items():
        lock["architectures"][architecture]["base_image"] = fp
    descriptor, temporary = tempfile.mkstemp(prefix=".tools.lock-", dir=lock_path.parent)
    try:
        with os.fdopen(descriptor, "w") as output:
            output.write(json.dumps(lock, indent=2) + "\n")
        os.chmod(temporary, lock_path.stat().st_mode & 0o777)
        os.replace(temporary, lock_path)
    except BaseException:
        Path(temporary).unlink(missing_ok=True)
        raise
    return pins
