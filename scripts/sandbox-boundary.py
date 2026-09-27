#!/usr/bin/env python3
"""Prove selected budget and native-tool boundaries in two disposable, offline Incus containers."""

import argparse
from pathlib import Path
import re
import subprocess
import sys

from sandbox_boundary_checks import check_budget, check_native, prepare_files
from sandbox_boundary_host import BoundaryHost


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a plain Incus name")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True, type=identifier)
    parser.add_argument("--image-dir", required=True, type=Path)
    parser.add_argument("--storage-pool", default="default", type=identifier)
    parser.add_argument("--bin-dir", type=Path, help="optional Linux collab/collab-codex binaries for testing local edits")
    host = BoundaryHost(parser.parse_args())
    print(f"Offline boundary proof: disposable project {host.project}", flush=True)
    try:
        host.prepare()
        prepare_files(host)
        check_budget(host)
        check_native(host, "codex")
        check_native(host, "claude")
    finally:
        host.cleanup()


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-boundary: {error}", file=sys.stderr)
        sys.exit(1)
