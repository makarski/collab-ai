"""The tool store: immutable digest-keyed artifacts, atomic generations and a
durable maintenance state. See docs/sandbox-tools.md.

Runs as root inside a container (activator, readiness, launcher) and in host
tests. It never downloads: artifacts arrive from the host, already named by
digest, and are verified again here before anything is extracted.
"""

import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import tarfile
import tempfile
import time
import uuid
import zipfile

STORE = Path("/var/lib/collab-ai-tools")
STATE = Path("/var/lib/collab-ai/tools-state.json")
SHARE = Path("/usr/local/share/collab-ai")
MODE_FILE = SHARE / "tools-mode"
LOCK_FILE = SHARE / "tools.lock.json"
SOURCE_FILE = SHARE / "build-source.json"
KINDS = ("archive", "binary", "zip")
REQUIRED_MANIFEST = ("release", "lock_sha256", "architecture", "selection", "tools")


class StoreError(ValueError):
    pass


def file_sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def mode(path=MODE_FILE):
    """`baked`: the image carries the tools and the store is exercised only;
    `store`: the launchers depend on an activated generation."""
    try:
        value = path.read_text().strip()
    except FileNotFoundError:
        return "baked"
    if value not in ("baked", "store"):
        raise StoreError(f"Unknown tools mode {value!r}")
    return value


def read_state(path=STATE):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return None


def write_state(value, path=STATE):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    with temporary.open("rb") as stream:
        os.fsync(stream.fileno())
    temporary.replace(path)


def generation_id(manifest):
    return hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()[:16]


def manifest_for(lock, release, selection, tools):
    """The host builds this from the release's lock. `release` carries the
    digests this root must match: {"release", "lock_sha256", "architecture"}."""
    from tool_lock import artifact
    entries = {name: tool_entry(artifact(lock, name, release["architecture"])) for name in tools}
    return {**release, "selection": selection, "tools": entries}


def tool_entry(item):
    """One manifest entry: the artifact arrives under its URL's file name."""
    return {"kind": item["kind"], "entry": item["entry"], "sha256": item["sha256"],
            "file": PurePosixPath(item["url"]).name, "version": item["version"]}


def validate_manifest(manifest):
    missing = [key for key in REQUIRED_MANIFEST if key not in manifest]
    if missing:
        raise StoreError(f"Manifest lacks {', '.join(missing)}")
    for name, tool in manifest["tools"].items():
        validate_tool(name, tool)


def validate_tool(name, tool):
    if tool.get("kind") not in KINDS:
        raise StoreError(f"Tool {name} has an unsupported kind")
    digest = tool.get("sha256")
    if not isinstance(digest, str) or len(digest) != 64:
        raise StoreError(f"Tool {name} has no digest")
    if not tool.get("entry") or not tool.get("file"):
        raise StoreError(f"Tool {name} lacks its entry or file name")
    if "/" in tool["file"]:
        raise StoreError(f"Tool {name} has an invalid file name")


# Archive member policy. Python's tarfile "data" filter is the second layer.

def normalized(name):
    path = PurePosixPath(name)
    if path.is_absolute():
        raise StoreError(f"Archive member escapes the artifact: {name!r}")
    parts = [part for part in path.parts if part != "."]
    if not parts:
        raise StoreError("Archive member has an empty name")
    if ".." in parts:
        raise StoreError(f"Archive member escapes the artifact: {name!r}")
    return PurePosixPath(*parts)


def link_target_inside(member_path, linkname):
    target = PurePosixPath(linkname)
    if target.is_absolute():
        raise StoreError(f"Absolute link target in archive: {linkname!r}")
    resolved = os.path.normpath(str(member_path.parent / target))
    if resolved.startswith("..") or resolved == "..":
        raise StoreError(f"Link target escapes the artifact: {linkname!r}")


def validate_members(members):
    """Reject anything outside: regular files, directories, symlinks that stay
    inside, and hardlinks to earlier regular members. Names are unique after
    normalization, including case-insensitively."""
    seen, folded, regular = set(), set(), set()
    for member in members:
        path = normalized(member.name)
        key = str(path)
        if key in seen or key.lower() in folded:
            raise StoreError(f"Duplicate or colliding archive member: {member.name!r}")
        validate_member_type(member, path, regular)
        seen.add(key)
        folded.add(key.lower())


