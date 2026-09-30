import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_operator_recovery import recover_state
from sandbox_operator_state import StateStore


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.before = {"version": 4, "lineage": "proof", "serial": 1, "resources": []}
        StateStore(self.directory).save(json.dumps(self.before).encode())

    def operator(self, emergency):
        class FakeOperator:
            remote = "local:"
            target = "local:operator"
            project = "collab-operator-" + "a" * 16

            def run(self, *args):
                Path(args[-1]).write_text(json.dumps(emergency))
        return FakeOperator()

    def test_native_emergency_state_is_validated_and_backed_up(self):
        after = dict(self.before, serial=2, resources=[{"type": "incus_project"}])
        with patch("sandbox_operator_recovery.subprocess.check_output", return_value=b'["errored.tfstate"]'):
            recover_state(self.operator(after), self.directory)
        self.assertEqual(json.loads((self.directory / "terraform.tfstate").read_text()), after)
        self.assertEqual(json.loads((self.directory / "terraform.tfstate.backup").read_text()), self.before)

    def test_foreign_emergency_state_cannot_replace_host_state(self):
        foreign = dict(self.before, serial=9, lineage="another-deployment")
        with patch("sandbox_operator_recovery.subprocess.check_output", return_value=b'["errored.tfstate"]'):
            with self.assertRaisesRegex(ValueError, "lineage"):
                recover_state(self.operator(foreign), self.directory)
        self.assertEqual(json.loads((self.directory / "terraform.tfstate").read_text()), self.before)

    def test_missing_state_does_not_silently_recover_an_empty_deployment(self):
        (self.directory / "terraform.tfstate").unlink()
        with patch("sandbox_operator_recovery.subprocess.check_output", return_value=b'[]'):
            with self.assertRaisesRegex(ValueError, "No host or emergency state"):
                recover_state(self.operator({}), self.directory)
