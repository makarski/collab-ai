#!/usr/bin/env python3
"""Build the optional tool bundles as release assets, inside a disposable Incus container."""

import argparse
import json
from pathlib import Path
import subprocess
import sys

from sandbox_artifacts import ARCHITECTURES
from sandbox_builder import Builder, identifier, server_architecture
from sandbox_bundles import BUNDLE_KINDS, ROOT, build_bundles


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True, type=identifier)
    parser.add_argument("--storage-pool", default="default", type=identifier)
    parser.add_argument("--network", default="incusbr0", type=identifier)
    parser.add_argument("--output", type=Path, default=ROOT / "dist/workspace/release",
                        help="Release asset directory; bundles are added next to the image assets")
    parser.add_argument("--lock-file", type=Path, default=ROOT / "infra/image/tools.lock.json")
    parser.add_argument("--kinds", nargs="+", choices=BUNDLE_KINDS, default=list(BUNDLE_KINDS))
    args = parser.parse_args()
    machine = server_architecture(args.remote)
    lock = json.loads(args.lock_file.read_text())
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    builder = Builder(args.remote, "collab-bundles", args.storage_pool, args.network)
    try:
        builder.create(lock["architectures"][machine]["base_image"])
        manifest = build_bundles(builder, ARCHITECTURES[machine], output, args.kinds)
    finally:
        builder.cleanup()
    for kind, details in manifest["bundles"].items():
        print(f"Bundle {kind}: {details['file']} {details['sha256']}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-bundles: {error}", file=sys.stderr)
        sys.exit(1)