def validate_member_type(member, path, regular):
    if member.isreg():
        regular.add(str(path))
    elif member.issym():
        link_target_inside(path, member.linkname)
    elif member.islnk():
        require_known_hardlink(member, regular)
    elif not member.isdir():
        raise StoreError(f"Unsupported archive member type: {member.name!r}")


def require_known_hardlink(member, regular):
    if str(normalized(member.linkname)) not in regular:
        raise StoreError(f"Hardlink to an unknown member: {member.name!r}")


def extract_tar(archive, destination):
    with tarfile.open(archive, "r:*") as tar:
        members = tar.getmembers()
        validate_members(members)
        tar.extractall(destination, members=members, filter="data")


def extract_zip(archive, destination, entry):
    with zipfile.ZipFile(archive) as package:
        matches = [item for item in package.infolist() if item.filename == entry]
        if len(matches) != 1:
            raise StoreError(f"Expected exactly one {entry} in the zip artifact")
        target = destination / normalized(entry)
        target.parent.mkdir(parents=True, exist_ok=True)
        with package.open(matches[0]) as source, target.open("xb") as output:
            shutil.copyfileobj(source, output, length=1024 * 1024)


def install_artifact(kind, source, destination, entry):
    destination.mkdir()
    if kind == "archive":
        extract_tar(source, destination)
    elif kind == "zip":
        extract_zip(source, destination, entry)
    else:
        shutil.copyfile(source, destination / entry)
    target = destination / normalized(entry)
    if not target.is_file():
        raise StoreError(f"Artifact lacks its entry {entry}")
    if kind != "archive":
        target.chmod(0o755)
    root_owned(destination)


def root_owned(directory):
    """Vendor archive owners never apply; everything installed is root's and
    never group- or world-writable."""
    for path in [directory, *directory.rglob("*")]:
        if path.is_symlink():
            continue
        os.chown(path, 0, 0, follow_symlinks=False) if os.geteuid() == 0 else None
        path.chmod(path.stat().st_mode & ~(stat.S_IWGRP | stat.S_IWOTH))


def remove_unless_marked(directory, marker):
    """Entries without their completion marker are garbage from a crash."""
    for path in directory.iterdir():
        if not (path / marker).exists():
            shutil.rmtree(path)


