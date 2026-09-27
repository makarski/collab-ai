"""Attack the public budget endpoint as the unprivileged dev user, offline."""

import http.client
import json
import os
import socket
import subprocess


SOCKET = "/mnt/proof-status/public/status.sock"


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(2)
        self.sock.connect(SOCKET)


def request(method, path):
    connection = UnixHTTP("budget", timeout=2)
    try:
        connection.request(method, path, '{"cap":9999,"role":"admin"}',
                           {"Authorization": "Bearer pretend-admin"})
        response = connection.getresponse()
        return response.status, response.read()
    finally:
        connection.close()


def check_mutations():
    # Creating a high-cap decoy in dev cannot replace the secured launcher's file.
    subprocess.run(["env", "COLLAB_BUDGET_DIR=/tmp/proof-decoy", "/usr/local/bin/collab",
                    "budget", "create", "task", "--tokens", "9999"], check=True, capture_output=True)
    for method, route in [("POST", "/v1/status"), ("DELETE", "/v1/status"), ("PUT", "/v1/status"),
                          ("POST", "/v1/create"), ("POST", "/v1/observe"), ("GET", "/v1/status?cap=9999")]:
        code, _ = request(method, route)
        if code not in (404, 405):
            raise ValueError(f"Budget mutation accepted: {method} {route}")
    code, data = request("GET", "/v1/status")
    snapshot = json.loads(data)
    if code != 200 or snapshot["cap"] != 100 or snapshot["reported_tokens"] != 60:
        raise ValueError(f"Unexpected budget after mutation attempts: {snapshot}")


def check_filesystem():
    for command in [["touch", "/mnt/proof-status/public/overwrite"], ["rm", SOCKET],
                    ["cat", "/var/lib/collab-proof/budgets/task.json"], ["cat", "/var/lib/collab-proof/private"],
                    ["cat", "/proc/1/root/var/lib/collab-proof/budgets/task.json"]]:
        if subprocess.run(command, capture_output=True).returncode == 0:
            raise ValueError(f"Dev accessed or changed protected state: {command}")
    if os.path.exists("/var/lib/incus/unix.socket"):
        raise ValueError("Incus admin socket exposed to dev")
    links = json.loads(subprocess.check_output(["ip", "-json", "link"]))
    if [link["ifname"] for link in links] != ["lo"]:
        raise ValueError("Dev proof has network access")


if __name__ == "__main__":
    check_mutations()
    check_filesystem()
    print("PASS: dev reads the fixed cap; mutation, socket replacement, state access and admin access are denied.")
