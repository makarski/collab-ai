"""Report symlinks that need another explicitly selected directory."""

import os
from pathlib import Path
import sys


SKIP = {".git", "node_modules", ".venv", "venv", "__pycache__", "target", "dist", "build", ".cache"}


def warn_uncovered_links(mounts):
    links = list(uncovered_links(mounts))
    if not links:
        return
    color = sys.stdout.isatty() and "NO_COLOR" not in os.environ and os.environ.get("TERM") != "dumb"
    yellow, reset = ("\033[33m", "\033[0m") if color else ("", "")
    cyan = "\033[36m" if color else ""
    for link, target in links:
        print(f"{yellow}Warning: unshared symlink target: {link} -> {target}{reset}")
    print(f"{cyan}To resolve: add needed target directories to mounts.json with matching host_path and container_mount_path.\n"
          f"Use container_readonly: false for editing, true for reference access. Rerun plan; ignore unused links.{reset}")


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