class Store:
    def __init__(self, root=STORE, state=STATE):
        self.root = Path(root)
        self.state_path = Path(state)
        self.artifacts = self.root / "artifacts"
        self.generations = self.root / "generations"
        self.current = self.root / "current"

    # Reading

    def current_manifest(self):
        try:
            return json.loads((self.current / "manifest.json").read_text())
        except (FileNotFoundError, NotADirectoryError):
            return None

    def status(self):
        state = read_state(self.state_path)
        manifest = self.current_manifest()
        return {"mode": mode(), "state": state,
                "current": generation_id(manifest) if manifest else None,
                "manifest": manifest, "complete": sorted(self.complete_artifacts())}

    def complete_artifacts(self):
        if not self.artifacts.is_dir():
            return set()
        return {path.name for path in self.artifacts.iterdir() if (path / ".complete").exists()}

    def resolve(self, tool):
        """The absolute entry path of tool in the current generation."""
        manifest = self.current_manifest()
        if manifest is None or tool not in manifest["tools"]:
            return None
        entry = manifest["tools"][tool]["entry"]
        return (self.current / tool).resolve() / normalized(entry)

    # Activation

    def activate(self, manifest, incoming):
        validate_manifest(manifest)
        state = read_state(self.state_path)
        if state is not None and state.get("state") == "ready":
            raise StoreError("This root already has an activated generation; tools change only on a new root")
        generation = generation_id(manifest)
        self.root.mkdir(parents=True, exist_ok=True)
        with (self.root / "lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            write_state({"state": "maintenance", "generation": generation, "started": time.time()}, self.state_path)
            self.remove_incomplete()
            for name, tool in manifest["tools"].items():
                self.stage(name, tool, Path(incoming))
            self.build_generation(generation, manifest)
            os.chmod(self.artifacts, 0o755)
            os.chmod(self.generations, 0o755)
            self.flip(generation)
            self.prune()
            write_state({"state": "ready", "generation": generation, "activated": time.time()}, self.state_path)
        return generation

    def remove_incomplete(self):
        for directory in (self.artifacts, self.generations):
            directory.mkdir(mode=0o700, exist_ok=True)
            os.chmod(directory, 0o700)
        remove_unless_marked(self.artifacts, ".complete")
        remove_unless_marked(self.generations, "manifest.json")

    def stage(self, name, tool, incoming):
        destination = self.artifacts / tool["sha256"]
        if (destination / ".complete").exists():
            return
        source = incoming / tool["file"]
        if not source.is_file() or source.is_symlink():
            raise StoreError(f"Artifact for {name} was not delivered")
        if file_sha256(source) != tool["sha256"]:
            raise StoreError(f"Artifact for {name} does not match its digest")
        staging = Path(tempfile.mkdtemp(prefix=".stage-", dir=self.artifacts))
        try:
            install_artifact(tool["kind"], source, staging / "content", tool["entry"])
            (staging / "content" / ".complete").write_text(json.dumps({"tool": name, "version": tool.get("version")}) + "\n")
            (staging / "content").rename(destination)
        finally:
            shutil.rmtree(staging, ignore_errors=True)

    def build_generation(self, generation, manifest):
        target = self.generations / generation
        if target.exists():
            shutil.rmtree(target)
        staging = self.generations / (".build-" + uuid.uuid4().hex)
        staging.mkdir()
        for name, tool in manifest["tools"].items():
            (staging / name).symlink_to(Path("..") / ".." / "artifacts" / tool["sha256"])
            if not (staging / name / normalized(tool["entry"])).is_file():
                raise StoreError(f"Generation validation failed: {name} has no entry {tool['entry']}")
        (staging / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        staging.rename(target)

    def flip(self, generation):
        temporary = self.root / (".current-" + uuid.uuid4().hex)
        temporary.symlink_to(Path("generations") / generation, target_is_directory=True)
        try:
            os.replace(temporary, self.current)
        finally:
            temporary.unlink(missing_ok=True)

    def prune(self):
        """Keep the current and the newest previous generation and the
        artifacts they reference; this only ever runs inside an activation,
        with no tool process alive on this root."""
        current = self.current.resolve().name
        others = sorted((path for path in self.generations.iterdir() if path.name != current),
                        key=lambda path: path.stat().st_mtime, reverse=True)
        keep = {current} | {path.name for path in others[:1]}
        referenced = set()
        for name in keep:
            manifest = json.loads((self.generations / name / "manifest.json").read_text())
            referenced |= {tool["sha256"] for tool in manifest["tools"].values()}
        for path in others[1:]:
            shutil.rmtree(path)
        for path in self.artifacts.iterdir():
            if path.name not in referenced:
                shutil.rmtree(path)


def ready(store=None, lock_file=LOCK_FILE, source_file=SOURCE_FILE, mode_file=MODE_FILE):
    """Return None when tool entry points may start, else the reason they may not."""
    if mode(mode_file) == "baked":
        return None
    manifest, reason = active_generation(store or Store())
    if reason:
        return reason
    return release_mismatch(manifest, lock_file, source_file)


def active_generation(store):
    """The current manifest when the state says ready for it, else the reason."""
    state = read_state(store.state_path)
    if state is None or state.get("state") != "ready":
        return None, "sandbox tools are in maintenance; run the rollout to activate them"
    manifest = store.current_manifest()
    if manifest is None or generation_id(manifest) != state.get("generation"):
        return None, "the active tool generation does not match the recorded state"
    return manifest, None


def release_mismatch(manifest, lock_file, source_file):
    if manifest["lock_sha256"] != file_sha256(lock_file) or manifest["release"] != file_sha256(source_file):
        return "tools were activated for a different release; run the rollout"
    return None
