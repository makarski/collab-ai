#!/usr/bin/env python3
"""Compile and export a native Incus workspace image, without host mounts or secrets."""

import argparse
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import uuid

from sandbox_artifacts import sha256, image_variables, release_assets
from sandbox_base_image import DEFAULT_REPO, Store, ensure_base
from sandbox_builder import cleanup_project, identifier, run
from sandbox_releases import repository
from sandbox_source import source_archive
from sandbox_versions import resolve_lock, version, fingerprint


ROOT = Path(__file__).resolve().parents[1]


def build(args, output):
    server = json.loads(run(["incus", "query", f"{args.remote}:/1.0"],
                            capture_output=True, text=True).stdout)
    arch = server["environment"]["architectures"][0]
    lock_path = getattr(args, "lock_file", ROOT / "infra/image/tools.lock.json")
    lock = resolve_lock(lock_path, arch, args)
    base = lock["architectures"][arch]["base_image"]
    project = "collab-build-" + uuid.uuid4().hex[:12]
    target = f"{args.remote}:builder"
    command = ["incus", "--project", project]
    with tempfile.TemporaryDirectory(prefix="collab-image-") as directory:
        source = Path(directory) / "source.tar"
        provenance = source_archive(source, lock, getattr(args, "collab_ref", "working-tree"))
        # Only clean up a project after this invocation successfully created it.
        run(["incus", "project", "create", f"{args.remote}:{project}",
             "-c", "features.images=true", "-c", "features.profiles=true",
             "-c", "features.networks=false", "-c", "features.storage.volumes=true"])
        print(f"Building in {args.remote}:{project}; temporary network: {args.network}", flush=True)
        try:
            image = ensure_base(Store(args.remote, project), base, getattr(args, "repo", DEFAULT_REPO)).ref
            run(command + ["init", image, target, "--no-profiles",
                           "--storage", args.storage_pool, "--network", args.network,
                           "-d", "root,size=12GiB", "-c", "security.privileged=false",
                           "-c", "security.nesting=false", "-c", "security.guestapi=false",
                           "-c", "security.idmap.isolated=true", "-c", "limits.cpu=4",
                           "-c", "limits.memory=4GiB", "-c", "limits.processes=1024"])
            run(command + ["start", target])
            run(command + ["file", "push", str(source), f"{target}/root/source.tar"])
            run(command + ["exec", target, "-T", "--", "mkdir", "/root/build"])
            run(command + ["exec", target, "-T", "--", "tar", "-xf", "/root/source.tar", "-C", "/root/build"])
            run(command + ["exec", target, "-T", "--", "bash", "/root/build/infra/image/install.sh"])
            run(command + ["stop", target])
            run(command + ["publish", target, f"{args.remote}:", "--alias", "workspace", "--compression", "gzip",
                           "description=collab-ai workspace", f"user.collab-ai.source={sha256(source)}"])
            image = output / "workspace.tar.gz"
            # Incus appends the image's compression extension to the supplied basename.
            run(command + ["image", "export", f"{args.remote}:workspace", str(output / "workspace")])
            digest = sha256(image)
            provenance.update({"architecture": arch, "tools": lock, "image_fingerprint": digest})
            (output / "manifest.json").write_text(json.dumps(provenance, indent=2) + "\n")
            image_variables(output, digest)
            release_assets(output, provenance)
            print(f"Image ready: {image}\nSHA256: {digest}", flush=True)
        finally:
            # UUID project created above is the entire cleanup boundary, even on failure.
            cleanup_project(args.remote, project)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True, type=identifier)
    parser.add_argument("--storage-pool", default="default", type=identifier)
    parser.add_argument("--network", default="incusbr0", type=identifier)
    parser.add_argument("--output", type=Path, default=ROOT / "dist/workspace")
    parser.add_argument("--collab-ref", default="working-tree", help="Local tag/commit, or working-tree (tracked local edits)")
    parser.add_argument("--lock-file", type=Path, default=ROOT / "infra/image/tools.lock.json")
    parser.add_argument("--go-version", type=version)
    parser.add_argument("--codex-version", type=version)
    parser.add_argument("--claude-version", type=version)
    parser.add_argument("--repo", type=repository, default=DEFAULT_REPO, help="Repository holding the base-<fingerprint> mirror releases")
    parser.add_argument("--base-image", type=fingerprint, help="Ubuntu 24.04 container fingerprint for the server architecture")
    args = parser.parse_args()
    output = args.output.resolve()
    # Never replace an image referenced by an existing Terraform state/plan.
    output.mkdir(parents=True, exist_ok=False)
    try:
        build(args, output)
    except BaseException:
        # Allow retry after an early failure, retaining any partial export for inspection.
        try:
            output.rmdir()  # Only succeeds when the directory is empty.
        except OSError:
            pass
        raise


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-image: {error}", file=sys.stderr)
        sys.exit(1)
