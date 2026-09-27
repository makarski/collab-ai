"""Exercise the persistent two-runtime deployment in a disposable smoke project."""

import json
from pathlib import Path
import subprocess
import time

from sandbox_boundary_checks import report, stop_relay


STATE = "/var/lib/collab-ai-secured"
STATUS = "/mnt/collab-status/status.sock"
ENV = ["env", f"COLLAB_BUDGET_DIR={STATE}/budgets"]


def run(command, **kwargs):
    result = subprocess.run(command, capture_output=True, text=True, timeout=120, **kwargs)
    if result.returncode:
        raise ValueError(f"Secured smoke command failed: {command}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def denied(command):
    result = subprocess.run(command, capture_output=True, timeout=10)
    if result.returncode == 0:
        raise ValueError(f"Forbidden operation succeeded: {command}")


class Deployment:
    def __init__(self, args, directory, project):
        self.remote = args.remote + ":"
        self.base = ["incus", "--project", project]
        self.tofu = [args.tofu, f"-chdir={directory}"]

    def execute(self, role, *command, uid=0):
        return self.base + ["exec", self.remote + role, "-T", "--user", str(uid),
                            "--group", str(uid), "--", *command]

    def status(self):
        return self.execute("workspace", "env", "COLLAB_BUDGET_DIR=/tmp/decoy", "collab", "budget", "status",
                            "smoke", "--socket", STATUS, "--json", uid=1001)

    def snapshot(self):
        return json.loads(run(self.execute("secured", *ENV, "collab", "budget", "status", "smoke", "--json")))


def check_configuration(deployment):
    instances = {item["name"]: item for item in json.loads(run(
        deployment.base + ["list", deployment.remote, "--format", "json"]))}
    mappings = [instances[role]["expanded_config"]["volatile.idmap.current"]
                for role in ("workspace", "secured")]
    for role in ("workspace", "secured"):
        check_offline(deployment, role)
    if mappings[0] == mappings[1]:
        raise ValueError("Dev and secured share an identity mapping")
    devices = instances["secured"]["expanded_devices"]
    if set(devices) != {"root", "secured-state", "budget-status"}:
        raise ValueError("Secured inherited unexpected devices")
    denied(deployment.execute("secured", "systemctl", "is-active", "--quiet", "collab-broker"))


def check_offline(deployment, role):
    for path in ("/var/lib/incus/unix.socket", "/dev/incus/sock", "/var/run/docker.sock"):
        run(deployment.execute(role, "test", "!", "-S", path))
    links = json.loads(run(deployment.execute(role, "ip", "-json", "link")))
    if [link["ifname"] for link in links] != ["lo"]:
        raise ValueError(f"{role} unexpectedly has networking")


def prepare_budget(deployment):
    run(deployment.execute("secured", *ENV, "collab", "budget", "create", "smoke", "--tokens", "100"))
    source = Path(__file__).parent / "boundary/relay.py"
    run(deployment.base + ["file", "push", str(source), deployment.remote + "secured/opt/smoke-relay.py"])
    run(deployment.execute("secured", "sh", "-c",
        "printf '#!/bin/sh\nexec python3 /opt/smoke-relay.py fake-codex\n' >/opt/smoke-codex; chmod 755 /opt/smoke-codex"))
    return deployment.execute("secured", *ENV, "collab-codex", "--agent-id", "smoke", "--budget", "smoke",
                              "--budget-status-socket", STATUS, "--codex", "/opt/smoke-codex")


def await_status(deployment):
    for _ in range(30):
        result = subprocess.run(deployment.status(), capture_output=True, text=True, timeout=5)
        if result.returncode == 0:
            snapshot = json.loads(result.stdout)
            if snapshot["reported_tokens"] == 60:
                return
        time.sleep(0.2)
    raise ValueError("Read-only status socket did not become ready")


def check_denials(deployment):
    for uid in (0, 1001):
        for command in (["touch", "/mnt/collab-status/forbidden"], ["rm", STATUS],
                        ["cat", f"{STATE}/budgets/smoke.json"],
                        ["cat", f"/proc/1/root{STATE}/budgets/smoke.json"]):
            denied(deployment.execute("workspace", *command, uid=uid))
    denied(deployment.execute("workspace", "mount", "-o", "remount,rw", "/mnt/collab-status"))
    denied(deployment.execute("secured", "cat", f"{STATE}/budgets/smoke.json", uid=1001))
    denied(deployment.execute("secured", "touch", "/mnt/collab-status/forbidden", uid=1001))
    # Creating a same-name budget in dev must not change the mounted authority.
    run(deployment.execute("workspace", "env", "COLLAB_BUDGET_DIR=/tmp/decoy", "collab", "budget", "create",
                           "smoke", "--tokens", "9999", uid=1001))
    snapshot = json.loads(run(deployment.status()))
    if (snapshot["cap"], snapshot["reported_tokens"]) != (100, 60):
        raise ValueError("Dev changed the authoritative snapshot")


def check_persistence(deployment):
    for running in (False, True):
        run(deployment.tofu + ["apply", "-auto-approve", "-input=false", f"-var=running={str(running).lower()}"])
        instances = json.loads(run(deployment.base + ["list", deployment.remote, "--format", "json"]))
        expected = "Running" if running else "Stopped"
        if {item["status"] for item in instances} != {expected}:
            raise ValueError("Desired power state did not apply to both runtimes")
    check_saved_budget(deployment)
    # Replacement destroys the secured root disk; the private custom volume survives.
    run(deployment.tofu + ["apply", "-auto-approve", "-input=false", "-replace=incus_instance.secured[0]"])
    check_saved_budget(deployment)
    run(deployment.tofu + ["plan", "-input=false", "-detailed-exitcode"])


def check_saved_budget(deployment):
    snapshot = deployment.snapshot()
    if (snapshot["cap"], snapshot["reported_tokens"]) != (100, 60):
        raise ValueError("Lifecycle operation changed persisted accounting")
    denied(deployment.execute("secured", *ENV, "collab", "budget", "create", "smoke", "--tokens", "9999"))
    denied(deployment.status())


def enable_secured(deployment, directory):
    path = directory / "sandbox.auto.tfvars.json"
    variables = json.loads(path.read_text())
    variables["secured_runtime"] = True
    path.write_text(json.dumps(variables))
    run(deployment.tofu + ["apply", "-auto-approve", "-input=false"])
    run(deployment.tofu + ["plan", "-input=false", "-detailed-exitcode"])
    run(deployment.execute("workspace", "test", "-f", "/workspace/restart-check"))
    print("PASS: enabling secured runtime preserves dev data and produces an unchanged second plan.", flush=True)


def verify(args, directory, project):
    deployment = Deployment(args, directory, project)
    enable_secured(deployment, directory)
    check_configuration(deployment)
    command = prepare_budget(deployment)
    # Inherit the test log; undrained output pipes could stall the launcher.
    process = subprocess.Popen(command, stdin=subprocess.PIPE, text=True)
    try:
        report(process, 60)
        await_status(deployment)
        check_denials(deployment)
        print("PASS: read-only UDS status and private-state denials for dev root and agent.", flush=True)
    finally:
        stop_relay(process)
        run(deployment.base + ["restart", deployment.remote + "secured"])
    check_persistence(deployment)
    print("PASS: isolated runtimes, root-only persistent state, read-only status, stop/start and secured replacement.")
