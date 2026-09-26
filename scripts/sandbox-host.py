#!/usr/bin/env python3
"""Bootstrap the dedicated macOS Colima host; Incus resources belong to Terraform."""

import argparse
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys


PROFILE = "collab-ai"
REMOTE = "colima-collab-ai"
CONFIG = Path(__file__).resolve().parents[1] / "infra/incus/colima.json"


def desired_config():
    config = json.loads(CONFIG.read_text())
    architecture = {"arm64": "aarch64", "x86_64": "x86_64"}.get(platform.machine())
    if architecture is None:
        raise ValueError("Unsupported Mac architecture")
    config["arch"] = architecture
    return config


def check_profile(directory, config):
    if directory.is_symlink():
        raise ValueError("Refusing a symlinked Colima profile")
    if not directory.exists():
        return
    marker = directory / "collab-ai-owner.json"
    settings = directory / "colima.yaml"
    if not marker.is_file() or not settings.is_file():
        raise ValueError("Existing collab-ai profile is not managed by this script; refusing to adopt it")
    if marker.is_symlink() or settings.is_symlink():
        raise ValueError("Refusing symlinked profile configuration")
    if json.loads(marker.read_text()) != {"schema": 1, "config": config}:
        raise ValueError("Host settings changed; reconcile the existing profile explicitly before proceeding")
    if json.loads(settings.read_text()) != config:
        raise ValueError("Colima configuration drifted; inspect it before proceeding")


def apply_profile(directory, config):
    check_profile(directory, config)
    if directory.exists():
        return
    directory.mkdir(mode=0o700, parents=True)
    # JSON is valid YAML. --save-config=false preserves this inspectable file.
    with (directory / "colima.yaml").open("x") as stream:
        json.dump(config, stream, indent=2)
        stream.write("\n")
    with (directory / "collab-ai-owner.json").open("x") as stream:
        json.dump({"schema": 1, "config": config}, stream, indent=2)
        stream.write("\n")


def check_remote(socket, required=False):
    result = subprocess.run(["incus", "remote", "list", "--format", "json"],
                            capture_output=True, text=True, check=True, timeout=30)
    remote = json.loads(result.stdout).get(REMOTE)
    if remote is None and not required:
        return
    # Older Incus clients expose Addr; newer clients expose an Addrs list.
    addresses = remote.get("Addrs", [remote.get("Addr")]) if remote else []
    if addresses != [f"unix://{socket}"] or remote.get("Protocol") != "incus":
        raise ValueError(f"Incus remote {REMOTE} does not point exclusively to {socket}; refusing to use it")


def start_host(directory, config, command):
    for executable in ("colima", "incus"):
        if shutil.which(executable) is None:
            raise ValueError(f"Install {executable} first; see docs/sandbox.md")
    socket = directory / "incus.sock"
    check_remote(socket)
    apply_profile(directory, config)
    subprocess.run(command, check=True, timeout=900)
    if not socket.is_socket():
        raise ValueError(f"Colima did not expose its Incus socket: {socket}")
    check_remote(socket, required=True)
    subprocess.run(["incus", "query", f"{REMOTE}:/1.0"],
                   stdout=subprocess.DEVNULL, check=True, timeout=30)
    print(f"Incus ready. Set incus_socket = {json.dumps(str(socket))}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["plan", "apply"])
    args = parser.parse_args()
    if platform.system() != "Darwin":
        raise ValueError("This bootstrap is for macOS; on Linux use your initialized Incus server (docs/sandbox.md)")
    if os.environ.get("COLIMA_HOME"):
        raise ValueError("Unset COLIMA_HOME; this bootstrap uses the standard ~/.colima location")
    directory = Path.home() / ".colima" / PROFILE
    config = desired_config()
    check_profile(directory, config)
    command = ["colima", "start", PROFILE, "--save-config=false", "--template=false",
               "--activate=false", "--ssh-config=false", "--ssh-agent=false", "--mount", "none"]
    print(json.dumps(config, indent=2))
    print(shlex.join(command), flush=True)
    if args.action == "plan":
        print("Preview only: no files written or VM started.")
        return
    start_host(directory, config, command)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-host: {error}", file=sys.stderr)
        sys.exit(1)
