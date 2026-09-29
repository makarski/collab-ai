"""Resolve an explicitly selected, private Linux account for host sharing."""

import grp
import os
import pwd


def resolve_identity(share_user, writable, system):
    if system == "Darwin":
        return mac_identity(share_user, writable)
    if system != "Linux":
        raise ValueError("Host sharing supports Linux and macOS only")
    return linux_identity(share_user, writable)


def linux_identity(share_user, writable):
    if share_user:
        return sharing_identity(share_user)
    if writable:
        raise ValueError("Writable mounts require --share-user with a dedicated Linux account")
    return None


def mac_identity(share_user, writable):
    error = "macOS host sharing is read-only without host ID mapping; edit inside the persistent workspace"
    if writable:
        raise ValueError(error)
    if share_user:
        raise ValueError(error)
    return None


def sharing_identity(name):
    account, group = lookup_account(name)
    validate_account(account)
    other_users = [user for user in pwd.getpwall() if user.pw_name != name]
    validate_unique_uid(account, other_users)
    validate_private_group(account, group, other_users)
    return {"uid": account.pw_uid, "gid": account.pw_gid}


def lookup_account(name):
    try:
        account = pwd.getpwnam(name)
        return account, grp.getgrgid(account.pw_gid)
    except KeyError as error:
        raise ValueError("Create a dedicated sharing account first; see docs/sandbox-mounts.md") from error


def validate_account(account):
    validate_account_ids(account)
    if account.pw_shell not in ("/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"):
        raise ValueError("The sharing account must have a nologin/false shell")
    if set(os.getgrouplist(account.pw_name, account.pw_gid)) != {account.pw_gid}:
        raise ValueError("The sharing account must have no supplementary groups")


def validate_account_ids(account):
    error = "The sharing account must not be root or the invoking operator"
    require(min(account.pw_uid, account.pw_gid) > 0, error)
    operator_ids = {os.getuid(), int(os.environ.get("SUDO_UID", os.getuid()))}
    require(account.pw_uid not in operator_ids, error)


def validate_private_group(account, group, other_users):
    error = "The sharing account must have a private primary group with the same name"
    require(group.gr_name == account.pw_name, error)
    require(set(group.gr_mem).issubset({account.pw_name}), error)
    require(all(user.pw_gid != account.pw_gid for user in other_users), error)


def validate_unique_uid(account, other_users):
    require(all(user.pw_uid != account.pw_uid for user in other_users),
            "The sharing account must have a unique UID")


def require(condition, message):
    if not condition:
        raise ValueError(message)
