"""Validate explicit macOS shares and prepare a non-login file-sharing identity."""

import grp
import os
from pathlib import Path
import pwd
import re
import subprocess

from sandbox_mounts import canonical_source, validated_readonly, validate_mount_shape, validate_mount_set
from sandbox_share_identity import require
from sandbox_share_install import install_commands


def validate_mounts(manifest):
    if not isinstance(manifest, list) or not manifest:
        raise ValueError("Select at least one directory in the mount list")
    mounts = [validate_mount(entry) for entry in manifest]
    validate_mount_set(mounts)
    return mounts


def validate_mount(entry):
    name = validate_mount_shape(entry)
    source = canonical_source(entry.get("host_path"))
    if not re.fullmatch(r"/Users/[^/]+/[^/]+/.+", source):
        raise ValueError("Mac sharing requires selected project directories below /Users/USER/DIRECTORY/")
    if entry.get("container_mount_path") != source:
        raise ValueError("Mac writable shares preserve symlinks: container_mount_path must equal host_path")
    return dict(project_name=name, host_path=source, container_mount_path=source,
                container_readonly=validated_readonly(entry.get("container_readonly", True)))


def validate_username(user):
    if not re.fullmatch(r"collab-share(?:-[a-z0-9-]{1,16})?", user):
        raise ValueError("Use collab-share or collab-share-NAME for the dedicated account")


def identity(user):
    validate_username(user)
    account = pwd.getpwnam(user)
    group = grp.getgrgid(account.pw_gid)
    validate_login(account)
    validate_groups(account, group)
    validate_unique(account)
    return account


def validate_login(account):
    require(min(account.pw_uid, account.pw_gid) > 0 and account.pw_uid != os.getuid(),
            "Sharing cannot use root or the operator identity")
    require((account.pw_shell, account.pw_dir) == ("/usr/bin/false", "/var/empty"),
            "Sharing account must have /usr/bin/false shell and /var/empty home")


def validate_groups(account, group):
    user = account.pw_name
    require(group.gr_name == user and set(group.gr_mem).issubset({user}),
            "Sharing requires a private same-name primary group")
    # macOS adds implicit groups even to daemon users. The installed helper
    # clears them all before serving; reject administrative membership outright.
    groups = set(os.getgrouplist(user, account.pw_gid))
    if groups.intersection({0, 20, 80}):
        raise ValueError("Sharing account belongs to wheel, staff or admin")


def validate_unique(account):
    for other in pwd.getpwall():
        if other.pw_name == account.pw_name:
            continue
        if other.pw_uid == account.pw_uid or other.pw_gid == account.pw_gid:
            raise ValueError("Sharing UID/GID is also used by another account")


def unused_account(user, uid):
    users = {value for account in pwd.getpwall() for value in (account.pw_name, account.pw_uid)}
    groups = {value for group in grp.getgrall() for value in (group.gr_name, group.gr_gid)}
    if {user, uid}.intersection(users | groups):
        raise ValueError(f"Sharing account or ID {uid} already exists; inspect it before setup")


def account_commands(user):
    validate_username(user)
    try:
        identity(user)
        return []
    except KeyError:
        pass
    uid = 60000
    unused_account(user, uid)
    return create_account_commands(user, uid)


def create_account_commands(user, uid):
    group, account = f"/Groups/{user}", f"/Users/{user}"
    entries = [(group, None, None), (group, "PrimaryGroupID", str(uid)),
               (account, None, None), (account, "UniqueID", str(uid)),
               (account, "PrimaryGroupID", str(uid)), (account, "UserShell", "/usr/bin/false"),
               (account, "NFSHomeDirectory", "/var/empty"), (account, "Password", "*"),
               (account, "IsHidden", "1")]
    return [["sudo", "-n", "/usr/bin/dscl", ".", "-create", path, *([key, value] if key else [])]
            for path, key, value in entries]


def acl_commands(mounts, user):
    operator = pwd.getpwuid(os.getuid()).pw_name
    commands = []
    parents = set()
    for entry in mounts:
        source = Path(entry["host_path"])
        parents.update(parent for parent in source.parents if str(parent).startswith("/Users/"))
        rights = access_rights(entry["container_readonly"])
        rule = f"user:{user} allow {rights},file_inherit,directory_inherit"
        commands.append(["/bin/chmod", "-R", "-P", "+a", rule, str(source)])
        if not entry["container_readonly"]:
            rule = f"user:{operator} allow {rights},file_inherit,directory_inherit"
            commands.append(["/bin/chmod", "-R", "-P", "+a", rule, str(source)])
    for parent in sorted(parents):
        commands.insert(0, ["/bin/chmod", "+a", f"user:{user} allow search", str(parent)])
    return commands


def access_rights(readonly):
    rights = "read,list,search,readattr,readextattr"
    if not readonly:
        rights += ",write,append,add_file,add_subdirectory,delete,delete_child,writeattr,writeextattr"
    return rights


def setup(mounts, user):
    commands = account_commands(user) + install_commands()
    subprocess.run(["sudo", "-v"], check=True)
    for command in commands:
        subprocess.run(command, check=True)
    identity(user)
    # ACL changes run as the operator, never as root. -P does not follow links.
    for command in acl_commands(mounts, user):
        subprocess.run(command, check=True)
