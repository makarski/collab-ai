"""Disposable Linux-host mount fixtures for the image smoke test."""

import json
import os
import platform
import subprocess


def run(command):
    return subprocess.run(command, check=True, capture_output=True, text=True)


def require_local_root(remote):
    if platform.system() != "Linux" or remote != "local":
        raise ValueError("--mount-check requires the local Linux Incus server")
    if os.getuid() != 0:
        raise ValueError("--mount-check requires root for disposable identity fixtures")


def prepare(directory, remote):
    require_local_root(remote)
    uid = gid = 60000
    mounts = []
    for name, readonly in [("reference", True), ("work", False)]:
        source = directory / name
        source.mkdir()
        (source / "from-host").write_text(name)
        os.chown(source, uid, gid)  # only this disposable test directory
        mounts.append({"project_name": name, "host_path": str(source),
                       "container_mount_path": f"/workspace/{name}", "container_readonly": readonly})
    private = directory / "work/operator-private"
    private.write_text("operator only")
    private.chmod(0o600)
    (directory / "mounts.auto.tfvars.json").write_text(json.dumps({
        "host_mounts": mounts, "share_identity": {"uid": uid, "gid": gid},
    }))


def check_access(execute, directory):
    if run(execute + ["cat", "/workspace/reference/from-host"]).stdout != "reference":
        raise ValueError("Agent could not read the host reference directory")
    run(execute + ["touch", "/workspace/work/from-agent"])
    check_host_ownership(directory)
    check_readonly_mount(execute, directory)
    check_mount_restrictions(execute)


def check_host_ownership(directory):
    if not (directory / "work/from-agent").is_file():
        raise ValueError("Agent write did not reach the host")
    owner = (directory / "work/from-agent").stat()
    if (owner.st_uid, owner.st_gid) != (60000, 60000):
        raise ValueError("Agent write used an identity other than the dedicated share IDs")
    if (directory / "work/from-host").stat().st_uid != 0:
        raise ValueError("Sharing changed the original host file ownership")


def check_readonly_mount(execute, directory):
    denied = subprocess.run(execute + ["touch", "/workspace/reference/forbidden"], capture_output=True)
    if denied.returncode == 0 or (directory / "reference/forbidden").exists():
        raise ValueError("Read-only mount allowed a write")


def check_mount_restrictions(execute):
    command = execute + ["cat", "/workspace/work/operator-private"]
    if subprocess.run(command, capture_output=True).returncode == 0:
        raise ValueError("Agent unexpectedly read an operator-private host file")


def verify(args, directory, project):
    incus = ["incus", "--project", project]
    target = f"{args.remote}:workspace"
    execute = incus + ["exec", target, "--user", "1001", "--group", "1001", "--"]
    check_access(execute, directory)
    run(incus + ["restart", target])
    run(execute + ["test", "-f", "/workspace/work/from-agent"])
    remove_mounts(incus, target, directory, args.tofu)
    run(execute + ["test", "!", "-e", "/workspace/work/from-host"])
    if (directory / "work/from-host").read_text() != "work":
        raise ValueError("Mount removal changed host data")
    print("PASS: host mounts, agent ownership, read-only denial, restart and safe removal.")


def remove_mounts(incus, target, directory, tofu):
    run(incus + ["stop", target])
    profile = target.removesuffix("workspace") + "offline"
    for name in ("reference", "work"):
        run(incus + ["profile", "device", "remove", profile, "host-" + name])
    run(incus + ["profile", "unset", profile, "raw.idmap"])
    (directory / "mounts.auto.tfvars.json").write_text('{"host_mounts":[],"share_identity":null}')
    run([tofu, f"-chdir={directory}", "apply", "-auto-approve", "-input=false"])
