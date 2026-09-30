import argparse
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_operator_config import deployment_directory, image_selection, provisioning_files
from sandbox_operator_recovery import retained_operator
from sandbox_operator_workflow import require_saved_plan, prepare


class ConfigTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def state(self, root, project):
        directory = root / "infra/incus"
        directory.mkdir(parents=True)
        (directory / "terraform.tfstate").write_text(json.dumps({"resources": [{"type": "incus_project",
             "instances": [{"attributes": {"name": project}}]}]}))
        return directory

    def test_unique_existing_worktree_state_is_discovered_without_moving_it(self):
        directory = self.state(self.root / "operator-checkout", "collab-ai")
        listing = subprocess.CompletedProcess([], 0, stdout=f"worktree {directory.parent.parent}\n")
        with patch("sandbox_operator_config.subprocess.run", return_value=listing):
            found = deployment_directory(None, "collab-ai", root=self.root, location=self.root / "locations.json")
        self.assertEqual(found, directory.resolve())

    def test_symlinked_deployment_directory_is_rejected(self):
        directory = self.state(self.root / "real", "collab-ai")
        alias = self.root / "alias"
        alias.symlink_to(directory, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "non-symlinked"):
            deployment_directory(alias, "collab-ai")

    def test_ambiguous_or_wrong_project_state_is_rejected(self):
        first = self.state(self.root / "one", "collab-ai")
        second = self.state(self.root / "two", "collab-ai")
        listing = subprocess.CompletedProcess([], 0, stdout=f"worktree {first.parent.parent}\nworktree {second.parent.parent}\n")
        with patch("sandbox_operator_config.subprocess.run", return_value=listing):
            with self.assertRaisesRegex(ValueError, "Multiple"):
                deployment_directory(None, "collab-ai", root=self.root, location=self.root / "locations.json")
        with self.assertRaisesRegex(ValueError, "belongs to"):
            deployment_directory(first, "collab-other")

    def test_image_tampering_is_rejected(self):
        image = self.root / "workspace.tar.gz"
        image.write_bytes(b"changed")
        (self.root / "image.tfvars.json").write_text(json.dumps({"image_file": str(image), "image_fingerprint": "a" * 64}))
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            image_selection(self.root, self.root)

    def test_state_keys_and_unrelated_files_are_not_packaged(self):
        for name in (".terraform.lock.hcl", "secured_preflight.py", "main.tf", "operator.auto.tfvars.json"):
            (self.root / name).write_text("{}")
        for name in ("terraform.tfstate", "id_ed25519", "operator-image.json", "project-secret"):
            (self.root / name).write_text("must not package")
        self.assertEqual(set(provisioning_files(self.root)),
                         {".terraform.lock.hcl", "secured_preflight.py", "main.tf", "operator.auto.tfvars.json"})

    def test_saved_plan_rejects_changes_before_starting_an_operator(self):
        import hashlib
        plan = self.root / "operator.tfplan"
        plan.write_bytes(b"plan")
        identity = {"files": {"main.tf": "original"}, "image": "image", "server": "server",
                    "options": {"replace": True, "destroy": False}}
        previous = dict(identity, plan_sha256=hashlib.sha256(plan.read_bytes()).hexdigest())
        (self.root / "operator-plan.json").write_text(json.dumps(previous))
        require_saved_plan(self.root, copy.deepcopy(identity))
        changed = copy.deepcopy(identity)
        changed["files"]["main.tf"] = "changed"
        with self.assertRaisesRegex(ValueError, "inputs or server changed"):
            require_saved_plan(self.root, changed)
        plan.write_bytes(b"modified")
        with self.assertRaisesRegex(ValueError, "Saved plan changed"):
            require_saved_plan(self.root, copy.deepcopy(identity))

    def test_pending_operation_blocks_new_work(self):
        (self.root / "operator-pending.json").write_text(json.dumps({"project": "retained", "remote": "local"}))
        with self.assertRaisesRegex(ValueError, "recover"):
            prepare(argparse.Namespace(), self.root, {})

    def test_recovery_refuses_another_server_or_deployment(self):
        args = argparse.Namespace(remote="local", project="collab-ai", storage_pool="default", network="incusbr0")
        pending = {"remote": "local", "server": "original", "deployment_project": "collab-ai",
                   "project": "collab-operator-" + "a" * 16}
        with self.assertRaisesRegex(ValueError, "another Incus"):
            retained_operator(args, {"certificate_fingerprint": "other"}, pending)
        pending["deployment_project"] = "collab-other"
        with self.assertRaisesRegex(ValueError, "another deployment"):
            retained_operator(args, {"certificate_fingerprint": "original"}, pending)
