"""Install the tiny privilege-dropping helper outside agent-writable checkouts."""

from pathlib import Path
import stat


HELPER_DIRECTORY = Path("/Library/Application Support/collab-ai/sharing")
HELPER = HELPER_DIRECTORY / "host.py"


def check_protected(path):
    for parent in [path, *path.parents]:
        if not parent.exists() and not parent.is_symlink():
            continue
        check_metadata(parent.lstat(), parent)


def check_metadata(info, path):
    if stat.S_ISLNK(info.st_mode):
        raise ValueError(f"Sharing helper path must not be a symlink: {path}")
    if info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError(f"Sharing helper path must be root-owned and non-writable: {path}")


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
