"""Resolve one pinned artifact of tools.lock.json for a machine architecture.

Shared by the image build (inside the builder) and the host scripts, so both
derive a tool's URL, entry path and checksum from the same table.
"""

import re


def artifact(lock, name, machine):
    spec = lock["artifacts"][name]
    per_arch = spec["architectures"][machine]
    values = {"version": spec["version"], "arch": per_arch["arch"]}
    item = {"name": name, "kind": spec["kind"], "version": spec["version"], "arch": per_arch["arch"],
            "url": spec["url"].format(**values), "entry": spec["entry"].format(**values),
            "sha256": per_arch["sha256"]}
    if not re.fullmatch(r"[0-9a-f]{64}", item["sha256"]):
        raise ValueError(f"Artifact {name} has no full lowercase SHA256 for {machine}")
    if not item["url"].startswith("https://"):
        raise ValueError(f"Artifact {name} must be downloaded over HTTPS")
    return item
