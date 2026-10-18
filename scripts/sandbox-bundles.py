#!/usr/bin/env python3
"""Build the optional tool bundles as release assets, inside a disposable Incus container."""

import argparse
import json
from pathlib import Path
import subprocess
import sys

from sandbox_artifacts import ARCHITECTURES
from sandbox_base_image import DEFAULT_REPO
from sandbox_bundle_release import bundle_inputs, inputs_digest, release_name, reuse, write_release_files
from sandbox_builder import Builder, identifier, server_architecture
from sandbox_bundles import BUNDLE_KINDS, ROOT, BundleBuild
from sandbox_releases import repository


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True, type=identifier)
    parser.add_argument("--storage-pool", default="default", type=identifier)
    parser.add_argument("--network", default="incusbr0", type=identifier)
    parser.add_argument("--output", type=Path, default=ROOT / "dist/workspace/release",
                        help="Release asset directory; bundles are added next to the image assets")
    parser.add_argument("--lock-file", type=Path, default=ROOT / "infra/image/tools.lock.json")
    parser.add_argument("--repo", type=repository, default=DEFAULT_REPO, help="Repository holding the base-<fingerprint> mirror releases")
    parser.add_argument("--kinds", nargs="+", choices=BUNDLE_KINDS, default=list(BUNDLE_KINDS))
    parser.add_argument("--reuse", action="store_true",
                        help="Use the published bundles-<digest> release for these inputs, else build a versioned one")
    args = parser.parse_args()
    if args.reuse and sorted(args.kinds) != sorted(BUNDLE_KINDS):
        parser.error("--reuse builds or reuses the complete bundle set; omit --kinds")
    machine = server_architecture(args.remote)
    lock = json.loads(args.lock_file.read_text())
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    manifest = obtain_bundles(args, machine, lock, output)
    for kind, details in manifest["bundles"].items():
        print(f"Bundle {kind}: {details['file']} {details['sha256']}", flush=True)


def obtain_bundles(args, machine, lock, output):
    """Reuse the published bundles for these inputs, or build them; only a
    --reuse build is bound to a bundle release."""
    inputs = bundle_inputs(lock, machine, args.kinds) if args.reuse else None
    if inputs is not None:
        manifest = reuse(args.repo, inputs, output)
        if manifest is not None:
            print(f"Bundles {inputs_digest(inputs)[:12]} reused from {manifest['release']}", flush=True)
            return manifest
    builder = Builder(args.remote, "collab-bundles", args.storage_pool, args.network)
    try:
        builder.create(lock["architectures"][machine]["base_image"], args.repo)
        manifest = BundleBuild(builder, ARCHITECTURES[machine], output, args.lock_file).build(args.kinds)
    finally:
        builder.cleanup()
    if inputs is not None:
        manifest = write_release_files(output, inputs)
        print(f"Bundles {inputs_digest(inputs)[:12]} built for {release_name(inputs_digest(inputs))}", flush=True)
    return manifest


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-bundles: {error}", file=sys.stderr)
        sys.exit(1)
