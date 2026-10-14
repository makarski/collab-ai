"""Install tool generations into the containers from the host, at rollout.

The host is the only installer: it caches each artifact by digest, pushes only
what a container's store lacks, and runs the in-container activator on a
fresh, unreleased root. See docs/sandbox-tools.md.
"""

from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile

from sandbox_tools import CACHE, cache_artifact, resolve

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from tool_store import generation_id, manifest_for  # noqa: E402

SHARE = "/usr/local/share/collab-ai"
INCOMING = "/var/lib/collab-ai-tools/incoming"
SELECTION_FILE = "/etc/collab-ai/tools.json"
AGENT_KEY = "/etc/ssh/authorized_keys/agent"
ACTIVATOR = "/usr/local/bin/collab-tools-activate"
# Every launch mode needs these; the rest can be turned off per preset.
REQUIRED_TOOLS = ("go", "codex", "claude", "rtk", "starship")
OPTIONAL_TOOLS = ("cs-mcp",)
SECURED_TOOLS = ("codex", "claude")
PROVENANCE_KEYS = ("collab", "recipe_commit", "files_sha256")
# Units that wait for collab-tools-ready; started by the host once a root is released.
GATED_UNITS = {
    "workspace": ("collab-tools-ready.service", "collab-client-setup.service",
                  "collab-docker-session.service", "collab-docker-setup.service"),
    "secured": ("collab-tools-ready.service",),
}
RETRY_COMMAND = "python3 scripts/sandbox-provision.py tools-retry"


class Container:
    """One managed instance, driven through the authenticated Incus endpoint."""

    def __init__(self, remote, project, name, run=subprocess.run):
        self.name = name
        self.command = ["incus", "--project", project]
        self.target = f"{remote}:{name}"
        self.run = run

    def execute(self, *args, **kwargs):
        return self.run(self.command + ["exec", self.target, "-T", "--", *args], check=True, **kwargs)

    def output(self, *args):
        return self.execute(*args, capture_output=True, text=True).stdout

    def push(self, source, destination):
        self.run(self.command + ["file", "push", str(source), self.target + destination], check=True)


def selection(enabled):
    """The tool selection as {name: bool}; required tools are always on."""
    unknown = set(enabled) - set(REQUIRED_TOOLS) - set(OPTIONAL_TOOLS)
    if unknown:
        raise ValueError(f"Unknown tools: {', '.join(sorted(unknown))}")
    return {**{name: True for name in REQUIRED_TOOLS}, **{name: name in enabled for name in OPTIONAL_TOOLS}}


def tools_for(role, selected):
    if role == "secured":
        return list(SECURED_TOOLS)
    return [name for name in (*REQUIRED_TOOLS, *OPTIONAL_TOOLS) if selected[name]]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def status(container):
    return json.loads(container.output("collab-tools-activate", "status"))


# The probe exits 0 whenever it evaluated and names its verdict; the Incus
# client also exits 1 on its own errors, so an exit code alone can't be trusted.
PROBE_SCRIPT = 'if "$@"; then echo present; else echo absent; fi'


def probe(container, *test):
    """A `test` inside the container. Only an explicit verdict counts; any
    other exit code or output is an error, never proof of absence."""
    result = container.run(container.command + ["exec", container.target, "-T", "--", "sh", "-c", PROBE_SCRIPT,
                                                "probe", "test", *test], capture_output=True, text=True)
    verdict = (result.stdout or "").strip()
    if result.returncode != 0 or verdict not in ("present", "absent"):
        raise ValueError(f"{container.name}: could not probe {' '.join(test)} "
                         f"(exit {result.returncode}, output {verdict!r})")
    return verdict == "present"


def agent_key_installed(container):
    """Whether the host ever admitted an agent to this root."""
    return probe(container, "-e", AGENT_KEY)


def has_tool_store(container):
    """Whether this root's image carries the activator: the evidence that the
    release supports the tool store. Older releases keep their baked tools."""
    return probe(container, "-x", ACTIVATOR)


def never_released(container, state):
    """True only for a root no agent could have used: no ready state, and the
    host never installed the agent's SSH key."""
    if state is not None and state.get("state") != "maintenance":
        return False  # ready, or a state this host doesn't understand: not safe to mutate
    return not agent_key_installed(container)


@dataclass(frozen=True)
class Release:
    """What the host knows about the selected release: its lock, the server
    architecture and, when the image manifest is at hand, its build provenance."""
    lock: dict
    machine: str
    provenance: dict | None = None


