"""Keep only the newest automatic releases; explicit version tags are never pruned."""

import json
import re
import subprocess

BUILD_TAG = re.compile(r"^workspace-v0\.0\.0-build\.[0-9]+\.[0-9]+$")


def prunable(releases, keep):
    """Tags to delete: automatic `build` releases beyond the `keep` newest,
    never drafts, prereleases or explicit workspace-vX.Y.Z releases."""
    builds = [item for item in releases
              if BUILD_TAG.match(item["tagName"]) and not item.get("isDraft") and not item.get("isPrerelease")]
    builds.sort(key=lambda item: item["createdAt"], reverse=True)
    return [item["tagName"] for item in builds[keep:]]


def list_releases(repo, run=subprocess.run):
    result = run(["gh", "release", "list", "--repo", repo, "--limit", "200",
                  "--json", "tagName,createdAt,isDraft,isPrerelease"], check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def prune(repo, keep, run=subprocess.run):
    victims = prunable(list_releases(repo, run), keep)
    for tag in victims:
        run(["gh", "release", "delete", tag, "--repo", repo, "--yes", "--cleanup-tag"], check=True)
    return victims
