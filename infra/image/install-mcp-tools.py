"""Install pinned MCP binaries, dependencies and Chromium during image build."""

import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import zipfile

from tool_download import download, extract


ROOT = Path("/root/build/infra/image")
BUNDLE = Path("/opt/collab-mcp")


def native_tools(lock, arch, temporary):
    node = f"node-v{lock['node_version']}-linux-{arch['node_arch']}"
    archive = temporary / "node.tar.gz"
    download(f"https://nodejs.org/dist/v{lock['node_version']}/{node}.tar.gz", arch["node_sha256"], archive)
    extract(archive, "/opt")
    for name in ("node", "npm", "npx"):
        Path(f"/usr/local/bin/{name}").symlink_to(f"/opt/{node}/bin/{name}")
    name = f"cs-mcp-linux-{arch['codescene_arch']}"
    archive = temporary / "codescene.zip"
    download(f"https://github.com/codescene-oss/codescene-mcp-server/releases/download/"
             f"MCP-{lock['codescene_version']}/{name}.zip", arch["codescene_sha256"], archive)
    with zipfile.ZipFile(archive) as package:
        binary = Path("/usr/local/bin/cs-mcp")
        binary.write_bytes(package.read(name))
        binary.chmod(0o755)


def playwright():
    BUNDLE.mkdir()
    for name in ("package.json", "package-lock.json"):
        shutil.copyfile(ROOT / "mcp" / name, BUNDLE / name)
    subprocess.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=BUNDLE, check=True)
    env = dict(os.environ, PLAYWRIGHT_BROWSERS_PATH=str(BUNDLE / "browsers"))
    subprocess.run(["node", "node_modules/playwright/cli.js", "install", "--with-deps", "--no-shell", "chromium"],
                   cwd=BUNDLE, env=env, check=True)
    executable = subprocess.check_output(["node", "-e",
        "console.log(require('playwright').chromium.executablePath())"], cwd=BUNDLE, env=env, text=True).strip()
    Path("/usr/local/bin/collab-chromium").symlink_to(executable)


def configure(etc=Path("/etc")):
    servers = json.loads((ROOT / "mcp/servers.json").read_text())
    claude = json.loads((ROOT / "claude-mcp.json").read_text())
    claude["mcpServers"].update(servers)
    (etc / "collab-ai/claude-mcp.json").write_text(json.dumps(claude, indent=2) + "\n")
    codex = (ROOT / "codex-config.toml").read_text()
    for name, server in servers.items():
        codex += f"\n[mcp_servers.{name}]\n"
        codex += "\n".join(f"{key} = {json.dumps(value)}" for key, value in server.items()) + "\n"
    (etc / "codex/config.toml").write_text(codex)


if __name__ == "__main__":
    lock = json.loads((ROOT / "tools.lock.json").read_text())
    with tempfile.TemporaryDirectory() as directory:
        native_tools(lock, lock["architectures"][platform.machine()], Path(directory))
    playwright()
    configure()
