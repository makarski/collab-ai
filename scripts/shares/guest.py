"""Prepare a protected mountpoint, then run SSHFS over the operator's stdio."""

import os
from pathlib import Path
import re
import shutil
import stat
import sys


def validate_target(target):
    if not re.fullmatch(r"/Users/[^/]+/[^/]+/.+", target) or str(Path(target)) != target:
        raise ValueError("Only preserved /Users project paths are accepted")
    if any(part in (".", "..") for part in Path(target).parts):
        raise ValueError("Mount path must be canonical")


def root_directory(path, mode):
    try:
        path.mkdir(mode=mode)
    except FileExistsError:
        pass
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode):
        raise ValueError(f"Mount parent is not a directory: {path}")
    if info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError(f"Mount parent must be root-owned and non-writable: {path}")


def protected_directory(target):
    validate_target(target)
    current = Path("/")
    for part in Path(target).parts[1:]:
        current /= part
        root_directory(current, 0o700 if str(current) == target else 0o755)
    if os.path.ismount(target) or any(current.iterdir()):
        raise ValueError("Mountpoint is occupied; stop the old sharing process or restart workspace first")
    current.chmod(0o700)  # An absent share must not become a writable local directory.


def main():
    target, source, readonly = sys.argv[1:]
    if os.getuid() != 0 or not shutil.which("sshfs"):
        raise ValueError("Workspace needs the image with sshfs; run this through Incus as root")
    protected_directory(target)
    options = "passive,allow_other,default_permissions,uid=1001,gid=1001,dir_cache=no,nodev,nosuid"
    if readonly == "true":
        options += ",ro"
    os.execvp("sshfs", ["sshfs", ":" + source, target, "-f", "-o", options])


if __name__ == "__main__":
    main()
