"""Download, stop managed runtimes, and apply a data-preserving replacement."""

from copy import copy
import importlib.util
import json
import subprocess
from types import SimpleNamespace

from sandbox_operator_config import ROOT, image_selection, state_project
from sandbox_operator_state import deployment_lock
from sandbox_operator_workflow import initialize_variables, require_recovered, run_locked_operation
from sandbox_releases import download_release


def managed_instances(directory, project):
    if state_project(directory) is None:
        return []
    state = json.loads((directory / "terraform.tfstate").read_text())
    attributes = []
    for resource in state.get("resources", []):
        if resource.get("mode", "managed") != "managed" or resource["type"] != "incus_instance":
            continue
        attributes.extend(instance["attributes"] for instance in resource.get("instances", []))
    return [managed_name(attrs, project) for attrs in attributes]


def managed_name(attrs, project):
    if attrs["name"] not in ("workspace", "secured") or attrs["project"] != project:
        raise ValueError("Unexpected managed instance; use explicit plan/apply")
    return attrs["name"]


def instances(args):
    result = subprocess.check_output(["incus", "--project", args.project, "list", args.remote + ":", "--format=json"])
    return {item["name"]: item["status"] for item in json.loads(result)}


def stop_managed(args, names):
    if not names:
        return
    statuses = instances(args)
    for name in sorted(names, reverse=True):  # workspace before secured
        status = statuses.get(name)
        if status is None or status == "Stopped":
            continue
        if status != "Running":
            raise ValueError(f"{name} is {status}; stop it before retrying rollout")
        print(f"Stopping {name} (active sessions will end)…", flush=True)
        subprocess.run(["incus", "--project", args.project, "stop", f"{args.remote}:{name}",
                        "--timeout", "60"], check=True)


def configure_ssh(args):
    if instances(args).get("workspace") != "Running":
        print("Rollout complete; workspace is stopped by deployment settings. Start it before SSH setup.")
        return
    spec = importlib.util.spec_from_file_location("sandbox_ssh", ROOT / "scripts/sandbox-ssh.py")
    ssh = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(ssh)
    ssh.setup(SimpleNamespace(remote=args.remote, project=args.project, instance="workspace",
                              state_dir=args.ssh_state_dir, refresh_host_key=True))


def rollout(args, directory, server):
    # One lock covers download through SSH: no competing plan/apply can change the selection.
    with deployment_lock(directory):
        require_recovered(directory)
        initialize_variables(args, directory)
        names = managed_instances(directory, args.project)
        selected = copy(args)
        selected.image_dir = args.image_dir or download_release(server["architectures"][0], args.release, args.repo)
        selected.image_dir = selected.image_dir.resolve()
        image_selection(directory, selected.image_dir)
        print("Rolling out workspace; persistent volumes are protected by plan validation.", flush=True)
        stop_managed(args, names)
        selected.action, selected.replace, selected.destroy = "plan", True, False
        selected.automatic = True
        try:
            run_locked_operation(selected, directory, server)
            selected.action, selected.replace = "apply", False
            run_locked_operation(selected, directory, server)
        except BaseException:
            print("Rollout incomplete. Inspect the error before restarting containers; host state and data are retained.", flush=True)
            raise
        # Re-trust the key through authenticated Incus only after a successful apply.
        configure_ssh(args)
