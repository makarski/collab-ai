"""Verify the shipped protected launcher against the provisioned pair of runtimes."""

from pathlib import Path
import subprocess
import time


SOCKET = "/mnt/collab-executor/codex.sock"


def run(command):
    result = subprocess.run(command, capture_output=True, text=True, timeout=120)
    if result.returncode:
        raise ValueError(f"Installed runtime proof failed: {command}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def denied(command):
    if subprocess.run(command, capture_output=True, timeout=10).returncode == 0:
        raise ValueError(f"Installed runtime accepted forbidden operation: {command}")


def await_services(deployment):
    for role, unit in (("workspace", "collab-dev-executor.socket"), ("secured", "collab-secured-broker")):
        for _ in range(40):
            result = subprocess.run(deployment.execute(role, "systemctl", "is-active", "--quiet", unit),
                                    capture_output=True, timeout=5)
            if result.returncode == 0:
                break
            time.sleep(0.25)
        else:
            diagnostic = run(deployment.execute(role, "journalctl", "-b", "--no-pager", "-n", "60"))
            raise ValueError(f"Installed {unit} did not start: {diagnostic}")


def check_endpoint(deployment):
    for role in ("workspace", "secured"):
        actual = run(deployment.execute(role, "stat", "-c", "%a %u %g", SOCKET)).strip()
        if actual != "600 0 0":
            raise ValueError(f"Unsafe executor socket in {role}: {actual}")
        denied(deployment.execute(role, "rm", SOCKET, uid=1001))
        denied(deployment.execute(role, "ln", "-sf", "/tmp/evil.sock", SOCKET, uid=1001))
        connect = "import socket; s=socket.socket(socket.AF_UNIX); s.connect('/mnt/collab-executor/codex.sock')"
        denied(deployment.execute(role, "python3", "-c", connect, uid=1001))
    denied(deployment.execute("secured", "touch", "/mnt/collab-executor/forbidden"))
    for options in ("remount,rw", "remount,bind,rw"):
        denied(deployment.execute("secured", "mount", "-o", options, "/mnt/collab-executor"))
    denied(deployment.execute("workspace", "sh", "-c", "echo evil >>/usr/local/bin/codex", uid=1001))
    denied(deployment.execute("secured", "sh", "-c",
        "echo evil >>/var/lib/collab-ai-secured/codex/environments.toml", uid=1001))


def fixture(deployment, name, missing=False):
    args = ["python3", "/opt/installed-proof/installed.py", name]
    if missing:
        args.append("missing")
    print(run(deployment.execute("secured", *args)), end="", flush=True)
    if not missing:
        actual = run(deployment.execute("workspace", "stat", "-c", "%u", "/workspace/proof-native")).strip()
        if actual != "1001":
            raise ValueError(f"Dev command did not run as agent: uid={actual}")


def verify(deployment):
    await_services(deployment)
    check_endpoint(deployment)
    run(deployment.execute("secured", "mkdir", "/opt/installed-proof"))
    for name in ("installed.py", "models.py", "restricted.py", "native.py"):
        run(deployment.base + ["file", "push", str(Path(__file__).parent / "boundary" / name),
                              deployment.remote + "secured/opt/installed-proof/" + name])
    fixture(deployment, "installed-first")
    run(deployment.base + ["restart", deployment.remote + "workspace", deployment.remote + "secured"])
    await_services(deployment)
    check_endpoint(deployment)
    fixture(deployment, "installed-restart")
    run(deployment.execute("workspace", "systemctl", "stop", "collab-dev-executor.socket"))
    try:
        fixture(deployment, "installed-missing", missing=True)
    finally:
        run(deployment.execute("workspace", "systemctl", "start", "collab-dev-executor.socket"))
    print("PASS: provisioned configuration/broker/executor, restart, endpoint denials and missing-executor refusal.", flush=True)
