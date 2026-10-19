"""Which names an image uses for the paths, helpers and units the host drives.

Host scripts and images are released together but run in any combination
during an upgrade: a host checkout drives the image it provisioned before as
well as the one it is rolling out. An image states its layout in a marker
file; an image without the marker is the legacy (collab) layout. Only a
confirmed missing marker means legacy; any other failure stops the operation.
"""

from collections import namedtuple
import json
from pathlib import Path
import sys
from types import MappingProxyType

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from layout_schema import load  # noqa: E402

MARKER = "/usr/local/share/pairtask/layout.json"

# Runs once inside the container and prints exactly one JSON line:
# {"absent": true} only for a missing marker, {"marker": <content>} for any
# present one. Any other error exits nonzero.
PROBE = f"""import json
try:
    with open({MARKER!r}) as marker:
        reply = {{"marker": json.load(marker)}}
except FileNotFoundError:
    reply = {{"absent": True}}
print(json.dumps(reply))
"""

Layout = namedtuple("Layout", ["version", "cli", "tools_ready", "tools_activate", "supervised_codex",
                               "codex_wrapper", "share_dir", "etc_dir", "sshd_config", "units"])

UNIT_KEYS = ("workspace_setup", "tools_ready", "tools_setup", "rtk_setup", "docker_setup", "docker_session",
             "dev_setup", "client_setup", "secured_setup", "secured_broker", "broker", "dev_executor_socket")

LEGACY = Layout(
    version=1,
    cli="/usr/local/bin/collab",
    tools_ready="/usr/local/bin/collab-tools-ready",
    tools_activate="/usr/local/bin/collab-tools-activate",
    supervised_codex="/usr/local/bin/collab-supervised-codex",
    codex_wrapper="/usr/local/bin/collab-codex",
    share_dir="/usr/local/share/collab-ai",
    etc_dir="/etc/collab-ai",
    sshd_config="/etc/ssh/sshd_config.collab-ai",
    units=MappingProxyType({
        "workspace_setup": "collab-workspace-setup.service",
        "tools_ready": "collab-tools-ready.service",
        "tools_setup": "collab-tools-setup.service",
        "rtk_setup": "collab-rtk-setup.service",
        "docker_setup": "collab-docker-setup.service",
        "docker_session": "collab-docker-session.service",
        "dev_setup": "collab-dev-setup.service",
        "client_setup": "collab-client-setup.service",
        "secured_setup": "collab-secured-setup.service",
        "secured_broker": "collab-secured-broker.service",
        "broker": "collab-broker.service",
        "dev_executor_socket": "collab-dev-executor.socket",
    }),
)



def from_table(path):
    """A Layout from a checked-in layout table. The image marker only selects
    the version; every path the host uses comes from its own checkout."""
    table = load(path)
    layout = dict(table["layout"], units=MappingProxyType(dict(table["layout"]["units"])))
    return Layout(version=table["version"], **layout)


PAIRTASK = from_table(ROOT / "infra/image/layout-v2.json")

# Layout version → layout. Released versions are immutable; a new layout gets a new version.
LAYOUTS = {1: LEGACY, PAIRTASK.version: PAIRTASK}


def discover(container):
    """The layout of container's image, probed once through container.output."""
    lines = container.output("python3", "-c", PROBE).splitlines()
    if len(lines) != 1:
        raise ValueError(f"{name_of(container)}: unexpected layout probe output")
    try:
        reply = json.loads(lines[0])
    except ValueError as error:
        raise ValueError(f"{name_of(container)}: unreadable layout probe output") from error
    if isinstance(reply, dict) and set(reply) == {"absent"} and reply["absent"] is True:
        return LEGACY
    if not isinstance(reply, dict) or set(reply) != {"marker"}:
        raise ValueError(f"{name_of(container)}: unexpected layout probe output")
    return layout_for(reply["marker"], name_of(container))


def layout_for(marker, name):
    if not isinstance(marker, dict) or set(marker) != {"layout"}:
        raise ValueError(f"{name}: malformed image layout marker {MARKER}")
    version = marker["layout"]
    if not isinstance(version, int) or isinstance(version, bool):
        raise ValueError(f"{name}: malformed image layout marker {MARKER}")
    if version not in LAYOUTS:
        raise ValueError(f"{name}: image layout {version} needs a newer host checkout")
    return LAYOUTS[version]


def name_of(container):
    return getattr(container, "name", "container")
