"""Root-installed entrypoint: drop all operator credentials before serving files."""

import grp
import ctypes
import json
import os
from pathlib import Path
import pwd
import re
import select
import signal
import subprocess
import sys


def validate_invocation(user):
    if os.getuid() != 0 or not re.fullmatch(r"collab-share(?:-[a-z0-9-]{1,16})?", user):
        raise ValueError("Expected sudo and a dedicated collab-share account")


def sharing_account(user):
    validate_invocation(user)
    account = pwd.getpwnam(user)
    expected = ("/usr/bin/false", "/var/empty", user)
    actual = (account.pw_shell, account.pw_dir, grp.getgrgid(account.pw_gid).gr_name)
    if actual != expected:
        raise ValueError("Invalid sharing account shell, home or primary group")
    if min(account.pw_uid, account.pw_gid) <= 0 or account.pw_uid == int(os.environ.get("SUDO_UID", "0")):
        raise ValueError("Sharing cannot use root or the operator")
    return account


def drop_identity(user):
    account = sharing_account(user)
    os.setgroups([account.pw_gid])
    os.setgid(account.pw_gid)
    os.setuid(account.pw_uid)
    if effective_groups() != [account.pw_gid]:
        raise ValueError("Could not restrict the effective process groups")
    os.environ.clear()
    os.environ.update(HOME="/var/empty", PATH="/usr/bin:/bin", LANG="C.UTF-8")


def effective_groups():
    # Python's macOS os.getgroups() reports directory-service membership, not
    # the process list changed by setgroups(). Read the POSIX libc entrypoint.
    libc = ctypes.CDLL(None)
    count = libc.getgroups(0, None)
    if not 0 <= count <= 1024:
        raise ValueError("Could not inspect effective process groups")
    groups = (ctypes.c_uint * count)()
    if libc.getgroups(count, groups) != count:
        raise ValueError("Effective group query failed")
    return list(groups)


def stop(*_):
    raise KeyboardInterrupt


def serve(command, watcher):
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGHUP, stop)
    process = subprocess.Popen(command)
    try:
        while process.poll() is None:
            if watcher.control(None, 1, 0.5):
                break
    finally:
        stop_server(process)


def stop_server(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def main():
    user, source, readonly, operator_pid = sys.argv[1:]
    # Subscribe before dropping credentials; the operator's death closes shares
    # even after SIGKILL, with no reliance on the operator running cleanup code.
    with select.kqueue() as watcher:
        event = select.kevent(int(operator_pid), filter=select.KQ_FILTER_PROC,
                             flags=select.KQ_EV_ADD, fflags=select.KQ_NOTE_EXIT)
        watcher.control([event], 0, 0)
        drop_identity(user)
        run_unprivileged(source, readonly, watcher)


def run_unprivileged(source, readonly, watcher):
    if source == "--identity":
        print(json.dumps(dict(uid=os.getuid(), gid=os.getgid(), groups=effective_groups())))
        return
    # -I excludes writable Python paths; this directory and module are root-owned.
    sys.path.insert(0, str(Path(__file__).parent))
    from sandbox_sftp_policy import server_command
    command = server_command(Path(source), readonly == "true")
    serve(command, watcher)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
