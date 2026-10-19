"""pairtask settings from the environment.

Settings are PAIRTASK_<NAME>. The deprecated COLLAB_<NAME> still applies when
the canonical variable is absent, with one warning per process. Presence
decides the source: a canonical variable set to an empty value masks the
legacy one.
"""

import os
import sys

CANONICAL, LEGACY = "PAIRTASK_", "COLLAB_"
_warned = False


def lookup(name, environ=None):
    """(value, present, legacy_used) for the setting name (without prefix)."""
    environ = os.environ if environ is None else environ
    if CANONICAL + name in environ:
        return environ[CANONICAL + name], True, False
    if LEGACY + name in environ:
        warn_legacy()
        return environ[LEGACY + name], True, True
    return "", False, False


def warn_legacy():
    global _warned
    if not _warned:
        _warned = True
        print("pairtask: COLLAB_* environment variables are deprecated; rename them to PAIRTASK_*", file=sys.stderr)


def canonical_name(name):
    return CANONICAL + name
