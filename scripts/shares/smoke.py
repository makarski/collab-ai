"""Mac transport proof in an explicitly disposable Incus project (no host ACL edits)."""

import argparse
from contextlib import ExitStack
import os
from pathlib import Path
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_share_runtime import connection, wait_ready


def run(command, check=True):
    return subprocess.run(command, check=check, capture_output=True, text=True, timeout=15)


def fixture(root):
    mounts = []
    for name, readonly in (("project", False), ("reference", True)):
        source = root / name
        source.mkdir()
        (source / "from-host").write_text(name)
        mounts.append(dict(project_name=name, host_path=str(source),
                           container_mount_path=str(source), container_readonly=readonly))
    (root / "project/reference").symlink_to(root / "reference")
    (root / "project/relative").symlink_to("../reference")
    return mounts


def verify_agent(agent, root):
    project, reference = root / "project", root / "reference"
    run(agent + ["sh", "-c", 'printf agent > "$1/from-agent"', "proof", str(project)])
    if (project / "from-agent").read_text() != "agent":
        raise ValueError("Agent write did not reach the Mac")
    if (project / "from-agent").stat().st_uid != os.getuid():
        raise ValueError("Transport-only fixture used an unexpected host identity")
    verify_links(agent, project)
    if run(agent + ["touch", str(reference / "forbidden")], check=False).returncode == 0:
        raise ValueError("Read-only share permitted a write")


def verify_links(agent, project):
    for link in ("reference", "relative"):
        result = run(agent + ["cat", str(project / link / "from-host")])
        if result.stdout != "reference":
            raise ValueError("Cross-mount symlink did not resolve")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--instance", required=True)
    args = parser.parse_args()
    if not args.project.startswith("collab-share-proof-"):
        raise ValueError("Use a disposable collab-share-proof-* project")
    incus = ["incus", "--project", args.project, "exec", f"{args.remote}:{args.instance}", "-T"]
    base, agent = incus + ["--"], incus + ["--user", "1001", "--group", "1001", "--"]
    with tempfile.TemporaryDirectory(prefix=".share-proof-", dir=Path(__file__).resolve().parents[2]) as directory:
        root = Path(directory).resolve()
        mounts = fixture(root)
        with ExitStack() as stack:
            processes = [process for mount in mounts for process in connection(stack, base, mount, None)]
            wait_ready(base, mounts, processes)
            verify_agent(agent, root)
        denied = run(agent + ["touch", str(root / "project/after-disconnect")], check=False)
        if denied.returncode == 0 or (root / "project/after-disconnect").exists():
            raise ValueError("Disconnected share permitted a write")
    print("PASS: Mac SFTP → Incus agent writes, absolute/relative links, read-only denial and disconnect refusal.")


if __name__ == "__main__":
    main()
