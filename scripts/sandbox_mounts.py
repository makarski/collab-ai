"""Validate the mount manifest shared by Colima and Incus provisioning."""

import json
import os
from pathlib import Path
import re
import tempfile

from sandbox_share_identity import resolve_identity


def validate_source_path(value):
    if not isinstance(value, str) or not value.startswith("/"):
        raise ValueError("Mount sources must be absolute directories")
    if re.search(r"[,\n\r]", value):
        raise ValueError("Mount sources cannot contain commas or newlines")


def canonical_source(value):
    validate_source_path(value)
    source = Path(value).resolve(strict=True)
    if not source.is_dir():
        raise ValueError(f"Mount source is not a directory: {source}")
    if source in (Path("/"), Path.home().resolve()):
        raise ValueError("Select a project directory, not / or your entire home")
    validate_source_path(str(source))
    return str(source)


def validate_mount_shape(spec):
    if not isinstance(spec, dict) or set(spec) - {"name", "source", "path", "readonly"}:
        raise ValueError("Each mount must be an object with only name, source, path and readonly")
    name = spec.get("name")
    if not isinstance(name, str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,29}", name):
        raise ValueError(f"Invalid mount name: {name!r}")
    return name


def validated_mount(spec):
    name = validate_mount_shape(spec)
    return {"name": name, "source": canonical_source(spec.get("source")),
            "path": validated_target(spec.get("path", "")),
            "readonly": validated_readonly(spec.get("readonly", True))}


def validated_readonly(readonly):
    if not isinstance(readonly, bool):
        raise ValueError("readonly must be true or false")
    return readonly


def validated_target(target):
    if not isinstance(target, str) or not re.fullmatch(r"/workspace/[a-zA-Z0-9][a-zA-Z0-9._-]*", target):
        raise ValueError("Mount destinations must be directly under /workspace")
    return target


def read_mounts(path):
    manifest = json.loads(Path(path).read_text())
    if not isinstance(manifest, list):
        raise ValueError("The mount manifest must be a list of objects with name, source and path")
    mounts = [validated_mount(spec) for spec in manifest]
    validate_mount_set(mounts)
    return mounts


def validate_mount_set(mounts):
    validate_distinct([mount["name"] for mount in mounts], "names")
    validate_distinct([mount["path"] for mount in mounts], "destinations")
    validate_sources(sorted(mount["source"] for mount in mounts))


def validate_distinct(values, label):
    if len(values) != len(set(values)):
        raise ValueError(f"Mount {label} must be distinct")


def validate_sources(sources):
    seen = set()
    for source in sources:
        path = Path(source)
        if path in seen or seen.intersection(path.parents):
            raise ValueError("Mount sources must not overlap or repeat")
        seen.add(path)


def mount_variables(mounts, share_user=None, system="Linux"):
    if not mounts:
        return {"host_mounts": [], "share_identity": None}
    writable = any(not entry["readonly"] for entry in mounts)
    identity = resolve_identity(share_user, writable, system)
    return {"host_mounts": mounts, "share_identity": identity}


def colima_mounts(mounts):
    return [{"location": entry["source"], "writable": not entry["readonly"]}
            for entry in sorted(mounts, key=lambda mount: mount["name"])] or None


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
