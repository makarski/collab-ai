#!/usr/bin/env python3
"""Download a pinned workspace release for the Incus server; never compile locally."""

import argparse
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
from urllib.parse import quote

from sandbox_artifacts import fetch, install_artifact


def release_tag(value):
    if not re.fullmatch(r"workspace-v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?", value):
        raise argparse.ArgumentTypeError("Select a published workspace-vX.Y.Z tag; moving aliases are not supported")
    return value


def repository(value):
    if not re.fullmatch(r"[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+", value):
        raise argparse.ArgumentTypeError("Use OWNER/REPOSITORY")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True, help="Incus remote name, without a colon")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--release", type=release_tag)
    source.add_argument("--from-dir", type=Path, help="Directory of extracted CI release assets")
    parser.add_argument("--repo", type=repository, default="makarski/collab-ai")
    parser.add_argument("--output", type=Path, default=Path("dist/installed-workspace"))
    args = parser.parse_args()
    server = subprocess.run(["incus", "query", f"{args.remote}:/1.0"],
                            check=True, capture_output=True, text=True)
    architecture = json.loads(server.stdout)["environment"]["architectures"][0]
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    if args.from_dir:
        obtain = lambda name, target: shutil.copyfile(args.from_dir / name, target)
    else:
        base = f"https://github.com/{args.repo}/releases/download/{quote(args.release, safe='')}/"
        obtain = lambda name, target: fetch(base + name, target)
    manifest = install_artifact(output, architecture, obtain)
    print(f"Verified {architecture} image: {manifest['image_fingerprint']}")
    print(f"Image variables: {output / 'image.tfvars.json'}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-download: {error}", file=sys.stderr)
        sys.exit(1)
