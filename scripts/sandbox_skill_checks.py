"""Verify the installed skill matches every document in the reviewed image recipe."""

import shlex
import subprocess

from sandbox_source import RECIPE_PATHS


def verify(ssh, manifest):
    prefix = "docs/skills/collab-ai/"
    documents = [name for name in RECIPE_PATHS if name.startswith(prefix)]
    for name in documents:
        installed = "/usr/local/share/collab-ai/" + name.removeprefix(prefix)
        command = shlex.join(["sha256sum", installed])
        result = subprocess.run(ssh + [command], check=True, capture_output=True, text=True)
        if result.stdout.split()[0] != manifest["files_sha256"][name]:
            raise ValueError(f"Installed collaboration skill differs from the image recipe: {name}")
