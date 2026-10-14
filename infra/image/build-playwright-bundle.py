"""Build the Playwright MCP bundle inside the disposable bundle container.

The bundle is the pinned closure of the MCP's npm dependencies and the
Chromium build Playwright pins, so one digest covers all of it. Node is
downloaded from the lock only to run npm here; it ships as its own artifact.
"""

import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

from tool_download import download, extract
from tool_lock import artifact

ROOT = Path("/root/bundle-src")
BUNDLE = Path("/root/playwright-bundle")


def node_binary(lock, machine, temporary):
    node = artifact(lock, "node", machine)
    archive = temporary / "node.tar.gz"
    download(node["url"], node["sha256"], archive)
    extract(archive, temporary)
    return temporary / node["entry"]


def build(lock, machine, temporary):
    node = node_binary(lock, machine, temporary)
    BUNDLE.mkdir()
    for name in ("package.json", "package-lock.json"):
        shutil.copyfile(ROOT / name, BUNDLE / name)
    env = dict(os.environ, PATH=f"{node.parent}:{os.environ['PATH']}",
               PLAYWRIGHT_BROWSERS_PATH=str(BUNDLE / "browsers"))
    subprocess.run([node.parent / "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"],
                   cwd=BUNDLE, env=env, check=True)
    subprocess.run([node, "node_modules/playwright/cli.js", "install", "--no-shell", "chromium"],
                   cwd=BUNDLE, env=env, check=True)
    executable = subprocess.check_output(
        [node, "-e", "console.log(require('playwright').chromium.executablePath())"],
        cwd=BUNDLE, env=env, text=True).strip()
    packages = json.loads((BUNDLE / "package-lock.json").read_text())["packages"]
    return {"chromium": str(Path(executable).relative_to(BUNDLE)),
            "playwright_mcp": packages["node_modules/@playwright/mcp"]["version"],
            "playwright": packages["node_modules/playwright"]["version"]}


if __name__ == "__main__":
    lock = json.loads((ROOT / "tools.lock.json").read_text())
    with tempfile.TemporaryDirectory() as directory:
        details = build(lock, platform.machine(), Path(directory))
    (BUNDLE / "bundle.json").write_text(json.dumps(details, indent=2) + "\n")
