#!/usr/bin/env python3
"""Install an image layout's legacy forwarders, then certify the layout.

apply:    create each forwarder and unit alias as a relative symlink.
finalize: run last in the image build; revalidate everything and only then
          write the layout marker, atomically.
Existing files are never replaced: an exact existing link is accepted, any
other file at a legacy path is an error.
"""

import argparse
import json
import os
from pathlib import Path
import posixpath
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
from layout_schema import FIELDS, load  # noqa: E402

UNIT_DIR = "/etc/systemd/system"
DIRECTORIES = ("share_dir", "etc_dir")
EXECUTABLES = ("cli", "tools_ready", "tools_activate", "supervised_codex", "codex_wrapper")


class Root:
    """Absolute layout paths, placed under a root directory."""

    def __init__(self, root):
        self.root = Path(root).resolve()

    def path(self, absolute):
        located = self.root / absolute.lstrip("/")
        self.inside(located.parent.resolve(), absolute)
        return located

    def target(self, absolute):
        """A canonical path that must exist and resolve inside the root."""
        located = self.path(absolute)
        if not located.exists():
            raise ValueError(f"{absolute} is missing")
        self.inside(located.resolve(), absolute)
        return located

    def inside(self, resolved, absolute):
        if resolved != self.root and self.root not in resolved.parents:
            raise ValueError(f"{absolute}: resolves outside the root")


def links(table):
    """(link path, relative target, absolute target) for every forwarder and unit alias."""
    for entry in table["forwarders"]:
        legacy, target = entry["legacy"], entry["target"]
        yield legacy, posixpath.relpath(target, posixpath.dirname(legacy)), target
    for entry in table["unit_aliases"]:
        yield f"{UNIT_DIR}/{entry['alias']}", entry["unit"], f"{UNIT_DIR}/{entry['unit']}"


def check_link(root, link, relative, target, create):
    root.target(target)
    located = root.path(link)
    if located.is_symlink():
        if os.readlink(located) != relative:
            raise ValueError(f"{link}: an existing link points elsewhere ({os.readlink(located)})")
        return
    if located.exists():
        raise ValueError(f"{link}: refusing to replace an existing file")
    if not create:
        raise ValueError(f"{link}: the forwarder is missing")
    located.parent.mkdir(parents=True, exist_ok=True)
    os.symlink(relative, located)


def apply(root, table):
    for link, relative, target in links(table):
        check_link(root, link, relative, target, create=True)


def check_targets(root, table):
    """Every canonical path the layout names must exist, including all units."""
    layout = table["layout"]
    for field in FIELDS[:-1]:
        check_target(root, field, layout[field])
    units = set(layout["units"].values()) | {entry["unit"] for entry in table["unit_aliases"]}
    for unit in sorted(units):
        if not root.target(f"{UNIT_DIR}/{unit}").is_file():
            raise ValueError(f"unit {unit} is not a file")


def check_target(root, field, absolute):
    located = root.target(absolute)
    if field in DIRECTORIES:
        if not located.is_dir():
            raise ValueError(f"{field}: {absolute} is not a directory")
    elif not located.is_file():
        raise ValueError(f"{field}: {absolute} is not a file")
    elif field in EXECUTABLES and not os.access(located, os.X_OK):
        raise ValueError(f"{field}: {absolute} is not executable")


def finalize(root, table):
    check_targets(root, table)
    for link, relative, target in links(table):
        check_link(root, link, relative, target, create=False)
    marker = root.path(table["marker"])
    marker.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".layout-", dir=marker.parent)
    try:
        with os.fdopen(descriptor, "w") as stream:
            stream.write(json.dumps({"layout": table["version"]}) + "\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o644)
        os.replace(temporary, marker)
    except BaseException:
        Path(temporary).unlink(missing_ok=True)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=["apply", "finalize"])
    parser.add_argument("--root", default="/")
    parser.add_argument("--layout", required=True, type=Path)
    args = parser.parse_args()
    table = load(args.layout)
    (apply if args.phase == "apply" else finalize)(Root(args.root), table)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as error:
        print(f"install-layout: {error}", file=sys.stderr)
        sys.exit(1)
