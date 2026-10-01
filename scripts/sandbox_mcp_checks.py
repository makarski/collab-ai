"""Run real MCP discovery and browser interaction in the offline workspace."""

from pathlib import Path
import subprocess


def verify(ssh):
    source = (Path(__file__).parent / "mcp/proof.py").read_text()
    subprocess.run(ssh + ["python3 -"], input=source, text=True, check=True, timeout=240)