def build_manifest(container, release, selected):
    """The manifest for this container's root: digests of the root's own lock
    and build source, which must agree with the release."""
    lock_text = container.output("cat", f"{SHARE}/tools.lock.json")
    source_text = container.output("cat", f"{SHARE}/build-source.json")
    lock = json.loads(lock_text)
    if lock["artifacts"] != release.lock["artifacts"]:
        raise ValueError(f"{container.name}: the image's tool lock differs from the release manifest")
    if release.provenance is not None and json.loads(source_text) != release.provenance:
        raise ValueError(f"{container.name}: the root's build source differs from the release manifest")
    facts = {"release": digest(source_text.encode()), "lock_sha256": digest(lock_text.encode()),
             "architecture": release.machine}
    return lock, manifest_for(lock, facts, selected, tools_for(container.name, selected))


def release_units(container):
    """Start the gated entry points after a successful release. Idempotent: in
    baked mode they are already running; in store mode their first boot failed
    on the readiness gate and they need a reset."""
    units = GATED_UNITS.get(container.name, GATED_UNITS["secured"])
    container.execute("systemctl", "reset-failed", *units)
    container.execute("systemctl", "start", *units)


def install(container, release, selected, cache=CACHE):
    """Activate the generation for container; returns the generation ID, or
    None when the released root already runs exactly this generation. Either
    way the gated units are (re)started, so an interrupted release completes."""
    _, manifest = build_manifest(container, release, selected)
    generation = generation_id(manifest)
    current = status(container)
    if already_released(container, current, generation):
        return None
    require_fresh_root(container, current.get("state"))
    complete = current.get("complete", [])
    deliver(container, release, [(name, tool) for name, tool in manifest["tools"].items()
                                 if tool["sha256"] not in complete], cache)
    activate(container, manifest, selected)
    verify_ready(container, generation)
    release_units(container)
    return generation


def already_released(container, current, generation):
    """A ready root is verified, never changed: the same generation is complete
    (its units are started), any other generation needs a rollout."""
    state = current.get("state")
    if state is None or state.get("state") != "ready":
        return False
    if current.get("current") != generation:
        raise ValueError(f"{container.name} already runs a released tool generation; "
                         "changing tools needs a rollout (a new root)")
    release_units(container)
    return True


def require_fresh_root(container, state):
    """Every mutation requires a root no agent could have used: no ready or
    unknown state, and no agent key."""
    if not never_released(container, state):
        raise ValueError(f"{container.name} was released to agents or is in an unknown tool state; "
                         "run rollout to replace the root")


def deliver(container, release, pending, cache):
    """Push the artifacts the store lacks, from the verified host cache."""
    container.execute("install", "-d", "-m", "0700", INCOMING)
    for name, tool in pending:
        print(f"{container.name}: installing {name} {tool['version']}", flush=True)
        item = resolve(release.lock, release.machine, [name])[0]
        container.push(cache_artifact(item, cache), f"{INCOMING}/{tool['file']}")


def activate(container, manifest, selected):
    with tempfile.TemporaryDirectory(prefix="collab-tools-") as directory:
        for name, value in (("manifest.json", manifest), ("tools.json", selected)):
            path = Path(directory) / name
            path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
            container.push(path, f"{INCOMING}/{name}")
    container.execute("install", "-d", "-m", "0755", "/etc/collab-ai")
    container.execute("install", "-m", "0644", f"{INCOMING}/tools.json", SELECTION_FILE)
    container.execute("collab-tools-activate", "activate", f"{INCOMING}/manifest.json", "--incoming", INCOMING)
    container.execute("rm", "-rf", INCOMING)


def ready_for(current, generation):
    """Whether a status report says ready for exactly this generation."""
    state = current.get("state") or {}
    return state.get("state") == "ready" and current.get("current") == generation


def verify_ready(container, generation):
    if not ready_for(status(container), generation):
        raise ValueError(f"{container.name}: tool activation did not reach ready for {generation}")


def release_lock_from(image_dir):
    """The lock the release was built with, from the image manifest."""
    return json.loads((Path(image_dir) / "manifest.json").read_text())["tools"]


def release_provenance(image_dir):
    """(provenance, architecture) of the release image, for the root to match;
    (None, None) when the image directory carries no manifest."""
    path = Path(image_dir) / "manifest.json"
    if not path.exists():
        return None, None
    manifest = json.loads(path.read_text())
    return provenance_of(manifest), manifest.get("architecture")


def supports_tool_store(release_lock):
    """A lock without the artifact table can't describe a generation; such a
    release predates the store. The converse is decided per root by
    has_tool_store(), because the table shipped before the activator."""
    return "artifacts" in release_lock


def provenance_of(manifest):
    return {key: manifest[key] for key in PROVENANCE_KEYS if key in manifest}
