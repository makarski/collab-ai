"""Disposable Linux-host mount fixtures for the image smoke test."""

import json
import os
import platform
import subprocess


def run(command):
    return subprocess.run(command, check=True, capture_output=True, text=True)


def prepare(directory, remote):
    if platform.system() != "Linux" or remote != "local":
        raise ValueError("--mount-check requires the local Linux Incus server")
    uid, gid = os.getuid() or 1001, os.getgid() or 1001
    mounts = {}
    for name, readonly in [("reference", True), ("work", False)]:
        source = directory / name
        source.mkdir()
        (source / "from-host").write_text(name)
        if os.getuid() == 0:
            os.chown(source, uid, gid)  # only this disposable test directory
        mounts[name] = {"source": str(source), "path": f"/workspace/{name}", "readonly": readonly}
    (directory / "mounts.auto.tfvars.json").write_text(json.dumps({
        "host_mounts": mounts, "mount_owner": {"uid": uid, "gid": gid},
    }))


def check_access(execute, directory):
    if run(execute + ["cat", "/workspace/reference/from-host"]).stdout != "reference":
        raise ValueError("Agent could not read the host reference directory")
    run(execute + ["touch", "/workspace/work/from-agent"])
    if not (directory / "work/from-agent").is_file():
        raise ValueError("Agent write did not reach the host")
    denied = subprocess.run(execute + ["touch", "/workspace/reference/forbidden"], capture_output=True)
    if denied.returncode == 0 or (directory / "reference/forbidden").exists():
        raise ValueError("Read-only mount allowed a write")


def verify(args, directory, project):
    incus = ["incus", "--project", project]
    target = f"{args.remote}:workspace"
    execute = incus + ["exec", target, "--user", "1001", "--group", "1001", "--"]
    check_access(execute, directory)
    run(incus + ["restart", target])
    run(execute + ["test", "-f", "/workspace/work/from-agent"])
    run(incus + ["stop", target])
    profile = f"{args.remote}:offline"
    for name in ("reference", "work"):
        run(incus + ["profile", "device", "remove", profile, "host-" + name])
    run(incus + ["profile", "unset", profile, "raw.idmap"])
    (directory / "mounts.auto.tfvars.json").write_text('{"host_mounts":{},"mount_owner":null}')
    run([args.tofu, f"-chdir={directory}", "apply", "-auto-approve", "-input=false"])
    run(execute + ["test", "!", "-e", "/workspace/work/from-host"])
    if (directory / "work/from-host").read_text() != "work":
        raise ValueError("Mount removal changed host data")
    print("PASS: host mounts, agent ownership, read-only denial, restart and safe removal.")
