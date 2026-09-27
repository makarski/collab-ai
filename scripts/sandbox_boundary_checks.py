"""Assertions against the disposable proof's real processes and public socket."""

import json
from pathlib import Path
import subprocess
import time

from sandbox_boundary_host import run


ENV = ["env", "COLLAB_BUDGET_DIR=/var/lib/collab-proof/budgets"]
CLI = "/usr/local/bin/collab"
LAUNCHER = "/usr/local/bin/collab-codex"
STATUS = "/mnt/proof-status/public/status.sock"


def prepare_files(host):
    for role in ("secured", "dev"):
        for source in (Path(__file__).parent / "boundary").glob("*.py"):
            host.push(role, source, "/opt/proof/" + source.name)
        install_overlay(host, role)
    host.exe("secured", "mkdir", "-p", "/var/lib/collab-proof/budgets", "/mnt/proof-status/public")
    host.exe("secured", "chmod", "700", "/var/lib/collab-proof/budgets")
    host.exe("secured", "chmod", "755", "/mnt/proof-status/public")
    host.exe("secured", "sh", "-c", "printf controller-only >/var/lib/collab-proof/private")
    host.exe("secured", "sh", "-c", "printf '#!/bin/sh\nexec python3 /opt/proof/relay.py fake-codex\n' >/opt/proof/fake-codex")
    host.exe("secured", "chmod", "755", "/opt/proof/fake-codex")
    host.exe("secured", *ENV, CLI, "budget", "create", "task", "--tokens", "100")


def install_overlay(host, role):
    if host.args.bin_dir is None:
        return
    for name in ("collab", "collab-codex"):
        host.push(role, host.args.bin_dir / name, "/usr/local/bin/" + name)
        host.exe(role, "chmod", "755", "/usr/local/bin/" + name)


def dev_command(host, *command):
    return host.base + ["exec", host.remote + "dev", "--user", "1001", "--group", "1001", "--", *command]


def status_command(host):
    return dev_command(host, CLI, "budget", "status", "task", "--socket", STATUS, "--json")


def await_report(host):
    for _ in range(40):
        result = subprocess.run(status_command(host), capture_output=True, text=True, timeout=5)
        if result.returncode == 0 and json.loads(result.stdout)["reported_tokens"] == 60:
            return
        time.sleep(0.2)
    raise ValueError(f"Budget status not ready: {result.stderr}")


def await_shutdown(host):
    for _ in range(40):
        result = subprocess.run(host.execute("secured", "pgrep", "-f", "^" + LAUNCHER), capture_output=True, timeout=5)
        if result.returncode == 1:
            return
        if result.returncode != 0:
            raise ValueError("Cannot verify protected launcher shutdown")
        time.sleep(0.2)
    raise ValueError("Protected launcher did not exit after reaching its budget")


def stop_relay(process):
    if process.stdin and not process.stdin.closed:
        process.stdin.close()
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)


def report(process, tokens):
    process.stdin.write(json.dumps({"method": "proof/report", "params": {"total": tokens}}) + "\n")
    process.stdin.flush()


def check_budget(host):
    command = host.execute("secured", *ENV, LAUNCHER, "--agent-id", "proof", "--budget", "task",
                           "--budget-status-socket", STATUS, "--codex", "/opt/proof/fake-codex")
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        report(process, 60)
        await_report(host)
        print(run(dev_command(host, "python3", "/opt/proof/dev_probe.py")), end="", flush=True)
        report(process, 107)
        await_shutdown(host)
        process.stdin.close()  # Incus's host stdin relay may itself wait for EOF.
        process.wait(timeout=5)
        diagnostic = process.stderr.read()
        if process.returncode == 0 or "107 reported / 100 cap" not in diagnostic:
            raise ValueError(f"Unexpected budget shutdown: {diagnostic}")
        check_exhaustion(host, command)
    finally:
        stop_relay(process)


def check_exhaustion(host, command):
    run(host.base + ["restart", host.remote + "secured"])
    saved = json.loads(host.exe("secured", *ENV, CLI, "budget", "status", "task", "--json"))
    if saved["state"] != "exhausted" or saved["cap"] != 100 or saved["reported_tokens"] != 107:
        raise ValueError(f"Restart changed the budget: {saved}")
    retry = subprocess.run(command, input="", capture_output=True, text=True, timeout=10)
    if retry.returncode == 0 or "soft token cap reached" not in retry.stderr:
        raise ValueError("Exhausted budget permitted relaunch")
    lost = subprocess.run(status_command(host), capture_output=True, text=True, timeout=5)
    if lost.returncode == 0 or lost.stdout:
        raise ValueError("Controller loss returned a usable budget snapshot")
    print("PASS: cap stops protected fake client; exhaustion survives restart; lost status never falls back.", flush=True)


def check_native(host, kind):
    transport = "codex" if kind == "codex" else "mcp"
    process = subprocess.Popen(dev_command(host, "python3", "/opt/proof/relay.py", "serve", transport),
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        wait_for_executor(host, transport)
        print(host.exe("secured", "python3", "/opt/proof/native.py", kind), end="", flush=True)
        marker, expected = ("proof-native", "remote") if kind == "codex" else ("proof-claude", "claude-remote")
        if host.exe("dev", "cat", "/workspace/" + marker) != expected:
            raise ValueError("Native tool did not write the dev container")
    finally:
        stop_relay(process)


def wait_for_executor(host, kind):
    for _ in range(30):
        result = subprocess.run(host.execute("secured", "test", "-S", f"/mnt/proof-executor/public/{kind}.sock"),
                                capture_output=True, timeout=5)
        if result.returncode == 0:
            return
        time.sleep(0.2)
    raise ValueError(f"Native {kind} executor did not start")
