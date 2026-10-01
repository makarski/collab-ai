"""Exercise installed MCP servers offline, as agent, without credentials/models."""

from contextlib import ExitStack
import http.server
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import tempfile
import threading
import time
import tomllib


class Client:
    def __init__(self, command, home):
        env = dict(os.environ, HOME=str(home), XDG_CONFIG_HOME=str(home / ".config"),
                   XDG_CACHE_HOME=str(home / ".cache"), CS_CONFIG_DIR=str(home / ".codescene"))
        for key in ("CS_ACCESS_TOKEN", "CS_OAUTH_TOKEN"):
            env.pop(key, None)
        self.process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, env=env, cwd="/workspace", start_new_session=True)
        self.selector = selectors.DefaultSelector()
        self.selector.register(self.process.stdout, selectors.EVENT_READ)
        self.pending = b""
        self.sequence = 0

    def send(self, data):
        self.process.stdin.write((json.dumps(dict(jsonrpc="2.0", **data)) + "\n").encode())
        self.process.stdin.flush()

    def receive(self, deadline):
        while b"\n" not in self.pending:
            if not self.selector.select(max(0, deadline - time.monotonic())):
                raise ValueError("MCP response timed out")
            chunk = os.read(self.process.stdout.fileno(), 65536)
            if not chunk:
                raise ValueError("MCP server closed stdout")
            self.pending += chunk
        line, self.pending = self.pending.split(b"\n", 1)
        return json.loads(line)

    def call(self, method, params):
        self.sequence += 1
        self.send({"id": self.sequence, "method": method, "params": params})
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            response = self.receive(deadline)
            if response.get("method") == "roots/list":
                self.send({"id": response["id"], "result": {"roots": [{"uri": "file:///workspace"}]}})
            elif response.get("id") == self.sequence:
                return rpc_result(response, method)
        raise ValueError(f"MCP {method} timed out")

    def initialize(self):
        self.call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {"roots": {}},
                                "clientInfo": {"name": "collab-smoke", "version": "1"}})
        self.send({"method": "notifications/initialized"})
        return {tool["name"] for tool in self.call("tools/list", {})["tools"]}

    def tool(self, name, arguments):
        result = self.call("tools/call", {"name": name, "arguments": arguments})
        if result.get("isError"):
            raise ValueError(f"MCP {name} failed: {result}")
        return json.dumps(result)

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(self.process.pid, signal.SIGKILL)
            self.process.wait()
        self.selector.close()
        self.process.stdout.close()


def rpc_result(response, method):
    if "error" in response:
        raise ValueError(f"MCP {method} failed: {response['error']}")
    return response["result"]


class Page(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.end_headers()
        self.wfile.write(b"<title>Collab browser proof</title><h1>Local sandbox app</h1>")

    def log_message(self, *_):
        pass


def client(stack, command, home):
    connection = Client(command, home)
    stack.callback(connection.close)
    return connection, connection.initialize()


def verify_configuration():
    claude = json.loads(Path("/etc/collab-ai/claude-mcp.json").read_text())["mcpServers"]
    codex = tomllib.loads(Path("/etc/codex/config.toml").read_text())["mcp_servers"]
    for name in ("codescene", "playwright"):
        if claude[name] != codex[name]:
            raise ValueError(f"MCP configuration differs between agents: {name}")
    if "collab" not in claude:
        raise ValueError("Bundled MCPs displaced Claude's collaboration channel")
    return claude


def browser_proof(stack, config, home):
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Page)
    stack.callback(server.server_close)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    stack.callback(server.shutdown)
    url = f"http://127.0.0.1:{server.server_port}/"
    first, tools = client(stack, [config["command"], *config["args"]], home)
    if "browser_navigate" not in tools:
        raise ValueError("Browser navigation tool missing")
    if "Collab browser proof" not in first.tool("browser_navigate", {"url": url}):
        raise ValueError("Bundled browser did not reach the local app")
    first.tool("browser_evaluate", {"function": "() => { document.cookie = 'proof=one'; return document.cookie; }"})
    second, _ = client(stack, [config["command"], *config["args"]], home)
    second.tool("browser_navigate", {"url": url})
    if "proof=one" in second.tool("browser_evaluate", {"function": "() => document.cookie"}):
        raise ValueError("Parallel agent browsers shared cookies")
    first.tool("browser_close", {})
    second.tool("browser_close", {})


def verify():
    if os.getuid() != 1001:
        raise ValueError("MCP proof must run as agent")
    config = verify_configuration()
    with ExitStack() as stack:
        home = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="collab-mcp-proof-")))
        _, tools = client(stack, [config["codescene"]["command"]], home)
        if not {"code_health_review", "pre_commit_code_health_safeguard"} <= tools:
            raise ValueError("CodeScene review tools missing")
        browser_proof(stack, config["playwright"], home)
    print("PASS: both agent MCP configurations, unauthenticated CodeScene discovery, sandboxed Chromium and isolated browser sessions.")


verify()
