#!/usr/bin/env python3
"""Prove host and image compatibility across an image layout change.

A host checkout drives both the image it provisioned before and the one it
rolls out, so both directions must work:

1. This (new) host provisions the pinned previous release (layout 1), activates
   its tools and admits SSH through layout discovery.
2. The previous release's own, unchanged host checkout rolls this run's image
   (layout 2) over the same deployment, reaching it only through the legacy
   names layout 2 keeps as forwarders. Projects, home and the login key survive.
3. That old host refuses SSH admission while the new root's tools aren't ready.

The deployment is disposable and removed afterwards.
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import uuid

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_rollout  # noqa: E402
from sandbox_layout import LEGACY, PAIRTASK, discover  # noqa: E402
from sandbox_tools_install import Container  # noqa: E402
from sandbox_workspace_checks import wait_for_broker  # noqa: E402

PROOF = "compat proof"
SEEDS = ("/workspace/compat-proof", "/home/agent/compat-proof")
READY_STATE = "/var/lib/collab-ai/tools-state.json"
AGENT_KEY = "/etc/ssh/authorized_keys/agent"


def smoke_module():
    spec = importlib.util.spec_from_file_location("sandbox_smoke", ROOT / "scripts/sandbox-smoke.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run(command, **kwargs):
    print("+", " ".join(str(part) for part in command), flush=True)
    return subprocess.run(command, check=True, **kwargs)


def operator_env(directory):
    # The baseline may predate PAIRTASK_*; both names point at the same registry.
    registry = str(directory / "registry")
    return dict(os.environ, PAIRTASK_OPERATOR_HOME=registry, COLLAB_OPERATOR_HOME=registry)


def download_baseline(args, directory, baseline):
    output = directory / "baseline-image"
    run([sys.executable, str(ROOT / "scripts/sandbox-download.py"), "--remote", args.remote,
         "--release", baseline["release"], "--repo", args.repo, "--output", str(output)])
    return output


def old_host(directory, baseline):
    checkout = directory / "old-host"
    run(git() + ["worktree", "add", "--detach", str(checkout), baseline["source"]])
    return checkout


def git():
    # CI runs the proof as root over the runner's checkout.
    return ["git", "-c", f"safe.directory={ROOT}", "-C", str(ROOT)]


def ssh_command(ssh_state):
    return ["ssh", "-F", str(ssh_state / "config"), "workspace"]


def require_layout(args, project, expected):
    layout = discover(Container(args.remote, project, "workspace"))
    if layout.version != expected.version:
        raise ValueError(f"expected image layout {expected.version}, found {layout.version}")


def new_host_on_old_image(args, directory, project, image):
    """Phase 1: today's host provisions, activates and admits on the old image."""
    sandbox_rollout.activate_running(args.remote, project, image)
    require_layout(args, project, LEGACY)
    run([sys.executable, str(ROOT / "scripts/sandbox-ssh.py"), "--remote", args.remote, "--project", project,
         "--state-dir", str(directory / "ssh")], stdout=subprocess.DEVNULL)
    ssh = ssh_command(directory / "ssh")
    wait_for_broker(ssh, cli="collab")
    for path in SEEDS:
        run(ssh + [f"printf '{PROOF}\\n' > {path}"])
    print("PASS: new host provisioned, activated and admitted the previous release (layout 1).", flush=True)


def old_host_rolls_out_new_image(args, directory, project, checkout):
    """Phase 2: the unchanged old host replaces the root with this run's image."""
    identity = (directory / "ssh/id_ed25519").read_bytes()
    run([sys.executable, str(checkout / "scripts/sandbox-provision.py"), "rollout",
         "--remote", args.remote, "--project", project, "--state-dir", str(directory),
         "--image-dir", str(args.image_dir.resolve()), "--ssh-state-dir", str(directory / "ssh")],
        env=operator_env(directory))
    require_layout(args, project, PAIRTASK)
    if (directory / "ssh/id_ed25519").read_bytes() != identity:
        raise ValueError("the old host's rollout changed the login key")
    ssh = ssh_command(directory / "ssh")
    wait_for_broker(ssh, cli="pairtask")
    for path in SEEDS:
        content = run(ssh + [f"cat {path}"], capture_output=True, text=True).stdout
        if content != PROOF + "\n":
            raise ValueError(f"{path} did not survive the replacement")
    legacy = run(ssh + ["collab status --json"], capture_output=True, text=True)
    if json.loads(legacy.stdout)["health"] != "ready" or "deprecated" not in legacy.stderr:
        raise ValueError("the deprecated collab command no longer works on the new image")
    print("PASS: the old host rolled out layout 2 through the legacy names; data and login key kept.", flush=True)


def old_host_refuses_unready_root(args, directory, project, checkout):
    """Phase 3: the old host's readiness gate still holds on the new image."""
    workspace = Container(args.remote, project, "workspace")
    workspace.execute("rm", "-f", AGENT_KEY)
    workspace.execute("mv", READY_STATE, READY_STATE + ".proof")
    try:
        refused = subprocess.run([sys.executable, str(checkout / "scripts/sandbox-ssh.py"), "--remote", args.remote,
                                  "--project", project, "--state-dir", str(directory / "ssh")],
                                 stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        if refused.returncode == 0:
            raise ValueError("the old host admitted SSH while the new root's tools were not ready")
        present = workspace.output("sh", "-c", f'test -e {AGENT_KEY} && echo present || echo absent').strip()
        if present != "absent":
            raise ValueError("an agent key was installed although readiness failed")
    finally:
        workspace.execute("mv", READY_STATE + ".proof", READY_STATE)
    print("PASS: the old host refused SSH admission while the new root's tools were not ready.", flush=True)


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True)
    parser.add_argument("--image-dir", required=True, type=Path, help="This run's installed image (layout 2)")
    parser.add_argument("--baseline", type=Path, default=ROOT / "infra/image/compat-baseline.json")
    parser.add_argument("--repo", default="pairtask/pairtask")
    parser.add_argument("--tofu", default="tofu")
    return parser.parse_args()


def main():
    args = parse_args()
    baseline = json.loads(args.baseline.read_text())
    print(f"Baseline: {baseline['release']} at {baseline['source']}", flush=True)
    smoke = smoke_module()
    project = "collab-smoke-compat-" + uuid.uuid4().hex[:8]
    directory = Path(tempfile.mkdtemp(prefix="collab-compat-"))
    image = download_baseline(args, directory, baseline)
    checkout = old_host(directory, baseline)
    smoke.prepare_state(directory, SimpleNamespace(remote=args.remote, image_dir=image), project)
    tofu = [args.tofu, f"-chdir={directory}"]
    run(tofu + ["init", "-input=false", "-lockfile=readonly"])
    try:
        run(tofu + ["apply", "-auto-approve", "-input=false"])
        new_host_on_old_image(args, directory, project, image)
        old_host_rolls_out_new_image(args, directory, project, checkout)
        old_host_refuses_unready_root(args, directory, project, checkout)
    finally:
        smoke.destroy_deployment(tofu, directory, project)
        run(git() + ["worktree", "prune"])
    print("PASS: host and image layouts are compatible in both directions.", flush=True)


if __name__ == "__main__":
    main()
