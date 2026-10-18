#!/usr/bin/env python3
"""Pin the newest upstream Ubuntu 24.04 container builds in the image lock."""

import argparse
from pathlib import Path
import sys

from sandbox_base_image import IntegrityError, Unavailable
from sandbox_base_mirror import upstream_index
from sandbox_base_refresh import refresh


ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock-file", type=Path, default=ROOT / "infra/image/tools.lock.json")
    args = parser.parse_args()
    for architecture, (serial, fp) in refresh(args.lock_file, upstream_index()).items():
        print(f"{architecture}: {serial} {fp}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError) as error:
        print(f"base-image-refresh: {error}", file=sys.stderr)
        sys.exit(1)
