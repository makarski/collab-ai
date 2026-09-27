import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
spec = importlib.util.spec_from_file_location("sandbox_smoke", Path(__file__).resolve().parents[1] / "sandbox-smoke.py")
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class CleanupTests(unittest.TestCase):
    def test_failed_teardown_preserves_state_for_recovery(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            state = directory / "terraform.tfstate"
            state.write_text("retained state")
            with patch.object(smoke, "run", side_effect=subprocess.CalledProcessError(1, ["tofu"])):
                with self.assertRaises(subprocess.CalledProcessError):
                    smoke.destroy_deployment(["tofu"], directory, "collab-smoke-test")
            self.assertEqual(state.read_text(), "retained state")

    def test_successful_teardown_removes_only_its_temporary_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            parent = Path(temporary)
            directory = parent / "owned"
            directory.mkdir()
            (parent / "unrelated").write_text("keep")
            with patch.object(smoke, "run") as run:
                smoke.destroy_deployment(["tofu", "-chdir=" + str(directory)], directory, "collab-smoke-test")
            self.assertIn("destroy", run.call_args.args[0])
            self.assertFalse(directory.exists())
            self.assertEqual((parent / "unrelated").read_text(), "keep")
