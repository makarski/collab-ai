"""Install the pinned Go toolchain for compiling collab inside the builder.

The slim image ships no Go: install.sh removes this toolchain again before the
image is published; agents get Go from the tool store.
"""

import json
from pathlib import Path
import platform
import tempfile

from tool_download import download, extract
from tool_lock import artifact

if __name__ == "__main__":
    lock = json.loads(Path("/root/build/infra/image/tools.lock.json").read_text())
    go = artifact(lock, "go", platform.machine())
    with tempfile.TemporaryDirectory() as directory:
        archive = Path(directory) / "go.tar.gz"
        download(go["url"], go["sha256"], archive)
        extract(archive, "/usr/local")
