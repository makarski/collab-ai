"""Host-owned, locked state backend for a disposable provisioning container."""

import base64
from contextlib import contextmanager
import fcntl
import hmac
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import secrets
import tempfile
import threading
from urllib.parse import parse_qs, urlsplit


MAX_STATE = 32 * 1024 * 1024


def private_write(path, data):
    """Replace atomically; acknowledge writes only after file/directory fsync."""
    path = Path(path)
    descriptor, temporary = tempfile.mkstemp(dir=path.parent, prefix=".operator-")
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        Path(temporary).unlink(missing_ok=True)


@contextmanager
def deployment_lock(directory):
    descriptor = os.open(directory / ".operator.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError("Another provisioning command owns this deployment") from error
        yield
    finally:
        os.close(descriptor)


class StateStore:
    def __init__(self, directory):
        self.path = directory / "terraform.tfstate"
        self.backup = directory / "terraform.tfstate.backup"
        self.lock = None
        self.token = secrets.token_urlsafe(32)
        self.auth = "Basic " + base64.b64encode(f"operator:{self.token}".encode()).decode()

    def request(self, method, url, body):
        parsed = urlsplit(url)
        if parsed.path != "/state":
            return 404, b""
        if method == "GET":
            return (200, self.path.read_bytes()) if self.path.exists() else (404, b"")
        if method in ("LOCK", "UNLOCK"):
            return self.change_lock(method, body)
        if method == "POST":
            lock_id = parse_qs(parsed.query).get("ID", [None])[0]
            if self.lock is None or lock_id != self.lock["ID"]:
                return 409, json.dumps(self.lock or {}).encode()
            self.save(body)
            return 200, b""
        return 405, b""  # No state deletion endpoint.

    def change_lock(self, method, body):
        requested = json.loads(body)
        if not isinstance(requested, dict) or not requested.get("ID"):
            return 400, b""
        if self.lock and self.lock["ID"] != requested["ID"]:
            return 423, json.dumps(self.lock).encode()
        self.lock = requested if method == "LOCK" else None
        return 200, b""

    def save(self, body):
        state = checked_state(body)
        if self.path.is_symlink() or self.backup.is_symlink():
            raise ValueError("Refusing symlinked state files")
        if self.path.exists():
            old = self.path.read_bytes()
            if old == body:
                return
            require_successor(state, checked_state(old))
            private_write(self.backup, old)
        private_write(self.path, body)


def checked_state(body):
    state = json.loads(body)
    if not isinstance(state, dict) or state.get("version") != 4:
        raise ValueError("Invalid state document")
    if not isinstance(state.get("lineage"), str) or not state["lineage"]:
        raise ValueError("State requires a lineage")
    require_serial(state.get("serial"))
    return state


def require_serial(serial):
    if type(serial) is not int or serial < 0:
        raise ValueError("State requires a non-negative serial")


def require_successor(state, previous):
    if state["lineage"] != previous["lineage"]:
        raise ValueError("State lineage changed")
    if state["serial"] < previous["serial"]:
        raise ValueError("State serial moved backwards")
    if state["serial"] == previous["serial"] and state != previous:
        raise ValueError("State changed without advancing its serial")


class StateHandler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # Never log state contents or credentials.

    def handle_request(self):
        self.connection.settimeout(15)
        if not hmac.compare_digest(self.headers.get("Authorization", ""), self.server.store.auth):
            self.respond(401, b"")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length < 0 or length > MAX_STATE:
                self.respond(413, b"")
                return
            body = self.read_body(length)
            code, data = self.server.store.request(self.command, self.path, body)
        except (ValueError, KeyError, TypeError, OSError):
            code, data = 500, b"State request failed; host state was not acknowledged"
        self.respond(code, data)

    def read_body(self, length):
        body = self.rfile.read(length)
        if len(body) != length:
            raise ValueError("Incomplete state request")
        return body

    def respond(self, code, data):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_GET = do_POST = do_LOCK = do_UNLOCK = do_DELETE = handle_request


@contextmanager
def state_server(directory):
    server = HTTPServer(("127.0.0.1", 0), StateHandler)
    server.store = StateStore(directory)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join()
