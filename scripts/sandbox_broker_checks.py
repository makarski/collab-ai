"""Prove a single shared broker across isolated containers, including restart."""

import json
from pathlib import Path
import subprocess
import time

from sandbox_installed_checks import denied, run


IPC = "/mnt/collab-ipc"
SOCKET = IPC + "/broker.sock"
DATABASE = "/var/lib/collab-ai-secured/broker/broker.db"


def agent(deployment, role, *command):
    # Load supplementary collab-clients membership, as a normal SSH login does.
    return deployment.execute(role, "runuser", "-u", "agent", "--", *command)


def snapshot(deployment, role):
    return json.loads(run(agent(deployment, role, "collab", "status", "--json")))


def await_broker(deployment):
    for _ in range(40):
        result = subprocess.run(agent(deployment, "workspace", "collab", "status", "--json"),
                                capture_output=True, text=True, timeout=5)
        if result.returncode == 0 and json.loads(result.stdout)["health"] == "ready":
            return
        time.sleep(0.25)
    diagnostic = run(deployment.execute("secured", "journalctl", "-b", "--no-pager", "-n", "80"))
    raise ValueError(f"Shared broker did not become ready: {diagnostic}")


def check_permissions(deployment):
    for role in ("workspace", "secured"):
        for path, expected in ((IPC, "750 1002 1003"), (SOCKET, "660 1002 1003")):
            actual = run(deployment.execute(role, "stat", "-c", "%a %u %g", path)).strip()
            if actual != expected:
                raise ValueError(f"Unsafe broker endpoint in {role}: {path}: {actual}")
        denied(agent(deployment, role, "rm", SOCKET))
        denied(agent(deployment, role, "ln", "-sf", "/tmp/evil.sock", SOCKET))
        denied(agent(deployment, role, "cat", DATABASE))
        denied(deployment.execute(role, "collab", "status", "--socket", SOCKET, uid=65534))
    denied(deployment.execute("workspace", "cat", DATABASE))
    denied(deployment.execute("workspace", "touch", IPC + "/forbidden"))
    for options in ("remount,rw", "remount,bind,rw"):
        denied(deployment.execute("workspace", "mount", "-o", options, IPC))
    denied(deployment.execute("workspace", "systemctl", "is-active", "--quiet", "collab-broker"))
    denied(deployment.execute("workspace", "pgrep", "-x", "broker"))
    pids = run(deployment.execute("secured", "pgrep", "-x", "broker")).splitlines()
    if len(pids) != 1:
        raise ValueError("Control must run exactly one broker")
    run(deployment.execute("secured", "test", "-f", DATABASE))


def exercise(deployment):
    source = Path(__file__).parent / "broker/adapters.py"
    target = "/opt/broker-adapters.py"
    run(deployment.base + ["file", "push", str(source), deployment.remote + "workspace" + target])
    run(deployment.execute("workspace", "chmod", "0755", target))
    print(run(agent(deployment, "workspace", "python3", target)), end="", flush=True)
    dev, control = snapshot(deployment, "workspace"), snapshot(deployment, "secured")
    if dev["broker_started_at"] != control["broker_started_at"] or dev["durable_pending"] != control["durable_pending"]:
        raise ValueError("Dev and control observed different broker state")
    return dev["durable_pending"]["total"]


def verify(deployment):
    await_broker(deployment)
    check_permissions(deployment)
    pending = exercise(deployment)
    run(deployment.execute("secured", "systemctl", "stop", "collab-secured-broker"))
    try:
        denied(agent(deployment, "workspace", "collab", "status", "--json"))
        denied(deployment.execute("workspace", "pgrep", "-x", "broker"))
    finally:
        run(deployment.execute("secured", "systemctl", "start", "collab-secured-broker"))
    await_broker(deployment)
    # SIGKILL leaves a stale socket on the persistent IPC volume. The service
    # must recover it without removing an active or substituted endpoint.
    run(deployment.execute("secured", "systemctl", "kill", "--kill-who=main", "--signal=SIGKILL", "collab-secured-broker"))
    time.sleep(0.5)
    await_broker(deployment)
    check_permissions(deployment)
    if snapshot(deployment, "workspace")["durable_pending"]["total"] != pending:
        raise ValueError("Broker restart lost persisted pending messages")
    exercise(deployment)
    print("PASS: one broker/SQLite, shared socket groups, read-only mount, private DB, no fallback and crash restart.", flush=True)
