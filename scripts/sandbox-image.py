#!/usr/bin/env python3
"""Compile and export a native Incus workspace image, without host mounts or secrets."""

import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile
import uuid


ROOT = Path(__file__).resolve().parents[1]


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def source_archive(destination):
    # A source allowlist, never the working directory, .git, home, or credentials.
    tracked = run(["git", "ls-files", "-z", "--", "go.mod", "go.sum", "cmd", "internal",
                   "docs/skills/collab-ai/SKILL.md"], cwd=ROOT, capture_output=True).stdout
    paths = [Path(p.decode()) for p in tracked.split(b"\0") if p]
    paths += [Path("infra/image") / name for name in (
        "tools.lock.json", "install.sh", "install-tools.py", "sshd_config",
        "collab-broker.service", "claude-mcp.json",
    )]
    hashes = {}
    with tarfile.open(destination, "w") as archive:
        for relative in sorted(paths):
            source = ROOT / relative
            if source.is_symlink() or not source.is_file() or not source.resolve().is_relative_to(ROOT):
                raise ValueError(f"Build input must be a regular repository file: {relative}")
            data = source.read_bytes()
            hashes[str(relative)] = hashlib.sha256(data).hexdigest()
            info = tarfile.TarInfo(str(relative))
            info.size, info.mode = len(data), 0o644
            archive.addfile(info, io.BytesIO(data))
        provenance = {
            "git_revision": run(["git", "rev-parse", "HEAD"], cwd=ROOT,
                                capture_output=True, text=True).stdout.strip(),
            "files_sha256": hashes,
        }
        data = (json.dumps(provenance, indent=2) + "\n").encode()
        info = tarfile.TarInfo("build-source.json")
        info.size = len(data)
        archive.addfile(info, io.BytesIO(data))
    return provenance


def build(args, output):
    server = json.loads(run(["incus", "query", f"{args.remote}:/1.0"],
                            capture_output=True, text=True).stdout)
    arch = server["environment"]["architectures"][0]
    lock = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
    if arch not in lock["architectures"]:
        raise ValueError(f"Unsupported Incus server architecture: {arch}")
    base = lock["architectures"][arch]["base_image"]
    project = "collab-build-" + uuid.uuid4().hex[:12]
    target = f"{args.remote}:builder"
    command = ["incus", "--project", project]
    with tempfile.TemporaryDirectory(prefix="collab-image-") as directory:
        source = Path(directory) / "source.tar"
        provenance = source_archive(source)
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
            (output / "image.tfvars.json").write_text(json.dumps({
                "image_file": str(image), "image_fingerprint": digest,
            }, indent=2) + "\n")
            print(f"Image ready: {image}\nSHA256: {digest}", flush=True)
        finally:
            # UUID project created above is the entire cleanup boundary, even on failure.
            run(["incus", "project", "delete", f"{args.remote}:{project}", "--force"], input="yes\n", text=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True, type=identifier)
    parser.add_argument("--storage-pool", default="default", type=identifier)
    parser.add_argument("--network", default="incusbr0", type=identifier)
    parser.add_argument("--output", type=Path, default=ROOT / "dist/workspace")
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
