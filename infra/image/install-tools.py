"""Install only checksum-pinned native tool distributions, inside the builder."""

import hashlib
import json
from pathlib import Path
import platform
import subprocess
import tempfile
import urllib.request


def download(url, checksum, destination):
    digest = hashlib.sha256()
    with urllib.request.urlopen(url, timeout=120) as response, destination.open("wb") as out:
        while chunk := response.read(1024 * 1024):
            digest.update(chunk)
            out.write(chunk)
    if digest.hexdigest() != checksum:
        raise ValueError(f"SHA256 mismatch for {url}")


def main():
    lock = json.loads(Path("/root/build/infra/image/tools.lock.json").read_text())
    arch = lock["architectures"][platform.machine()]
    with tempfile.TemporaryDirectory() as directory:
        archive = Path(directory) / "tool.tar.gz"
        download(f"https://go.dev/dl/go{lock['go_version']}.linux-{arch['go_arch']}.tar.gz",
                 arch["go_sha256"], archive)
        extract(archive, "/usr/local")
        for name in ("go", "gofmt"):
            Path(f"/usr/local/bin/{name}").symlink_to(f"/usr/local/go/bin/{name}")
        download(f"https://github.com/openai/codex/releases/download/rust-v{lock['codex_version']}/"
                 f"codex-package-{arch['codex_target']}.tar.gz", arch["codex_sha256"], archive)
        Path("/opt/codex").mkdir()
        extract(archive, "/opt/codex")
        Path("/usr/local/bin/codex").symlink_to("/opt/codex/bin/codex")
        claude = Path("/usr/local/bin/claude")
        download(f"https://downloads.claude.ai/claude-code-releases/{lock['claude_version']}/"
                 f"{arch['claude_platform']}/claude", arch["claude_sha256"], claude)
        claude.chmod(0o755)


def extract(archive, destination):
    # Vendor archive UIDs can match the agent account. Keep installed code owned
    # by the root installer and apply its umask, rather than trusting archive modes.
    subprocess.run(["tar", "--no-same-owner", "--no-same-permissions", "-xzf", str(archive),
                    "-C", destination], check=True, umask=0o022)


if __name__ == "__main__":
    main()
