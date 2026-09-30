"""Exercise installed RTK hooks and retained tracking through sandbox SSH."""

from pathlib import Path
import subprocess


def verify(ssh):
    source = (Path(__file__).parent / "rtk/probe.py").read_text()
    subprocess.run(ssh + ["python3 -"], input=source, text=True, check=True, timeout=60)
