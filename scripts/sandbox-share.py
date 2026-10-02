#!/usr/bin/env python3
"""Opt-in macOS file sharing through a dedicated, restricted host identity."""

import argparse
import json
import os
from pathlib import Path
import platform
import shlex
import subprocess
import sys

from sandbox_mac_shares import account_commands, acl_commands, identity, setup, validate_mounts
from sandbox_share_runtime import serve
from sandbox_share_links import uncovered_links


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["plan", "setup", "run"])
    parser.add_argument("--mounts-file", type=Path, required=True)
    parser.add_argument("--share-user", default="collab-share")
    parser.add_argument("--remote", default="colima-collab-ai")
    parser.add_argument("--project", default="collab-ai")
    return parser.parse_args()


def main():
    args = arguments()
    if platform.system() != "Darwin" or os.getuid() == 0:
        raise ValueError("Run on macOS as your normal user; setup requests sudo only for account creation")
    mounts = validate_mounts(json.loads(args.mounts_file.read_text()))
    if args.action == "plan":
        show_plan(mounts, args.share_user)
    elif args.action == "setup":
        setup(mounts, args.share_user)
        print("Sharing identity and selected-directory ACLs ready. Run sandbox-share.py run next.")
    else:
        identity(args.share_user)
        serve(mounts, args.share_user, args.remote, args.project)


def show_plan(mounts, user):
    print(json.dumps(mounts, indent=2))
    for link, target in uncovered_links(mounts):
        print(f"Unshared symlink target: {link} -> {target}")
    for command in account_commands(user) + acl_commands(mounts, user):
        print(shlex.join(command))
    print("Preview only. Setup grants host file access; run keeps the file connection open.")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("Sharing stopped. Restart workspace before reconnecting.")
    except (KeyError, ValueError, OSError, subprocess.SubprocessError) as error:
        sys.exit(f"sandbox-share: {error}")
