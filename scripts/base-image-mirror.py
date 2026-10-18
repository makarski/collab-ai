#!/usr/bin/env python3
"""Check or publish the base-<fingerprint> mirror releases for the pinned base images."""

import argparse
import json
from pathlib import Path
import subprocess
import sys

from sandbox_base_mirror import check, publish
from sandbox_releases import repository


ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["check", "publish"],
                        help="check: each pin is mirrored or still upstream; publish: mirror and verify every pin")
    parser.add_argument("--repo", type=repository, default="makarski/collab-ai")
    parser.add_argument("--lock-file", type=Path, default=ROOT / "infra/image/tools.lock.json")
    args = parser.parse_args()
    lock = json.loads(args.lock_file.read_text())
    results = (check if args.action == "check" else publish)(args.repo, lock)
    for fp, state in results.items():
        print(f"{fp}: {state}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"base-image-mirror: {error}", file=sys.stderr)
        sys.exit(1)
