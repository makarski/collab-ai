"""Validate the mount manifest shared by Colima and Incus provisioning."""

import json
import os
from pathlib import Path
import re
import tempfile


def canonical_source(value):
    if not isinstance(value, str) or not value.startswith("/"):
        raise ValueError("Mount sources must be absolute directories")
    if any(character in value for character in (",", "\n", "\r")):
        raise ValueError("Mount sources cannot contain commas or newlines")
    source = Path(value).resolve(strict=True)
    if not source.is_dir():
        raise ValueError(f"Mount source is not a directory: {source}")
    if source in (Path("/"), Path.home().resolve()):
        raise ValueError("Select a project directory, not / or your entire home")
    return str(source)


def validated_mount(name, spec):
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,29}", name):
        raise ValueError(f"Invalid mount name: {name!r}")
    if not isinstance(spec, dict) or set(spec) - {"source", "path", "readonly"}:
        raise ValueError(f"Mount {name} accepts only source, path and readonly")
    target = spec.get("path", "")
    if not isinstance(target, str) or not re.fullmatch(r"/workspace/[a-zA-Z0-9][a-zA-Z0-9._-]*", target):
        raise ValueError("Mount destinations must be directly under /workspace")
    readonly = spec.get("readonly", True)
    if not isinstance(readonly, bool):
        raise ValueError("readonly must be true or false")
    return {"source": canonical_source(spec.get("source")), "path": target, "readonly": readonly}


def read_mounts(path):
    manifest = json.loads(Path(path).read_text())
    if not isinstance(manifest, dict):
        raise ValueError("The mount manifest must be an object keyed by mount name")
    mounts = {name: validated_mount(name, spec) for name, spec in manifest.items()}
    targets = [mount["path"] for mount in mounts.values()]
    if len(targets) != len(set(targets)):
        raise ValueError("Mount destinations must be distinct")
    sources = sorted(mount["source"] for mount in mounts.values())
    for previous, source in zip(sources, sources[1:]):
        if Path(source).is_relative_to(previous):
            raise ValueError("Mount sources must not overlap or repeat")
    return mounts


def mount_variables(mounts, uid=None, gid=None):
    if not mounts:
        return {"host_mounts": {}, "mount_owner": None}
    owner = {"uid": os.getuid() if uid is None else uid, "gid": os.getgid() if gid is None else gid}
    if any(value <= 0 or value >= 2147483647 for value in owner.values()):
        raise ValueError("Use non-root host IDs with --uid and --gid (id -u / id -g)")
    return {"host_mounts": mounts, "mount_owner": owner}


def colima_mounts(mounts):
    return [{"location": entry["source"], "writable": not entry["readonly"]}
            for _, entry in sorted(mounts.items())] or None


def write_json(path, value):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"Refusing symlinked output: {path}")
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
        temporary = Path(stream.name)
        try:
            json.dump(value, stream, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)
