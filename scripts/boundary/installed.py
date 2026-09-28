"""Offline native proof using only provisioned configuration, broker and executor."""

import http.server
import json
import shlex
from pathlib import Path
import subprocess
import sys
import threading

from models import Model
from restricted import Operator, await_session, check_exit, exercise, restricted_events, require_tool_output, stop_client, verify_boundary, verify_collaboration


ROOT = Path("/var/lib/collab-ai-secured")


def installed_events(number, name):
    events = restricted_events(number)
    if number == 1:
        item = events[1]["item"]
        args = json.loads(item["arguments"])
        args["cmd"] += "; collab budget status " + shlex.quote(name) + " --socket " + \
                       shlex.quote("/mnt/collab-status/" + name + ".sock") + " --json"
        check = 'import json,sys; assert json.load(sys.stdin)["cap"] == 100; print("protected-cap-100")'
        args["cmd"] += " | python3 -c " + shlex.quote(check)
        item["arguments"] = json.dumps(args)
    return events


def budget(*args):
    return subprocess.run(["collab", "budget", *args], env={"PATH": "/usr/local/bin:/usr/bin:/bin",
                          "COLLAB_BUDGET_DIR": str(ROOT / "budgets")},
                          capture_output=True, text=True, check=True, timeout=10).stdout


def run_session(name, missing):
    if missing:
        require_missing_executor(name)
        return
    with (ROOT / "installed-proof.log").open("w+") as log:
        process = subprocess.Popen(["collab-supervised-codex", name], stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE, stderr=log, text=True)
        try:
            operator = Operator(process)
            exercise(operator)
            await_session(operator, False)
            log.seek(0)
            check_exit(process, log.read(), False)
        except Exception:
            log.seek(0)
            print(log.read(), flush=True)
            raise
        finally:
            stop_client(process)


def require_missing_executor(name):
    result = subprocess.run(["collab-supervised-codex", name], input="", capture_output=True,
                            text=True, timeout=15)
    if result.returncode == 0 or "Dev executor unavailable" not in result.stderr:
        raise ValueError(f"Missing dev executor did not prevent launch: {result.stderr}")


def main(name, missing):
    directory = Path("/var/lib/collab-proof")
    directory.mkdir(mode=0o700, exist_ok=True)
    (directory / "private").write_text("controller-only")
    budget("create", name, "--tokens", "100")
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 18080), Model)
    server.observed, server.events = [], lambda number: installed_events(number, name)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        run_session(name, missing)
        verify_boundary()
        verify_observations(server, name, missing)
        print(f"PASS: installed protected session {name}; missing executor={missing}.", flush=True)
    finally:
        server.shutdown()
        server.server_close()


def verify_observations(server, name, missing):
    if missing:
        verify_unstarted(server, name)
        return
    verify_collaboration(server)
    require_tool_output(server.observed[-1], "protected-cap-100")
    saved = json.loads(budget("status", name, "--json"))
    if saved["reported_tokens"] != 60 or saved["state"] != "ready":
        raise ValueError(f"Installed session lost its accounting: {saved}")


def verify_unstarted(server, name):
    if server.observed:
        raise ValueError("Missing executor reached the model fixture")
    saved = json.loads(budget("status", name, "--json"))
    if saved["reported_tokens"] != 0 or saved["state"] != "ready":
        raise ValueError("Executor preflight started the budget session")


if __name__ == "__main__":
    main(sys.argv[1], len(sys.argv) > 2)
