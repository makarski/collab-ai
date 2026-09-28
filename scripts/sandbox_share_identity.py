"""Resolve an explicitly selected, private Linux account for host sharing."""

import grp
import os
import pwd


def sharing_identity(name):
    try:
        account = pwd.getpwnam(name)
        group = grp.getgrgid(account.pw_gid)
    except KeyError as error:
        raise ValueError("Create a dedicated sharing account first; see docs/sandbox-mounts.md") from error
    operator_ids = {0, os.getuid(), int(os.environ.get("SUDO_UID", os.getuid()))}
    if account.pw_uid <= 0 or account.pw_uid in operator_ids or account.pw_gid <= 0:
        raise ValueError("The sharing account must not be root or the invoking operator")
    if account.pw_shell not in ("/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"):
        raise ValueError("The sharing account must have a nologin/false shell")
    if set(os.getgrouplist(name, account.pw_gid)) != {account.pw_gid}:
        raise ValueError("The sharing account must have no supplementary groups")
    users = pwd.getpwall()
    if any(user.pw_uid == account.pw_uid and user.pw_name != name for user in users):
        raise ValueError("The sharing account must have a unique UID")
    if group.gr_name != name or set(group.gr_mem) - {name} or any(
        user.pw_gid == account.pw_gid and user.pw_name != name for user in users
    ):
        raise ValueError("The sharing account must have a private primary group with the same name")
    return {"uid": account.pw_uid, "gid": account.pw_gid}
