import importlib.util
from pathlib import Path
import unittest


path = Path(__file__).resolve().parents[2] / "infra/incus/secured_preflight.py"
spec = importlib.util.spec_from_file_location("secured_preflight", path)
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class SecuredPreflightTests(unittest.TestCase):
    def test_fresh_or_stopped_workspace_can_receive_the_mount_before_boot(self):
        preflight.require_stopped(404, {})
        preflight.require_stopped(200, {"metadata": {"status": "Stopped"}})

    def test_running_frozen_and_unknown_states_fail_closed(self):
        for status in ("Running", "Frozen", "Error", None):
            with self.subTest(status=status), self.assertRaisesRegex(ValueError, "Stop workspace"):
                preflight.require_stopped(200, {"metadata": {"status": status}})

    def test_server_errors_cannot_be_treated_as_an_absent_workspace(self):
        for status in (401, 403, 500):
            with self.subTest(status=status), self.assertRaisesRegex(ValueError, "Cannot inspect"):
                preflight.require_stopped(status, {})
