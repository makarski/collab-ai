"""Exercise automatic replacement against an existing disposable deployment."""

import os
from pathlib import Path
import subprocess
import sys

from sandbox_secured_checks import Deployment
from sandbox_storage_checks import check_data
from sandbox_workspace_checks import wait_for_broker


ROOT = Path(__file__).resolve().parents[1]


def verify(args, directory, project):
    if not project.startswith("collab-smoke-"):
        raise ValueError("Rollout proof requires a disposable smoke project")
    ssh = directory / "ssh"
    identity = (ssh / "id_ed25519").read_bytes()
    previous_host = (ssh / "known_hosts").read_bytes()
    subprocess.run([sys.executable, str(ROOT / "scripts/sandbox-provision.py"), "rollout",
        "--remote", args.remote, "--project", project, "--state-dir", str(directory),
        "--image-dir", str(args.image_dir.resolve()), "--ssh-state-dir", str(ssh)],
        env=dict(os.environ, COLLAB_OPERATOR_HOME=str(directory / "registry")), check=True)
    if (ssh / "id_ed25519").read_bytes() != identity:
        raise ValueError("Rollout changed the private login key")
    if (ssh / "known_hosts").read_bytes() == previous_host:
        raise ValueError("Rollout did not renew the replaced workspace host key")
    check_data(Deployment(args, directory, project))
    wait_for_broker(["ssh", "-F", str(ssh / "config"), "workspace"])
    print("PASS: automatic rollout retains projects/home and login key; renewed SSH reaches the broker.", flush=True)
