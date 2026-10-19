#!/usr/bin/env python3
"""Prove host and image compatibility across an image layout change.

A host checkout drives both the image it provisioned before and the one it
rolls out, so both directions must work:

1. This (new) host provisions the pinned previous release (layout 1), activates
   its tools and admits SSH through layout discovery.
2. The previous release's own, unchanged host checkout rolls this run's image
   (layout 2) over the same deployment. Its layout table predates layout 2, so
   it must fail closed after replacing the root: no tool activation, no SSH
   admission, and the deployment left recoverable. This host then finishes the
   rollout with tools-retry. Projects, home and the login key survive, and the
   deprecated collab command still works.
3. That old host refuses SSH admission on the new root, ready or not.

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
UNKNOWN_LAYOUT = "needs a newer host checkout"


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


def provision_command(checkout, action, args, directory, project):
    return [sys.executable, str(checkout / "scripts/sandbox-provision.py"), action,
            "--remote", args.remote, "--project", project, "--state-dir", str(directory),
            "--image-dir", str(args.image_dir.resolve()), "--ssh-state-dir", str(directory / "ssh")]


def agent_key_present(args, project):
    workspace = Container(args.remote, project, "workspace")
    return workspace.output("sh", "-c", f'test -e {AGENT_KEY} && echo present || echo absent').strip() == "present"


def old_host_rolls_out_new_image(args, directory, project, checkout):
    """Phase 2: the unchanged old host replaces the root with this run's image,
    then fails closed: it doesn't know layout 2, so it neither activates tools
    nor admits SSH, and leaves the deployment for a newer host to finish."""
    identity = (directory / "ssh/id_ed25519").read_bytes()
    command = provision_command(checkout, "rollout", args, directory, project)
    print("+", " ".join(command), flush=True)
    attempt = subprocess.run(command, env=operator_env(directory), stderr=subprocess.PIPE, text=True)
    print(attempt.stderr, end="", file=sys.stderr, flush=True)
    if attempt.returncode == 0:
        raise ValueError("the old host activated an image layout it does not know")
    if UNKNOWN_LAYOUT not in attempt.stderr:
        raise ValueError("the old host's rollout failed for another reason than the unknown layout")
    require_layout(args, project, PAIRTASK)
    if (directory / "ssh/id_ed25519").read_bytes() != identity:
        raise ValueError("the old host's rollout changed the login key")
    if agent_key_present(args, project):
        raise ValueError("the old host admitted SSH to a root whose tools it could not activate")
    print("PASS: the old host replaced the root with layout 2, then failed closed without admitting agents.", flush=True)


def new_host_finishes_rollout(args, directory, project):
    """Phase 2, continued: this host completes the interrupted rollout the
    documented way. Data and the login key survive; the legacy CLI still works."""
    identity = (directory / "ssh/id_ed25519").read_bytes()
    run(provision_command(ROOT, "tools-retry", args, directory, project), env=operator_env(directory))
    require_layout(args, project, PAIRTASK)
    if (directory / "ssh/id_ed25519").read_bytes() != identity:
        raise ValueError("finishing the rollout changed the login key")
    ssh = ssh_command(directory / "ssh")
    wait_for_broker(ssh, cli="pairtask")
    for path in SEEDS:
        content = run(ssh + [f"cat {path}"], capture_output=True, text=True).stdout
        if content != PROOF + "\n":
            raise ValueError(f"{path} did not survive the replacement")
    legacy = run(ssh + ["collab status --json"], capture_output=True, text=True)
    if json.loads(legacy.stdout)["health"] != "ready" or "deprecated" not in legacy.stderr:
        raise ValueError("the deprecated collab command no longer works on the new image")
    print("PASS: the new host finished the rollout with tools-retry; data and login key kept.", flush=True)


def old_host_refuses_unready_root(args, directory, project, checkout):
    """Phase 3: the old host refuses SSH admission on the new root; the tools
    state is moved away so the refusal holds in the hardest case too."""
    workspace = Container(args.remote, project, "workspace")
    workspace.execute("rm", "-f", AGENT_KEY)
    workspace.execute("mv", READY_STATE, READY_STATE + ".proof")
    try:
        refused = subprocess.run([sys.executable, str(checkout / "scripts/sandbox-ssh.py"), "--remote", args.remote,
                                  "--project", project, "--state-dir", str(directory / "ssh")],
                                 stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        if refused.returncode == 0:
            raise ValueError("the old host admitted SSH while the new root's tools were not ready")
        if agent_key_present(args, project):
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
        new_host_finishes_rollout(args, directory, project)
        old_host_refuses_unready_root(args, directory, project, checkout)
    finally:
        smoke.destroy_deployment(tofu, directory, project)
        run(git() + ["worktree", "prune"])
    print("PASS: the new host drives both layouts; the old host fails closed on the new one.", flush=True)


if __name__ == "__main__":
    main()
