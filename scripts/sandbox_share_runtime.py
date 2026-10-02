"""Relay SFTP to workspace without exposing a host listener or credentials."""

from contextlib import ExitStack
import fcntl
import hashlib
import os
from pathlib import Path
import signal
import subprocess
import time

from sandbox_sftp_policy import server_command


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def lock(stack, remote, project):
    directory = Path.home() / ".local/state/collab-ai/shares"
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    key = hashlib.sha256(f"{remote}:{project}".encode()).hexdigest()[:16]
    path = directory / (key + ".lock")
    fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    stream = stack.enter_context(os.fdopen(fd, "w"))
    try:
        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as error:
        raise ValueError("A sharing process is already running for this workspace") from error


def connection(stack, base, entry, user):
    # Two anonymous pipes: no socket, credential file or server command in dev.
    request_read, request_write = os.pipe()
    response_read, response_write = os.pipe()
    source = Path(entry["host_path"])
    guest = (Path(__file__).parent / "shares/guest.py").read_text()
    try:
        server = subprocess.Popen(server_command(source, entry["container_readonly"], user),
                                  stdin=request_read, stdout=response_write)
        stack.callback(stop, server)
        command = base + ["python3", "-c", guest, entry["container_mount_path"], str(source),
                          str(entry["container_readonly"]).lower()]
        client = subprocess.Popen(command, stdin=response_read, stdout=request_write)
        stack.callback(stop, client)
    finally:
        for fd in (request_read, request_write, response_read, response_write):
            os.close(fd)
    return server, client


def interrupt(*_):
    raise KeyboardInterrupt


def wait_ready(base, mounts, processes):
    targets = [entry["container_mount_path"] for entry in mounts]
    check = "import os,sys; sys.exit(not all(os.path.ismount(p) for p in sys.argv[1:]))"
    for _ in range(30):
        if any(process.poll() is not None for process in processes):
            raise ValueError("Sharing helper exited before mounting; inspect its error above")
        result = subprocess.run(base + ["python3", "-c", check, *targets], timeout=10)
        if result.returncode == 0:
            return
        time.sleep(0.5)
    raise ValueError("Sharing mount did not become ready")


def serve(mounts, user, remote, project):
    base = ["incus", "--project", project, "exec", f"{remote}:workspace", "-T", "--"]
    signal.signal(signal.SIGTERM, interrupt)
    subprocess.run(["sudo", "-v"], check=True)
    with ExitStack() as stack:
        lock(stack, remote, project)
        processes = []
        for entry in mounts:
            processes.extend(connection(stack, base, entry, user))
        wait_ready(base, mounts, processes)
        print("Sharing active while this command runs. Ctrl+C disconnects; host files remain.", flush=True)
        while all(process.poll() is None for process in processes):
            time.sleep(0.5)
        raise ValueError("Sharing disconnected. Restart workspace before reconnecting; no local fallback is enabled")
