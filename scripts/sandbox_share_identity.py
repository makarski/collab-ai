"""Resolve an explicitly selected, private Linux account for host sharing."""

import grp
import os
import pwd


def sharing_identity(name):
    account, group = lookup_account(name)
    validate_account(account)
    other_users = [user for user in pwd.getpwall() if user.pw_name != name]
    if any(user.pw_uid == account.pw_uid for user in other_users):
        raise ValueError("The sharing account must have a unique UID")
    validate_private_group(account, group, other_users)
    return {"uid": account.pw_uid, "gid": account.pw_gid}


def lookup_account(name):
    try:
        account = pwd.getpwnam(name)
        return account, grp.getgrgid(account.pw_gid)
    except KeyError as error:
        raise ValueError("Create a dedicated sharing account first; see docs/sandbox-mounts.md") from error


def validate_account(account):
    operator_ids = {0, os.getuid(), int(os.environ.get("SUDO_UID", os.getuid()))}
    if account.pw_uid <= 0 or account.pw_uid in operator_ids or account.pw_gid <= 0:
        raise ValueError("The sharing account must not be root or the invoking operator")
    if account.pw_shell not in ("/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"):
        raise ValueError("The sharing account must have a nologin/false shell")
    if set(os.getgrouplist(account.pw_name, account.pw_gid)) != {account.pw_gid}:
        raise ValueError("The sharing account must have no supplementary groups")


def validate_private_group(account, group, other_users):
    error = "The sharing account must have a private primary group with the same name"
    if group.gr_name != account.pw_name:
        raise ValueError(error)
    if set(group.gr_mem) - {account.pw_name}:
        raise ValueError(error)
    if any(user.pw_gid == account.pw_gid for user in other_users):
        raise ValueError(error)
