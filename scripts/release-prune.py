#!/usr/bin/env python3
"""Delete automatic build releases beyond the newest few, with their tags."""

import argparse
import subprocess
import sys

from release_prune import prune


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--keep", type=int, default=2, help="newest build releases to keep (default 2)")
    args = parser.parse_args()
    if args.keep < 1:
        raise ValueError("--keep must be at least 1")
    for tag in prune(args.repo, args.keep):
        print(f"Deleted {tag}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.SubprocessError) as error:
        print(f"release-prune: {error}", file=sys.stderr)
        sys.exit(1)
