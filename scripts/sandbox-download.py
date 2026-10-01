#!/usr/bin/env python3
"""Download a pinned workspace release for the Incus server; never compile locally."""

import argparse
import json
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys
import zipfile

from sandbox_artifacts import from_zip
from sandbox_image_cache import prepare_image
from sandbox_releases import download_release, release_tag, repository


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", default="colima-collab-ai" if platform.system() == "Darwin" else "local",
                        help="Incus remote name; defaults to colima-collab-ai on Mac, local on Linux")
    source = parser.add_mutually_exclusive_group(required=False)
    source.add_argument("--release", type=release_tag, default="latest")
    source.add_argument("--from-dir", type=Path, help="Directory of extracted CI release assets")
    source.add_argument("--from-zip", type=Path, help="CI image ZIP downloaded in a browser; no extraction needed")
    parser.add_argument("--repo", type=repository, default="makarski/collab-ai")
    parser.add_argument("--output", type=Path, help="Optional new output directory; default keeps images under dist/images")
    args = parser.parse_args()
    if shutil.which("incus") is None:
        raise ValueError("Incus is required on the host; see docs/sandbox.md#1-install-the-host-tools")
    server = subprocess.run(["incus", "query", f"{args.remote}:/1.0"],
                            check=True, capture_output=True, text=True)
    architecture = json.loads(server.stdout)["environment"]["architectures"][0]
    if args.from_zip:
        obtain = lambda name, target: from_zip(args.from_zip.expanduser(), name, target)
    elif args.from_dir:
        obtain = lambda name, target: shutil.copyfile(args.from_dir / name, target)
    else:
        output = download_release(architecture, args.release, args.repo, args.output)
        manifest = json.loads((output / "manifest.json").read_text())
        obtain = None
    if obtain is not None:
        output, manifest = prepare_image(architecture, obtain, args.output)
    print(f"Verified {architecture} image: {manifest['image_fingerprint']}")
    print(f"Image variables: {output / 'image.tfvars.json'}")
    command = ["python3", "scripts/sandbox-provision.py", "plan", "--image-dir", str(output),
               "--remote", args.remote]
    print("Next: " + shlex.join(command))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, zipfile.BadZipFile, subprocess.SubprocessError) as error:
        print(f"sandbox-download: {error}", file=sys.stderr)
        sys.exit(1)
