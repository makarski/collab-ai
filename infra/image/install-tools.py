"""Install only checksum-pinned native tool distributions, inside the builder."""

import json
from pathlib import Path
import platform
import tempfile
from tool_download import download, extract


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
        download(f"https://github.com/starship/starship/releases/download/v{lock['starship_version']}/"
                 f"starship-{arch['starship_target']}.tar.gz", arch["starship_sha256"], archive)
        extract(archive, "/usr/local/bin")
        download(f"https://github.com/rtk-ai/rtk/releases/download/v{lock['rtk_version']}/"
                 f"rtk-{arch['rtk_target']}.tar.gz", arch["rtk_sha256"], archive)
        extract(archive, "/usr/local/bin")


if __name__ == "__main__":
    main()
