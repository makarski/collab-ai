#!/usr/bin/env python3
"""Publish, or verify the published copy of, the tool bundles a CI run tested."""

import argparse
from pathlib import Path
import subprocess
import sys

from sandbox_bundle_release import publish
from sandbox_releases import repository


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["publish"])
    parser.add_argument("--repo", type=repository, default="makarski/collab-ai")
    parser.add_argument("--dir", type=Path, required=True, help="A CI workspace artifact directory for one architecture")
    parser.add_argument("--architecture", choices=["aarch64", "x86_64"], required=True)
    args = parser.parse_args()
    print(f"{args.architecture}: {publish(args.repo, args.dir, args.architecture)}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"bundle-release: {error}", file=sys.stderr)
        sys.exit(1)
