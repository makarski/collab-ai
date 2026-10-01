"""Probe the real managed terminal with an empty Codex home; never submit a prompt."""

import fcntl
import os
from pathlib import Path
import pty
import re
import select
import shlex
import signal
import struct
import subprocess
import tempfile
import termios
import time
import uuid


def await_login(master, process):
    output = b""
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            break
        if not select.select([master], [], [], 0.2)[0]:
            continue
        try:
            chunk = os.read(master, 65536)
        except OSError:
            break
        output += chunk
        if b"\x1b[6n" in chunk:
            os.write(master, b"\x1b[1;1R")
        text = re.sub(rb"\x1b\[[0-9;?]*[A-Za-z]", b"", output)
        if b"Sign in with ChatGPT" in text:
            return
    raise ValueError("Managed Codex did not reach login: " + output.decode(errors="replace")[-4000:])


def stop(process):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=5)


def verify():
    with tempfile.TemporaryDirectory(prefix="collab-terminal-proof-") as temporary:
        home = Path(temporary)
        aliases = Path("/etc/profile.d/collab-agent-aliases.sh").read_text()
        # Keep the installed alias's arguments; isolate only the logical inbox.
        source = home / "aliases.sh"
        source.write_text(aliases.replace("--agent-id codex-1", "--agent-id proof-" + uuid.uuid4().hex[:8]))
        env = dict(os.environ, CODEX_HOME=temporary, TERM="xterm-256color")
        for key in ("OPENAI_API_KEY", "CODEX_API_KEY"):
            env.pop(key, None)
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 35, 120, 0, 0))
        command = ". " + shlex.quote(str(source)) + "\ncodex"
        try:
            process = subprocess.Popen(["bash", "--noprofile", "--norc", "-ic", command],
                stdin=slave, stdout=slave, stderr=slave, env=env, cwd="/workspace", start_new_session=True)
            try:
                await_login(master, process)
            finally:
                stop(process)
        finally:
            os.close(master)
            os.close(slave)
    print("PASS: installed managed Codex alias reaches native login; no credentials or model request.")


verify()
