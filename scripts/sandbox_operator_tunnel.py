"""Expose a host-loopback state backend only inside the operator container."""

from contextlib import contextmanager
from pathlib import Path
import platform
import subprocess
import tempfile
import time
import uuid


@contextmanager
def state_connection(port, remote):
    if platform.system() == "Linux" and remote == "local":
        yield f"tcp:127.0.0.1:{port}"
        return
    if platform.system() != "Darwin" or remote != "colima-collab-ai":
        raise ValueError("Provisioning supports Linux local or macOS colima-collab-ai")
    with tempfile.TemporaryDirectory(prefix="collab-operator-ssh-") as directory:
        config = Path(directory) / "config"
        config.write_bytes(subprocess.check_output(["colima", "ssh-config", "collab-ai"]))
        config.chmod(0o600)
        socket = f"/tmp/collab-operator-{uuid.uuid4().hex}.sock"
        command = ["ssh", "-F", str(config), "-N", "-T", "-o", "BatchMode=yes",
                   "-o", "ForwardAgent=no", "-o", "ControlMaster=no", "-o", "ControlPath=none",
                   "-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindMask=0177",
                   "-R", f"{socket}:127.0.0.1:{port}", "colima-collab-ai"]
        proc = subprocess.Popen(command, stdin=subprocess.DEVNULL)
        try:
            wait_for_socket(proc, socket)
            yield f"unix:{socket}"
        finally:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
            subprocess.run(["colima", "-p", "collab-ai", "ssh", "--", "rm", "-f", "--", socket],
                           stdout=subprocess.DEVNULL, timeout=15, check=False)


def wait_for_socket(proc, socket):
    for _ in range(20):
        if proc.poll() is not None:
            raise ValueError("Could not establish the private operator state tunnel")
        ready = subprocess.run(["colima", "-p", "collab-ai", "ssh", "--", "test", "-S", socket],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
        if ready.returncode == 0:
            return
        time.sleep(0.2)
    raise ValueError("Timed out establishing the private operator state tunnel")
