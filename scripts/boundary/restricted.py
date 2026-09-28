"""Native App Server + restricted ingress + real budget, with loopback fixtures only."""

import http.server
import json
from pathlib import Path
import queue
import subprocess
import threading

from models import Model, codex_events
from native import codex_command


def restricted_events(number):
    events = codex_events(number)
    if number == 2:
        events[1]["item"].update(name="collab_listener_status", arguments="{}")
    return events


class Operator:
    def __init__(self, process):
        self.process = process
        self.frames = queue.Queue()
        self.transcript = []
        self.sequence = 0
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()

    def read(self):
        try:
            for line in self.process.stdout:
                self.frames.put(json.loads(line))
        finally:
            self.frames.put(None)

    def send(self, frame):
        self.process.stdin.write(json.dumps(frame) + "\n")
        self.process.stdin.flush()

    def until(self, predicate):
        while True:
            frame = self.frames.get(timeout=30)
            if frame is None:
                raise ValueError("Restricted launcher exited before expected response")
            self.transcript.append(frame)
            if predicate(frame):
                return frame

    def call(self, method, params):
        self.sequence += 1
        request_id = self.sequence
        self.send({"id": request_id, "method": method, "params": params})
        return self.until(lambda frame: frame.get("id") == request_id)

    def denied(self, method, params):
        if "error" not in self.call(method, params):
            raise ValueError(f"Restricted ingress accepted {method}")


def exercise(operator):
    if "error" in operator.call("initialize", {}):
        raise ValueError("Native initialize failed")
    operator.send({"method": "initialized"})
    operator.denied("command/exec", {"command": ["sh", "-c", "touch /var/lib/collab-proof/escaped"]})
    operator.denied("config/value/write", {"keyPath": "features.hooks", "value": True})
    operator.denied("thread/start", {"config": {"mcp_servers.evil.command": "sh"}})
    operator.denied("thread/tokenUsage/updated", {"totalTokens": 0})
    started = operator.call("thread/start", {})
    if "error" in started:
        raise ValueError(f"Native thread failed: {started}")
    identity = started["result"]["thread"]["id"]
    operator.denied("thread/resume", {"threadId": identity})
    operator.denied("turn/start", {"threadId": identity, "input": [{"type": "localImage", "path": "/private"}]})
    operator.denied("turn/start", {"threadId": "foreign", "input": [{"type": "text", "text": "run"}]})
    response = operator.call("turn/start", {"threadId": identity, "input": [
        {"type": "text", "text": "Execute the deterministic offline fixture."}]})
    if "error" in response:
        raise ValueError(f"Native turn failed: {response}")


def run_client(directory, environment, name, exhaust):
    command = ["collab-codex", "--restricted-operator", "--budget", name, "--agent-id", name]
    with (directory / "stderr.log").open("w+") as diagnostic:
        process = subprocess.Popen(command, cwd="/workspace", env=environment, stdin=subprocess.PIPE,
                                   stdout=subprocess.PIPE, stderr=diagnostic, text=True)
        operator = Operator(process)
        try:
            exercise(operator)
            await_session(operator, exhaust)
            diagnostic.seek(0)
            check_exit(process, diagnostic.read(), exhaust)
            (directory / "transcript.json").write_text(json.dumps(operator.transcript))
        finally:
            stop_client(process)
    return command


def await_session(operator, exhaust):
    if not exhaust:
        operator.until(lambda frame: frame.get("method") == "turn/completed")
        operator.process.stdin.close()
    operator.process.wait(timeout=15)  # On exhaustion, keep stdin open.
    operator.reader.join(timeout=5)


def check_exit(process, diagnostic, exhaust):
    if exhaust:
        if process.returncode == 0 or "soft token cap reached" not in diagnostic:
            raise ValueError(f"Native budget did not stop the session: {diagnostic}")
    elif process.returncode:
        raise ValueError(f"Restricted native session failed: {diagnostic}")


def stop_client(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    process.stdin.close()


def check_accounting(environment, command, name, exhaust):
    result = subprocess.run(["collab", "budget", "status", name, "--json"], env=environment,
                            capture_output=True, text=True, check=True, timeout=5)
    saved = json.loads(result.stdout)
    if saved["reported_tokens"] < (40 if exhaust else 60):
        raise ValueError(f"Native accounting missing: {saved}")
    if exhaust:
        retry = subprocess.run(command, env=environment, input="", capture_output=True, text=True, timeout=10)
        if retry.returncode == 0 or "soft token cap reached" not in retry.stderr:
            raise ValueError("Exhausted native session permitted relaunch")


def scenario(name, cap):
    directory = Path("/var/lib") / name
    directory.mkdir(mode=0o700)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    server.observed, server.events = [], restricted_events
    threading.Thread(target=server.serve_forever, daemon=True).start()
    _, environment = codex_command(directory, server.server_port)
    environment.update({"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": str(directory),
                        "COLLAB_BUDGET_DIR": str(directory / "budgets")})
    try:
        subprocess.run(["collab", "budget", "create", name, "--tokens", str(cap)],
                       env=environment, check=True, capture_output=True, timeout=5)
        command = run_client(directory, environment, name, cap == 30)
        check_accounting(environment, command, name, cap == 30)
        verify_boundary()
        if cap == 100:
            verify_collaboration(server)
        print(f"PASS: restricted native session {name}, persisted accounting and ingress denials.", flush=True)
    finally:
        server.shutdown()
        server.server_close()


def verify_boundary():
    if Path("/var/lib/collab-proof/escaped").exists():
        raise ValueError("Operator command escaped into secured runtime")
    if Path("/var/lib/collab-proof/private").read_text() != "controller-only":
        raise ValueError("Native command changed private state")
    if Path("/workspace/proof-native").exists():
        raise ValueError("Native command ran in secured runtime")


def verify_collaboration(server):
    if len(server.observed) != 3:
        raise ValueError("Native fixture did not finish its tool sequence")
    require_tool_output(server.observed[-1], "isolated")
    require_tool_output(server.observed[-1], "listening_delivery_unconfirmed")


def require_tool_output(request, expected):
    outputs = [item.get("output") for item in request.get("input", [])
               if item.get("type") == "function_call_output"]
    if not any(expected in json.dumps(output) for output in outputs):
        raise ValueError(f"Native client did not receive tool output: {expected}")


if __name__ == "__main__":
    scenario("restricted-complete", 100)
    scenario("restricted-exhaust", 30)
