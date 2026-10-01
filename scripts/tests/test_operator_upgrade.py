import argparse
import gzip
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_operator_recipe import refresh_recipe
from sandbox_operator_workflow import PreparedInputs, stage
from sandbox_rollout_checks import changed_image


class OperatorUpgradeTests(unittest.TestCase):
    def test_image_path_changes_with_contents_and_matches_upload(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            paths = []
            for fingerprint in ("a" * 64, "b" * 64):
                operator = Mock()
                args = argparse.Namespace(action="plan", replace=True, destroy=False, project="proof")
                prepared = PreparedInputs(args, directory, directory / "archive", fingerprint, {}, {})
                stage(operator, directory, prepared, "test-token")
                variables = json.loads((directory / "variables.json").read_text())
                paths.append(variables["image_file"])
                self.assertEqual(variables["image_fingerprint"], fingerprint)
                self.assertIn(fingerprint, variables["image_file"])
                operator.push.assert_any_call(prepared.image, variables["image_file"])
            self.assertNotEqual(*paths)

    def test_refresh_preserves_settings_state_and_backs_up_old_recipe(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            source, state = root / "source", root / "state"
            source.mkdir()
            state.mkdir()
            for filename in ("main.tf", "storage.tf", ".terraform.lock.hcl", "secured_preflight.py"):
                (source / filename).write_text("new")
            (state / "main.tf").write_text("old")
            for filename in ("operator.auto.tfvars.json", "terraform.tfstate", "id_ed25519"):
                (state / filename).write_text("preserved")
            (source / "operator.auto.tfvars.json").write_text("must not copy")
            refresh_recipe(state, source)
            self.assertEqual((state / "main.tf").read_text(), "new")
            self.assertEqual((state / "storage.tf").read_text(), "new")
            backups = list(state.glob("recipe-backup-*"))
            self.assertEqual((backups[0] / "main.tf").read_text(), "old")
            for filename in ("operator.auto.tfvars.json", "terraform.tfstate", "id_ed25519"):
                self.assertEqual((state / filename).read_text(), "preserved")
            refresh_recipe(state, source)
            self.assertEqual(list(state.glob("recipe-backup-*")), backups)

    def test_recipe_refresh_rejects_symlinks_before_writing(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            source, state = root / "source", root / "state"
            source.mkdir()
            state.mkdir()
            for filename in ("main.tf", ".terraform.lock.hcl", "secured_preflight.py"):
                (source / filename).write_text("new")
                (state / filename).write_text("old")
            (state / "main.tf").unlink()
            (state / "main.tf").symlink_to(source / "main.tf")
            with self.assertRaisesRegex(ValueError, "regular recipe file"):
                refresh_recipe(state, source)
            self.assertEqual((state / ".terraform.lock.hcl").read_text(), "old")

    def test_upgrade_fixture_changes_archive_identity_not_contents(self):
        with tempfile.TemporaryDirectory() as name:
            source = Path(name)
            original = gzip.compress(b"same rootfs")
            (source / "workspace.tar.gz").write_bytes(original)
            fingerprint = changed_image(source, source / "upgrade")
            upgraded = (source / "upgrade/workspace.tar.gz").read_bytes()
            self.assertNotEqual(original, upgraded)
            self.assertEqual(gzip.decompress(upgraded), b"same rootfs")
            self.assertEqual((source / "workspace.tar.gz").read_bytes(), original)
            self.assertEqual(json.loads((source / "upgrade/image.tfvars.json").read_text())["image_fingerprint"], fingerprint)
