"""The image layout table (layout-v2.json): validated the same way by the
image build and by the host checkout that drives the image.

A released layout version is immutable; an incompatible path change gets a
new version and a new table.
"""

import json
import posixpath
import re

FIELDS = ("cli", "tools_ready", "tools_activate", "supervised_codex", "codex_wrapper",
          "share_dir", "etc_dir", "sshd_config", "units")
UNIT_KEYS = ("workspace_setup", "tools_ready", "tools_setup", "rtk_setup", "docker_setup", "docker_session",
             "dev_setup", "client_setup", "secured_setup", "secured_broker", "broker", "dev_executor_socket")
UNIT_NAME = re.compile(r"[a-z0-9-]+@?\.(service|socket)")
LIFETIMES = ("permanent", "release")


def load(path):
    """The validated table at path."""
    with open(path) as stream:
        table = json.load(stream)
    validate(table)
    return table


def validate(table):
    exact_keys(table, {"version", "marker", "layout", "forwarders", "unit_aliases"}, "layout table")
    if not isinstance(table["version"], int) or isinstance(table["version"], bool) or table["version"] < 2:
        raise ValueError("layout table: version must be an integer of at least 2")
    absolute(table["marker"], "marker")
    validate_layout(table["layout"])
    validate_forwarders(table["forwarders"])
    validate_aliases(table["unit_aliases"])


def validate_layout(layout):
    exact_keys(layout, set(FIELDS), "layout")
    for field in FIELDS[:-1]:
        absolute(layout[field], field)
    exact_keys(layout["units"], set(UNIT_KEYS), "layout units")
    for name in layout["units"].values():
        unit_name(name)


def validate_forwarders(forwarders):
    if not isinstance(forwarders, list):
        raise ValueError("layout table: forwarders must be a list")
    legacy = []
    for entry in forwarders:
        exact_keys(entry, {"legacy", "target", "lifetime"}, "forwarder")
        absolute(entry["legacy"], "forwarder legacy path")
        absolute(entry["target"], "forwarder target")
        if entry["lifetime"] not in LIFETIMES:
            raise ValueError(f"forwarder {entry['legacy']}: lifetime must be one of {', '.join(LIFETIMES)}")
        legacy.append(entry["legacy"])
    unique(legacy, "forwarder legacy path")


def validate_aliases(aliases):
    if not isinstance(aliases, list):
        raise ValueError("layout table: unit_aliases must be a list")
    names = []
    for entry in aliases:
        exact_keys(entry, {"unit", "alias"}, "unit alias")
        unit_name(entry["unit"])
        unit_name(entry["alias"])
        if template(entry["unit"]) != template(entry["alias"]) or kind(entry["unit"]) != kind(entry["alias"]):
            raise ValueError(f"unit alias {entry['alias']} must be the same kind of unit as {entry['unit']}")
        names.append(entry["alias"])
    unique(names, "unit alias")


def exact_keys(value, keys, what):
    if not isinstance(value, dict) or set(value) != keys:
        raise ValueError(f"{what} must have exactly the keys {', '.join(sorted(keys))}")


def absolute(path, what):
    if not isinstance(path, str) or not path.startswith("/") or posixpath.normpath(path) != path or "/../" in path + "/":
        raise ValueError(f"{what} must be an absolute, normalized path: {path!r}")


def unit_name(name):
    if not isinstance(name, str) or not UNIT_NAME.fullmatch(name):
        raise ValueError(f"not a unit file name: {name!r}")


def unique(values, what):
    if len(set(values)) != len(values):
        raise ValueError(f"duplicate {what}")


def template(name):
    return "@." in name


def kind(name):
    return name.rsplit(".", 1)[1]
