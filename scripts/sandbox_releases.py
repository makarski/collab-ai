"""Resolve public workspace releases once, then download from the selected tag."""

import json
import re
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen

from sandbox_artifacts import fetch
from sandbox_image_cache import prepare_image


def release_tag(value):
    if value != "latest" and not re.fullmatch(r"workspace-v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?", value):
        raise ValueError("Use latest or a published workspace-vX.Y.Z tag")
    return value


def repository(value):
    if not re.fullmatch(r"[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+", value):
        raise ValueError("Use OWNER/REPOSITORY")
    return value


def resolve_release(repo, selection):
    repository(repo)
    release_tag(selection)
    endpoint = "latest" if selection == "latest" else "tags/" + quote(selection, safe="")
    request = Request(f"https://api.github.com/repos/{repo}/releases/{endpoint}",
                      headers={"Accept": "application/vnd.github+json", "User-Agent": "collab-ai-sandbox"})
    try:
        with urlopen(request, timeout=30) as response:
            release = json.load(response)
    except HTTPError as error:
        raise ValueError(f"Cannot find published release {selection} in {repo} (HTTP {error.code}). "
                         "A successful main release build must finish before rollout.") from error
    return published_tag(release, selection)


def published_tag(release, selection):
    tag = release_tag(release["tag_name"])
    if tag == "latest" or release.get("draft"):
        raise ValueError("Expected a published, versioned workspace release")
    if selection != "latest" and tag != selection:
        raise ValueError("Expected a published, versioned workspace release")
    return tag


def download_release(architecture, selection="latest", repo="makarski/collab-ai", output=None):
    tag = resolve_release(repo, selection)
    print(f"Downloading {tag} for {architecture}…", flush=True)
    base = f"https://github.com/{repo}/releases/download/{quote(tag, safe='')}/"
    directory, manifest = prepare_image(architecture, lambda name, target: fetch(base + name, target), output)
    print(f"Verified image: {manifest['image_fingerprint']}", flush=True)
    # Freeze the selection; a concurrent download may move the convenience link.
    return directory.parent / manifest["image_fingerprint"] if output is None else directory
