"""Kill a real supervised native client while its offline provider is blocked."""

import http.server
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import threading
import time

from native import codex_command
from restricted import Operator, exercise, stop_client


ROOT = Path("/var/lib/collab-ai-secured")
UNIT = "collab-codex-crash-proof.service"


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=10).stdout.strip()


class HeldProvider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        self.server.called.set()
        self.server.release.wait(30)


def status():
    return json.loads(run("env", "COLLAB_BUDGET_DIR=" + str(ROOT / "budgets"),
                          "collab", "budget", "status", "crash-proof", "--json"))


def await_empty(group):
    path = Path("/sys/fs/cgroup" + group) / "cgroup.procs"
    for _ in range(50):
        if not path.exists() or not path.read_text().strip():
            return
        time.sleep(0.1)
    raise ValueError("Native processes survived supervisor death")


def kill_supervisor(process, unit=UNIT):
    group = run("systemctl", "show", unit, "--property=ControlGroup", "--value")
    if not group.startswith("/system.slice/collab-codex-"):
        raise ValueError(f"Unexpected supervision cgroup: {group}")
    members = (Path("/sys/fs/cgroup" + group) / "cgroup.procs").read_text().split()
    if len(members) < 2:
        raise ValueError("Proof did not contain both supervisor and native client")
    run("systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", unit)
    process.wait(timeout=10)
    if process.returncode == 0:
        raise ValueError("SIGKILL was reported as a successful session")
    await_empty(group)


def check_restart():
    saved = status()
    if saved["state"] != "supervised_unfinished" or not saved["session_unfinished"]:
        raise ValueError(f"Crash lost its durable uncertainty marker: {saved}")
    result = subprocess.run(service_command("crash-proof"), input="",
                            capture_output=True, text=True, timeout=10)
    if result.returncode == 0 or "unfinished supervised session" not in result.stderr:
        raise ValueError(f"Interrupted budget allowed relaunch: {result.stderr}")


def crash_session(server):
    command = service_command("crash-proof")
    with (ROOT / "crash-stderr.log").open("w+") as diagnostic:
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=diagnostic, text=True)
        try:
            exercise(Operator(process))
            if not server.called.wait(10):
                raise ValueError("Native client did not reach the offline provider")
            if not status().get("session_unfinished"):
                raise ValueError("Client started before its budget interruption marker")
            kill_supervisor(process)
        except Exception:
            diagnostic.seek(0)
            print(diagnostic.read(), flush=True)
            raise
        finally:
            stop_client(process)
            subprocess.run(["systemctl", "stop", UNIT], capture_output=True, timeout=10)


def main():
    Path("/mnt/collab-status").mkdir(mode=0o755, exist_ok=True)
    (ROOT / "codex").mkdir(parents=True, mode=0o700)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), HeldProvider)
    server.called, server.release = threading.Event(), threading.Event()
    threading.Thread(target=server.serve_forever, daemon=True).start()
    codex_command(ROOT / "codex", server.server_port)
    run("env", "COLLAB_BUDGET_DIR=" + str(ROOT / "budgets"),
        "collab", "budget", "create", "crash-proof", "--tokens", "100")
    broker = start_broker()
    try:
        crash_session(server)
        check_restart()
        prove_orphan_cleanup()
        print("PASS: supervisor SIGKILL stops the native cgroup; unfinished accounting blocks relaunch.", flush=True)
    finally:
        broker.terminate()
        broker.wait(timeout=5)
        server.release.set()
        server.shutdown()
        server.server_close()


def start_broker():
    directory = Path("/run/collab-ai")
    directory.mkdir(mode=0o700, exist_ok=True)
    process = subprocess.Popen(["broker"], env={"PATH": "/usr/local/bin:/usr/bin:/bin",
        "COLLAB_SOCKET_PATH": str(directory / "broker.sock"), "COLLAB_DB_PATH": str(ROOT / "broker.db")},
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(50):
        if (directory / "broker.sock").is_socket():
            return process
        time.sleep(0.1)
    process.terminate()
    process.wait(timeout=5)
    raise ValueError("Secured fixture broker did not start")


def prove_orphan_cleanup():
    args = service_command("orphan-proof")
    args = args[:args.index("/usr/local/bin/collab-codex")]
    unit = "collab-codex-orphan-proof.service"
    with open(os.devnull, "w") as log:
        process = subprocess.Popen(args + ["/usr/bin/python3", "/opt/proof/orphan.py", "parent"],
                                   stdin=subprocess.PIPE, stdout=log, stderr=log)
        try:
            child = await_orphan()
            parent = int(run("systemctl", "show", unit, "--property=MainPID", "--value"))
            if os.getpgid(child) == os.getpgid(parent):
                raise ValueError("Orphan fixture did not escape the original process group")
            kill_supervisor(process, unit)
            print("PASS: systemd kills a setsid descendant that ignores SIGTERM.", flush=True)
        finally:
            subprocess.run(["systemctl", "stop", unit], capture_output=True, timeout=10)
            stop_client(process)


def await_orphan():
    path = ROOT / "orphan.pid"
    for _ in range(50):
        if path.exists() and path.read_text():
            return int(path.read_text())
        time.sleep(0.1)
    raise ValueError("Orphan fixture did not start")


def service_command(name):
    # This standalone cgroup proof uses its own executor/configuration fixtures.
    # Installed launcher preflight is exercised separately by the provisioning test.
    return runpy.run_path("/usr/local/bin/collab-supervised-codex")["command"](name)


if __name__ == "__main__":
    if sys.argv[1:] == ["check-restart"]:
        check_restart()
    else:
        main()
