"""The Incus remote a host script drives: Linux `local`, macOS the dedicated
Colima VM. The VM's profile is `pairtask` and its remote `colima-pairtask`;
the previous remote is still recognized, with one warning per process; old
deployments are not migrated.
"""

import platform
import sys

PROFILE, REMOTE = "pairtask", "colima-pairtask"
LEGACY_PROFILE, LEGACY_REMOTE = "collab-ai", "colima-collab-ai"
_warned = False


def default_remote():
    return REMOTE if platform.system() == "Darwin" else "local"


def colima_profile(remote):
    """The Colima profile behind a macOS remote, or None for any other remote."""
    if remote == REMOTE:
        return PROFILE
    if remote == LEGACY_REMOTE:
        warn_legacy()
        return LEGACY_PROFILE
    return None


def warn_legacy():
    global _warned
    if not _warned:
        _warned = True
        print(f"pairtask: remote {LEGACY_REMOTE} (Colima profile {LEGACY_PROFILE}) is deprecated; "
              f"provision again as {REMOTE}", file=sys.stderr)
