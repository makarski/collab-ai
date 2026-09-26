#!/usr/bin/env python3
"""Compile and export a native Incus workspace image, without host mounts or secrets."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import uuid

from sandbox_artifacts import sha256, image_variables, release_assets
from sandbox_source import source_archive
from sandbox_versions import resolve_lock, version, fingerprint


ROOT = Path(__file__).resolve().parents[1]


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def cleanup_project(remote, project):
    # Explicit deletion works with the Incus 6.0 LTS client too. All resources
    # are confined to the UUID project successfully created by this invocation.
    scoped = ["incus", "--project", project]
    instances = json.loads(run(scoped + ["list", f"{remote}:", "--format", "json"],
                               capture_output=True, text=True).stdout)
    for instance in instances:
        run(scoped + ["delete", f"{remote}:{instance['name']}", "--force"])
    images = json.loads(run(scoped + ["image", "list", f"{remote}:", "--format", "json"],
                            capture_output=True, text=True).stdout)
    for image in images:
        run(scoped + ["image", "delete", f"{remote}:{image['fingerprint']}"])
    run(["incus", "project", "delete", f"{remote}:{project}"])


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
            run(command + ["init", f"images:{base}", target, "--no-profiles",
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
