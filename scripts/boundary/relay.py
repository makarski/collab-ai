#!/usr/bin/env python3
"""Offline proof fixture: bridge stdio to a Unix socket, never an Incus API."""

import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import threading


def connection_worker(connection, command):
    with connection:
        subprocess.run(command, stdin=connection, stdout=connection, check=False)


def serve(kind):
    directory = Path("/mnt/proof-executor/public")
    directory.mkdir(mode=0o755, exist_ok=True)
    path = directory / f"{kind}.sock"
    command = ["codex", "exec-server", "--listen", "stdio"]
    if kind == "mcp":
        command = [sys.executable, __file__, "mcp"]
    with socket.socket(socket.AF_UNIX) as listener:
        listener.bind(str(path))
        path.chmod(0o666)
        listener.listen(4)
        while True:
            connection, _ = listener.accept()
            threading.Thread(target=connection_worker, args=(connection, command), daemon=True).start()


def copy_input(connection):
    while data := os.read(0, 65536):
        connection.sendall(data)
    connection.shutdown(socket.SHUT_WR)


def client(kind):
    with socket.socket(socket.AF_UNIX) as connection:
        connection.connect(f"/mnt/proof-executor/public/{kind}.sock")
        threading.Thread(target=copy_input, args=(connection,), daemon=True).start()
        while data := connection.recv(65536):
            os.write(1, data)


def mcp_result(request):
    method = request.get("method")
    if method == "initialize":
        return {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                "serverInfo": {"name": "offline-dev-proof", "version": "1"}}
    if method == "tools/list":
        return {"tools": [{"name": "run_command", "description": "Run in the isolated dev container",
                           "inputSchema": {"type": "object", "properties": {"command": {"type": "string"}},
                                           "required": ["command"], "additionalProperties": False}}]}
    if method == "tools/call":
        return execute_tool(request["params"])
    return {}


def execute_tool(params):
    if params["name"] != "run_command":
        raise ValueError("Unknown proof tool")
    output = subprocess.run(["/bin/sh", "-c", params["arguments"]["command"]], cwd="/workspace",
                            capture_output=True, text=True, timeout=5, check=False)
    return {"content": [{"type": "text", "text": output.stdout + output.stderr}],
            "isError": output.returncode != 0}


def mcp():
    for line in sys.stdin:
        request = json.loads(line)
        if "id" in request:
            print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": mcp_result(request)}), flush=True)


def fake_codex():
    for line in sys.stdin:
        request = json.loads(line)
        if request.get("method") == "proof/report":
            print(json.dumps({"method": "thread/tokenUsage/updated", "params": {
                "threadId": "proof", "turnId": "proof-turn", "tokenUsage": {
                    "total": {"totalTokens": request["params"]["total"]}}}}), flush=True)
        elif request.get("method") == "turn/interrupt":
            print(json.dumps({"id": request["id"], "result": {}}), flush=True)


if __name__ == "__main__":
    actions = {"mcp": mcp, "fake-codex": fake_codex}
    if sys.argv[1] in actions:
        actions[sys.argv[1]]()
    elif sys.argv[1] == "serve":
        serve(sys.argv[2])
    else:
        client(sys.argv[2])
