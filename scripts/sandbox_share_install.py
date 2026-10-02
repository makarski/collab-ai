"""Install the tiny privilege-dropping helper outside agent-writable checkouts."""

from pathlib import Path
import stat


HELPER_DIRECTORY = Path("/Library/Application Support/collab-ai/sharing")
HELPER = HELPER_DIRECTORY / "host.py"
SYSTEM_ANCESTORS = {Path("/Library"), Path("/Library/Application Support")}


def check_protected(path):
    for parent in [path, *path.parents]:
        if not parent.exists() and not parent.is_symlink():
            continue
        check_metadata(parent.lstat(), parent)


def check_metadata(info, path):
    if stat.S_ISLNK(info.st_mode):
        raise ValueError(f"Sharing helper path must not be a symlink: {path}")
    if info.st_uid != 0 or info.st_mode & forbidden_write_bits(info, path):
        raise ValueError(f"Sharing helper path must be root-owned and non-writable: {path}")


def forbidden_write_bits(info, path):
    # macOS system directories may grant writes to wheel/admin. Those host
    # administrators are already trusted; the sharing account cannot join them.
    trusted_system_group = path in SYSTEM_ANCESTORS and info.st_gid in (0, 80)
    if trusted_system_group and stat.S_ISDIR(info.st_mode):
        return 0o002
    return 0o022


def install_commands():
    check_protected(HELPER_DIRECTORY)
    root = Path(__file__).parent
    commands = [["sudo", "-n", "/usr/bin/install", "-d", "-o", "root", "-g", "wheel", "-m", "0755", str(HELPER_DIRECTORY)]]
    for source, name in [(root / "shares/host.py", "host.py"), (root / "sandbox_sftp_policy.py", "sandbox_sftp_policy.py")]:
        destination = HELPER_DIRECTORY / name
        check_protected(destination)
        commands.append(["sudo", "-n", "/usr/bin/install", "-o", "root", "-g", "wheel", "-m", "0644", str(source), str(destination)])
    return commands


def require_installed():
    for name in ("host.py", "sandbox_sftp_policy.py"):
        path = HELPER_DIRECTORY / name
        check_protected(path)
        if not path.is_file():
            raise ValueError("Run sandbox-share.py setup to install the protected sharing helper")
