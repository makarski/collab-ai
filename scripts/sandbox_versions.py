"""Resolve exact development tool versions to verified publisher checksums."""

import argparse
import copy
import json
import re
import urllib.request


def version(value):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", value):
        raise argparse.ArgumentTypeError("Use an exact version such as 0.156.1, without a v prefix")
    return value


def fingerprint(value):
    if not re.fullmatch(r"[0-9a-f]{64}", value):
        raise ValueError("Expected a full lowercase SHA256 fingerprint")
    return value


def publisher_json(url):
    request = urllib.request.Request(url, headers={"User-Agent": "collab-ai-image-builder"})
    with urllib.request.urlopen(request, timeout=60) as response:
        return json.load(response)


def go_checksum(selected, arch):
    releases = publisher_json("https://go.dev/dl/?mode=json&include=all")
    name = f"go{selected}.linux-{arch['go_arch']}.tar.gz"
    for release in releases:
        for artifact in release["files"]:
            if artifact["filename"] == name:
                return fingerprint(artifact["sha256"])
    raise ValueError(f"Official Go archive unavailable: {name}")


def codex_checksum(selected, arch):
    release = publisher_json(f"https://api.github.com/repos/openai/codex/releases/tags/rust-v{selected}")
    name = f"codex-package-{arch['codex_target']}.tar.gz"
    for artifact in release["assets"]:
        if artifact["name"] == name:
            digest = artifact.get("digest") or ""
            if not digest.startswith("sha256:"):
                raise ValueError(f"No publisher SHA256 for {name}")
            return fingerprint(digest.removeprefix("sha256:"))
    raise ValueError(f"This Codex version has no supported native package: {name}")


def claude_checksum(selected, arch):
    manifest = publisher_json(f"https://downloads.claude.ai/claude-code-releases/{selected}/manifest.json")
    if manifest["version"] != selected:
        raise ValueError("Claude manifest version differs from the requested version")
    return fingerprint(manifest["platforms"][arch["claude_platform"]]["checksum"])


def resolve_lock(path, architecture, args):
    lock = json.loads(path.read_text())
    arch = copy.deepcopy(lock["architectures"][architecture])
    # This resolved lock describes only the architecture being built. Overrides
    # must never leave another architecture paired with stale version checksums.
    lock["architectures"] = {architecture: arch}
    resolvers = {"go": go_checksum, "codex": codex_checksum, "claude": claude_checksum}
    for tool, resolve in resolvers.items():
        requested = getattr(args, f"{tool}_version", None)
        selected = version(requested or lock[f"{tool}_version"])
        if selected != lock[f"{tool}_version"]:
            arch[f"{tool}_sha256"] = resolve(selected, arch)
        lock[f"{tool}_version"] = selected
        fingerprint(arch[f"{tool}_sha256"])
    for tool in ("starship", "rtk"):
        lock[f"{tool}_version"] = version(lock[f"{tool}_version"])
        fingerprint(arch[f"{tool}_sha256"])
    arch["base_image"] = fingerprint(getattr(args, "base_image", None) or arch["base_image"])
    return lock
