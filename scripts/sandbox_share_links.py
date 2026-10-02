"""Report symlinks that need another explicitly selected directory."""

import os
from pathlib import Path


SKIP = {".git", "node_modules", ".venv", "venv", "__pycache__", "target", "dist", "build", ".cache"}


def uncovered_links(mounts):
    roots = [Path(entry["host_path"]) for entry in mounts]
    for root in roots:
        for path in symlinks(root):
            target = Path(os.path.normpath(path.parent / os.readlink(path)))
            if not any(target.is_relative_to(source) for source in roots):
                yield path, target


def symlinks(root):
    for directory, dirs, files in os.walk(root, followlinks=False):
        dirs[:] = [name for name in dirs if name not in SKIP and not name.startswith("target-")]
        for name in dirs + files:
            path = Path(directory) / name
            if path.is_symlink():
                yield path
