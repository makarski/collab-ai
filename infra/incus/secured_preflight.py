"""Read-only host preflight: never hot-add the status volume to a running dev."""

import http.client
import json
import os
import socket
from urllib.parse import quote


class IncusConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.host)


def require_stopped(status, payload):
    if status == 404:
        return  # Fresh project/instance: the volume will be present at first boot.
    if status != 200:
        raise ValueError(f"Cannot inspect workspace power state (Incus HTTP {status})")
    if payload.get("metadata", {}).get("status") != "Stopped":
        raise ValueError("Stop workspace before enabling secured_runtime; hot-added read-only mounts can be remounted writable. "
                         "Keep it stopped until the apply succeeds. The apply starts it again when running=true.")


def main():
    connection = IncusConnection(os.environ["COLLAB_INCUS_SOCKET"], timeout=10)
    try:
        project = quote(os.environ["COLLAB_INCUS_PROJECT"], safe="")
        connection.request("GET", f"/1.0/instances/workspace?project={project}", headers={"Host": "incus"})
        response = connection.getresponse()
        data = response.read(1048577)
        if len(data) > 1048576:
            raise ValueError("Incus workspace metadata exceeds the preflight limit")
        payload = json.loads(data) if response.status == 200 else {}
        require_stopped(response.status, payload)
    finally:
        connection.close()


if __name__ == "__main__":
    main()
