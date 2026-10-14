"""Install only checksum-pinned native tool distributions, inside the builder."""

import json
from pathlib import Path
import platform
import tempfile

from tool_download import download, extract
from tool_lock import artifact


def main():
    lock = json.loads(Path("/root/build/infra/image/tools.lock.json").read_text())
    machine = platform.machine()
    with tempfile.TemporaryDirectory() as directory:
        archive = Path(directory) / "tool.tar.gz"
        go = artifact(lock, "go", machine)
        download(go["url"], go["sha256"], archive)
        extract(archive, "/usr/local")
        for name in ("go", "gofmt"):
            Path(f"/usr/local/bin/{name}").symlink_to(f"/usr/local/go/bin/{name}")
        codex = artifact(lock, "codex", machine)
        download(codex["url"], codex["sha256"], archive)
        Path("/opt/codex").mkdir()
        extract(archive, "/opt/codex")
        Path("/usr/local/bin/codex").symlink_to("/opt/codex/" + codex["entry"])
        claude = artifact(lock, "claude", machine)
        binary = Path("/usr/local/bin/claude")
        download(claude["url"], claude["sha256"], binary)
        binary.chmod(0o755)
        for name in ("starship", "rtk"):
            tool = artifact(lock, name, machine)
            download(tool["url"], tool["sha256"], archive)
            extract(archive, "/usr/local/bin")


if __name__ == "__main__":
    main()
