"""Root-installed entrypoint: drop all operator credentials before serving files."""

import grp
import json
import os
from pathlib import Path
import pwd
import re
import sys


def sharing_account(user):
    if os.getuid() != 0 or not re.fullmatch(r"collab-share(?:-[a-z0-9-]{1,16})?", user):
        raise ValueError("Expected sudo and a dedicated collab-share account")
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
    os.environ.clear()
    os.environ.update(HOME="/var/empty", PATH="/usr/bin:/bin", LANG="C.UTF-8")


def main():
    user, source, readonly = sys.argv[1:]
    drop_identity(user)
    if source == "--identity":
        print(json.dumps(dict(uid=os.getuid(), gid=os.getgid(), groups=os.getgroups())))
        return
    # -I excludes writable Python paths; this directory and module are root-owned.
    sys.path.insert(0, str(Path(__file__).parent))
    from sandbox_sftp_policy import server_command
    command = server_command(Path(source), readonly == "true")
    os.execv(command[0], command)


if __name__ == "__main__":
    main()
