import http.client
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_operator_state import state_server


class HTTPTests(unittest.TestCase):
    def request(self, method, path="/state", body=None, authorized=True):
        conn = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
        try:
            headers = {"Authorization": self.server.store.auth} if authorized else {}
            conn.request(method, path, body=body, headers=headers)
            response = conn.getresponse()
            return response.status, response.read()
        finally:
            conn.close()

    def test_authenticated_locked_backend_and_failed_write(self):
        with tempfile.TemporaryDirectory() as directory, state_server(Path(directory)) as server:
            self.server = server
            self.assertEqual(self.request("GET", authorized=False)[0], 401)
            self.assertEqual(self.request("GET")[0], 404)
            self.assertEqual(self.request("LOCK", body=b'{"ID":"owner"}')[0], 200)
            state = json.dumps({"version": 4, "lineage": "proof", "serial": 1, "resources": []}).encode()
            with patch.object(server.store, "save", side_effect=OSError("disk unavailable")):
                self.assertEqual(self.request("POST", "/state?ID=owner", state)[0], 500)
            self.assertEqual(self.request("GET")[0], 404)
            self.assertEqual(self.request("POST", "/state?ID=owner", state)[0], 200)
            self.assertEqual(self.request("GET"), (200, state))
            self.assertEqual(self.request("UNLOCK", body=b'{"ID":"owner"}')[0], 200)
