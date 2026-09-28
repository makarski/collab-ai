#!/usr/bin/env python3
"""Exercise real dev adapters with a fake App Server and no model requests."""

from contextlib import ExitStack
import json
from pathlib import Path
import queue
import subprocess
import sys
import threading
import time
import uuid


def emit(frame):
    print(json.dumps(frame), flush=True)


def fake_codex():
    for line in sys.stdin:
        request = json.loads(line)
        method = request.get("method")
        if method == "thread/start":
            emit({"id": request["id"], "result": {"thread": {"id": "socket-proof"}}})
        elif method == "turn/start":
            emit({"id": request["id"], "result": {"turn": {"id": "peer-turn"}}})
            peer = json.loads(request["params"]["toolOutput"]["output"])
            emit({"method": "proof/received", "params": peer})
            emit({"id": "reply-tool", "method": "item/tool/call", "params": {
                "threadId": "socket-proof", "tool": "collab_send", "arguments": {
                    "to": peer["from"], "text": "reply from dev Codex", "in_reply_to": peer["message_id"]}}})
        elif method and "id" in request:
            emit({"id": request["id"], "result": {}})


class Peer:
    def __init__(self, command):
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        self.frames = queue.Queue()
        self.sequence = 0
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        for line in self.process.stdout:
            self.frames.put(json.loads(line))
        self.frames.put(None)

    def send(self, frame):
        self.process.stdin.write(json.dumps(frame) + "\n")
        self.process.stdin.flush()

    def until(self, predicate):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            frame = self.frames.get(timeout=max(0.01, deadline - time.monotonic()))
            if frame is None:
                raise ValueError("Adapter exited before expected response")
            if predicate(frame):
                return frame
        raise ValueError("Adapter response deadline exceeded")

    def call(self, method, params):
        self.sequence += 1
        self.send({"jsonrpc": "2.0", "id": self.sequence, "method": method, "params": params})
        frame = self.until(lambda item: item.get("id") == self.sequence)
        if "error" in frame or frame.get("result", {}).get("isError"):
            raise ValueError(f"Adapter rejected request: {frame}")
        return frame["result"]

    def tool(self, name, arguments):
        result = self.call("tools/call", {"name": name, "arguments": arguments})
        return json.loads(result["content"][0]["text"])

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.process.stdout.close()


def await_registration(identity):
    for _ in range(30):
        result = subprocess.run(["collab", "status", "--json"], check=True, capture_output=True, text=True)
        if any(item["agent_id"] == identity and item["state"] == "transport_connected"
               for item in json.loads(result.stdout)["sessions"]):
            return
        time.sleep(0.1)
    raise ValueError("Codex listener did not register with the shared broker")


def verify():
    suffix = uuid.uuid4().hex[:8]
    claude_id, codex_id = "claude-" + suffix, "codex-" + suffix
    with ExitStack() as cleanup:
        claude = Peer(["collab-mcp", "--agent-id", claude_id])
        cleanup.callback(claude.close)
        claude.call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                  "clientInfo": {"name": "offline-proof", "version": "1"}})
        claude.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        if not claude.tool("receive", {})["connected"]:
            raise ValueError("MCP adapter did not connect")
        codex = Peer(["collab-codex", "--agent-id", codex_id, "--codex", str(Path(__file__).resolve())])
        cleanup.callback(codex.close)
        codex.call("initialize", {})
        codex.send({"method": "initialized"})
        codex.call("thread/start", {})
        await_registration(codex_id)
        sent = claude.tool("send", {"to": codex_id, "text": "hello from dev Claude adapter"})
        received = codex.until(lambda frame: frame.get("method") == "proof/received")["params"]
        if received["message_id"] != sent["message_id"] or received["from"] != claude_id:
            raise ValueError("Codex received the wrong peer context")
        await_reply(claude, codex_id, sent["message_id"])
    print("PASS: dev MCP -> shared broker -> managed Codex listener -> correlated MCP reply.", flush=True)


def await_reply(claude, codex_id, message_id):
    for _ in range(10):
        inbox = claude.tool("wait", {"timeout_seconds": 1})
        for message in inbox["messages"]:
            if message.get("from") == codex_id and message.get("in_reply_to") == message_id:
                return
    raise ValueError("Codex reply did not reach the dev MCP adapter")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "app-server":
        fake_codex()
    else:
        verify()
