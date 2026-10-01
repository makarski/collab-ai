"""Exercise Docker as the SSH agent without registries, credentials or model calls."""

from pathlib import Path
import subprocess


def verify(ssh, create=False):
    source = (Path(__file__).parent / "docker/proof.py").read_text()
    mode = "create" if create else "verify"
    subprocess.run(ssh + ["python3 - " + mode], input=source, text=True,
                   check=True, timeout=300)
