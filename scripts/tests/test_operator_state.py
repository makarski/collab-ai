import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_operator_state import StateStore, deployment_lock
from sandbox_operator_plan import validate_plan


class StateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.store = StateStore(self.directory)

    def state(self, serial=1, lineage="original"):
        return json.dumps({"version": 4, "lineage": lineage, "serial": serial, "resources": []}).encode()

    def lock(self, ident="owner"):
        return self.store.request("LOCK", "/state", json.dumps({"ID": ident}).encode())

    def test_locked_write_retains_private_backup_and_state(self):
        self.assertEqual(self.store.request("GET", "/state", b"")[0], 404)
        self.assertEqual(self.lock()[0], 200)
        self.assertEqual(self.store.request("POST", "/state?ID=owner", self.state())[0], 200)
        self.store.request("POST", "/state?ID=owner", self.state(2))
        self.assertEqual(self.store.path.read_bytes(), self.state(2))
        self.assertEqual(self.store.backup.read_bytes(), self.state())
        self.store.request("POST", "/state?ID=owner", self.state(2))
        self.assertEqual(self.store.backup.read_bytes(), self.state())
        self.assertEqual(self.store.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.store.backup.stat().st_mode & 0o777, 0o600)

    def test_missing_or_foreign_lock_cannot_write_unlock_or_delete(self):
        self.assertEqual(self.store.request("POST", "/state", self.state())[0], 409)
        self.lock()
        self.assertEqual(self.lock("another")[0], 423)
        self.assertEqual(self.store.request("POST", "/state?ID=another", self.state())[0], 409)
        self.assertEqual(self.store.request("UNLOCK", "/state", b'{"ID":"another"}')[0], 423)
        self.assertEqual(self.store.request("DELETE", "/state", b"")[0], 405)
        self.assertEqual(self.store.lock["ID"], "owner")

    def test_invalid_or_regressed_state_preserves_previous_file(self):
        self.store.save(self.state(2))
        for data in (b"{}", b"not-json", self.state(1), self.state(3, "foreign")):
            with self.subTest(data=data), self.assertRaises(ValueError):
                self.store.save(data)
            self.assertEqual(self.store.path.read_bytes(), self.state(2))

    def test_failed_atomic_replace_does_not_destroy_current_state(self):
        self.store.save(self.state())
        with patch("sandbox_operator_state.os.replace", side_effect=OSError("disk failed")):
            with self.assertRaises(OSError):
                self.store.save(self.state(2))
        self.assertEqual(self.store.path.read_bytes(), self.state())

    def test_concurrent_provisioners_cannot_share_a_deployment(self):
        with deployment_lock(self.directory):
            with self.assertRaisesRegex(ValueError, "Another provisioning"):
                with deployment_lock(self.directory):
                    pass
        with deployment_lock(self.directory):
            pass

    def test_symlinked_state_is_not_overwritten(self):
        target = self.directory / "unrelated"
        target.write_text("keep")
        self.store.path.symlink_to(target)
        with self.assertRaisesRegex(ValueError, "symlinked"):
            self.store.save(self.state())
        self.assertEqual(target.read_text(), "keep")


class PlanTests(unittest.TestCase):
    def test_data_volume_replacement_is_rejected(self):
        for name in ("workspace-data", "agent-home", "secured-state", "docker-data"):
            plan = {"resource_changes": [{"type": "incus_storage_volume", "change": {
                "before": {"name": name}, "actions": ["delete", "create"]}}]}
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "persistent data"):
                validate_plan(plan, Path("/host/state"), Path("/host/operator"))

    def test_sharing_parent_of_operator_state_is_rejected(self):
        plan = {"planned_values": {"root_module": {"resources": [{"type": "incus_profile", "values": {
            "device": [{"properties": {"source": "/host"}}]}}]}}}
        with self.assertRaisesRegex(ValueError, "exposes operator"):
            validate_plan(plan, Path("/host/state"), Path("/host/operator"))
        plan["planned_values"]["root_module"]["resources"][0]["values"]["device"][0]["properties"]["source"] = "/project"
        validate_plan(plan, Path("/host/state"), Path("/host/operator"))
