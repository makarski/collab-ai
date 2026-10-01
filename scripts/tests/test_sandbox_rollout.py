import argparse
from contextlib import ExitStack
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_rollout as rollout


class RolloutTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.image = self.root / "image"
        self.image.mkdir()
        archive = self.image / "workspace.tar.gz"
        archive.write_bytes(b"fixture")
        (self.image / "image.tfvars.json").write_text(json.dumps({"image_file": str(archive),
            "image_fingerprint": hashlib.sha256(archive.read_bytes()).hexdigest()}))
        state = {"resources": [{"type": "incus_project", "instances": [{"attributes": {"name": "proof"}}]},
            {"type": "incus_instance", "instances": [{"attributes": {"project": "proof", "name": name}}
             for name in ("workspace", "secured")]}]}
        (self.root / "terraform.tfstate").write_text(json.dumps(state))
        self.args = argparse.Namespace(project="proof", remote="local", image_dir=None, release="latest", repo="owner/repo")
        self.server = {"architectures": ["aarch64"]}

    def test_download_failure_does_not_stop_or_apply(self):
        with patch.object(rollout, "download_release", side_effect=ValueError("checksum")), \
             patch.object(rollout, "stop_managed") as stop, patch.object(rollout, "run_locked_operation") as run:
            with self.assertRaisesRegex(ValueError, "checksum"):
                rollout.rollout(self.args, self.root, self.server)
        stop.assert_not_called()
        run.assert_not_called()

    def test_local_image_verified_before_stopping(self):
        self.args.image_dir = self.image
        (self.image / "workspace.tar.gz").write_bytes(b"tampered")
        with patch.object(rollout, "stop_managed") as stop:
            with self.assertRaisesRegex(ValueError, "checksum"):
                rollout.rollout(self.args, self.root, self.server)
        stop.assert_not_called()

    def test_pending_recovery_blocks_download(self):
        (self.root / "operator-pending.json").write_text('{"project":"retained","remote":"local"}')
        with patch.object(rollout, "download_release") as download:
            with self.assertRaisesRegex(ValueError, "recover"):
                rollout.rollout(self.args, self.root, self.server)
        download.assert_not_called()

    def test_rollout_orders_operations_and_never_refreshes_ssh_on_failure(self):
        for failure in (None, "plan", "apply"):
            with self.subTest(failure=failure), ExitStack() as stack:
                events = []
                stack.enter_context(patch.object(rollout, "download_release", side_effect=lambda *a: events.append("download") or self.image))
                stack.enter_context(patch.object(rollout, "stop_managed", side_effect=lambda *a: events.append("stop")))
                stack.enter_context(patch.object(rollout, "configure_ssh", side_effect=lambda *a: events.append("ssh")))
                def operation(args, directory, server):
                    events.append(args.action)
                    self.assertEqual(args.image_dir, self.image.resolve())
                    if args.action == failure:
                        raise ValueError("failed")
                stack.enter_context(patch.object(rollout, "run_locked_operation", side_effect=operation))
                if failure:
                    with self.assertRaisesRegex(ValueError, "failed"):
                        rollout.rollout(self.args, self.root, self.server)
                    self.assertNotIn("ssh", events)
                else:
                    rollout.rollout(self.args, self.root, self.server)
                    self.assertEqual(events, ["download", "stop", "plan", "apply", "ssh"])
                if failure == "plan":
                    self.assertNotIn("apply", events)

    def test_stops_only_state_owned_running_instances_in_order(self):
        names = rollout.managed_instances(self.root, "proof")
        with patch.object(rollout, "instances", return_value={"workspace": "Running", "secured": "Running", "unrelated": "Running"}), \
             patch.object(rollout.subprocess, "run") as run:
            rollout.stop_managed(self.args, names)
        self.assertEqual([call.args[0][4] for call in run.call_args_list], ["local:workspace", "local:secured"])
        with self.assertRaisesRegex(ValueError, "Unexpected"):
            rollout.managed_instances(self.root, "wrong-project")

    def test_fresh_install_does_not_query_a_nonexistent_project(self):
        (self.root / "terraform.tfstate").unlink()
        names = rollout.managed_instances(self.root, "proof")
        with patch.object(rollout, "instances") as listing:
            rollout.stop_managed(self.args, names)
        listing.assert_not_called()

    def test_existing_project_without_state_is_never_adopted_or_stopped(self):
        (self.root / "terraform.tfstate").unlink()
        with patch.object(rollout.subprocess, "check_output", return_value=b'[{"name":"proof"}]'), \
             patch.object(rollout, "download_release") as download, patch.object(rollout, "stop_managed") as stop:
            with self.assertRaisesRegex(ValueError, "no matching state"):
                rollout.rollout(self.args, self.root, self.server)
        download.assert_not_called()
        stop.assert_not_called()

    def test_stop_failure_aborts_before_plan(self):
        self.args.image_dir = self.image
        with patch.object(rollout, "stop_managed", side_effect=ValueError("stop failed")), \
             patch.object(rollout, "run_locked_operation") as run:
            with self.assertRaisesRegex(ValueError, "stop failed"):
                rollout.rollout(self.args, self.root, self.server)
        run.assert_not_called()

    def test_stopped_workspace_does_not_refresh_ssh(self):
        with patch.object(rollout, "instances", return_value={"workspace": "Stopped"}), \
             patch.object(rollout.importlib.util, "spec_from_file_location") as load:
            rollout.configure_ssh(self.args)
        load.assert_not_called()
