#!/usr/bin/env python3
"""Provision an exported image, verify offline SSH and restart, then destroy the test deployment."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid

import sandbox_mount_checks
import sandbox_secured_checks


ROOT = Path(__file__).resolve().parents[1]


def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def local_socket(remote):
    result = run(["incus", "remote", "list", "--format", "json"], capture_output=True, text=True)
    connection = json.loads(result.stdout)[remote]
    addresses = connection.get("Addrs", [connection.get("Addr")])
    if len(addresses) != 1:
        raise ValueError("Smoke tests require one explicit local Incus socket")
    address = addresses[0]
    if not address.startswith("unix://"):
        raise ValueError("Smoke tests require a local Incus Unix socket")
    return address.removeprefix("unix://") or "/var/lib/incus/unix.socket"


def prepare_state(directory, args, project):
    for source in (ROOT / "infra/incus").glob("*.tf"):
        shutil.copyfile(source, directory / source.name)
    shutil.copyfile(ROOT / "infra/incus/.terraform.lock.hcl", directory / ".terraform.lock.hcl")
    shutil.copyfile(ROOT / "infra/incus/secured_preflight.py", directory / "secured_preflight.py")
    manifest = json.loads((args.image_dir / "manifest.json").read_text())
    variables = {
        "incus_socket": local_socket(args.remote), "project_name": project,
        "image_file": str((args.image_dir / "workspace.tar.gz").resolve()),
        "image_fingerprint": manifest["image_fingerprint"],
    }
    (directory / "sandbox.auto.tfvars.json").write_text(json.dumps(variables))
    return manifest


def wait_for_broker(ssh):
    for _ in range(30):
        result = subprocess.run(ssh + ["collab status --json"], capture_output=True, text=True)
        if result.returncode == 0:
            if json.loads(result.stdout)["health"] == "ready":
                return
        time.sleep(1)
    raise ValueError(f"Broker did not become ready: {result.stderr} {result.stdout}")


def check_versions(ssh, lock):
    commands = {
        "codex --version": f"codex-cli {lock['codex_version']}\n",
        "claude --version": f"{lock['claude_version']} (Claude Code)",
        "go version": f"go version go{lock['go_version']} ",
    }
    for command, expected in commands.items():
        output = run(ssh + [command], capture_output=True, text=True).stdout
        if not output.startswith(expected):
            raise ValueError(f"Installed version differs from the manifest: {command}")


def check_offline_image(execute):
    links = json.loads(run(execute + ["ip", "-json", "link"], capture_output=True, text=True).stdout)
    if [link["ifname"] for link in links] != ["lo"]:
        raise ValueError("Workspace unexpectedly has a network interface")
    key_check = run(execute + ["find", "/etc/ssh", "-name", "ssh_host_*"], capture_output=True, text=True)
    if key_check.stdout.strip():
        raise ValueError("Image shipped SSH host keys")


def check_workspace(args, directory, project, manifest):
    target = f"{args.remote}:workspace"
    execute = ["incus", "--project", project, "exec", target, "-T", "--"]
    check_offline_image(execute)
    ssh_dir = directory / "ssh"
    run([sys.executable, str(ROOT / "scripts/sandbox-ssh.py"), "--remote", args.remote,
         "--project", project, "--state-dir", str(ssh_dir)])
    ssh = ["ssh", "-F", str(ssh_dir / "config"), "workspace"]
    uid = run(ssh + ["id -u"], capture_output=True, text=True).stdout.strip()
    if uid == "0":
        raise ValueError("SSH must log in as an unprivileged user")
    check_versions(ssh, manifest["tools"])
    run(["ssh", "-tt", "-F", str(ssh_dir / "config"), "workspace", "test -t 0 && test -t 1"],
        stdin=subprocess.DEVNULL)
    wait_for_broker(ssh)
    run(ssh + ["touch /workspace/restart-check"])
    run(["incus", "--project", project, "stop", target])
    run(["incus", "--project", project, "start", target])
    wait_for_broker(ssh)
    run(ssh + ["test -f /workspace/restart-check"])


def destroy_deployment(tofu, directory, project):
    try:
        run(tofu + ["destroy", "-auto-approve", "-input=false"])
    except BaseException:
        print(f"Cleanup failed; retained deployment state for {project} at {directory}", file=sys.stderr)
        raise
    shutil.rmtree(directory)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True)
    parser.add_argument("--image-dir", required=True, type=Path)
    parser.add_argument("--tofu", default="tofu", help="OpenTofu or Terraform executable")
    parser.add_argument("--mount-check", action="store_true", help="also test disposable local Linux host mounts")
    parser.add_argument("--secured-check", action="store_true", help="also test persistent secured state and read-only status IPC")
    args = parser.parse_args()
    project = "collab-smoke-" + uuid.uuid4().hex[:12]
    directory = Path(tempfile.mkdtemp(prefix="collab-smoke-"))
    manifest = prepare_state(directory, args, project)
    if args.mount_check:
        sandbox_mount_checks.prepare(directory, args.remote)
    tofu = [args.tofu, f"-chdir={directory}"]
    run(tofu + ["init", "-input=false", "-lockfile=readonly"])
    try:
        run(tofu + ["apply", "-auto-approve", "-input=false"])
        run(tofu + ["plan", "-detailed-exitcode", "-input=false"])
        check_workspace(args, directory, project, manifest)
        if args.secured_check:
            sandbox_secured_checks.verify(args, directory, project)
        if args.mount_check:
            sandbox_mount_checks.verify(args, directory, project)
        print("PASS: verified image, unchanged plan, offline SSH, tool versions and restart persistence.")
    except Exception:
        subprocess.run(["incus", "--project", project, "info", f"{args.remote}:workspace", "--show-log"],
                       check=False, timeout=30)
        raise
    finally:
        destroy_deployment(tofu, directory, project)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-smoke: {error}", file=sys.stderr)
        sys.exit(1)
