"""Recover native emergency state before deleting an interrupted operator."""

import json
from pathlib import Path
import re
import subprocess
import tempfile

from sandbox_operator_runtime import Operator
from sandbox_operator_state import StateStore, deployment_lock
from sandbox_operator_workflow import PENDING, PLAN, IDENTITY


def retained_operator(args, server, pending):
    if pending["remote"] != args.remote or pending["server"] != server["certificate_fingerprint"]:
        raise ValueError("Retained operator belongs to another Incus server")
    if pending["deployment_project"] != args.project:
        raise ValueError("Retained operator belongs to another deployment")
    if not re.fullmatch(r"collab-operator-[0-9a-f]{16}", pending["project"]):
        raise ValueError("Invalid retained operator name")
    operator = Operator(args.remote, server, args.storage_pool, args.network)
    operator.project = pending["project"]
    operator.base = ["incus", "--project", operator.project]
    operator.created = True
    return operator


def recover(args, directory, server):
    with deployment_lock(directory):
        pending = json.loads((directory / PENDING).read_text())
        operator = retained_operator(args, server, pending)
        instances = operator.run("list", operator.remote, "--format=json", capture_output=True, text=True)
        for instance in json.loads(instances.stdout):
            if instance["status"] != "Stopped":
                operator.run("stop", operator.remote + instance["name"], "--force")
        if pending["action"] == "apply":
            recover_state(operator, directory)
        operator.cleanup()
        for name in (PENDING, PLAN, IDENTITY):
            (directory / name).unlink(missing_ok=True)
        print(f"Recovered host state in {directory}; run plan again.")


def recover_state(operator, directory):
    endpoint = operator.remote + f"/1.0/instances/operator/files?project={operator.project}&path=/operator/config"
    listing = json.loads(subprocess.check_output(["incus", "query", endpoint]))
    if "errored.tfstate" in listing:
        with tempfile.TemporaryDirectory(prefix="collab-state-recovery-") as temporary:
            state = Path(temporary) / "errored.tfstate"
            operator.run("file", "pull", operator.target + "/operator/config/errored.tfstate", str(state))
            StateStore(directory).save(state.read_bytes())
    if not (directory / "terraform.tfstate").exists():
        raise ValueError("No host or emergency state found; retain the operator and inspect Incus before retrying")
