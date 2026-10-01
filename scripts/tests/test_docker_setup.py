"""First-run Docker configuration must preserve user state and retry failures."""

from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SETUP = runpy.run_path(str(Path(__file__).resolve().parents[2] / "infra/image/collab-docker-setup"))


class DockerSetupTests(unittest.TestCase):
    def test_success_is_once_and_preserves_operator_settings(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            config = home / ".config/docker/daemon.json"
            config.parent.mkdir(parents=True)
            config.write_text('{"log-driver":"local"}\n')
            with patch("subprocess.run") as run:
                SETUP["initialize"](home)
                SETUP["initialize"](home)
                self.assertEqual(run.call_count, 1)
            self.assertEqual(config.read_text(), '{"log-driver":"local"}\n')

    def test_failed_install_retries_without_marking_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            with patch("subprocess.run", side_effect=subprocess.CalledProcessError(1, ["setup"])):
                with self.assertRaises(subprocess.CalledProcessError):
                    SETUP["initialize"](home)
            self.assertFalse((home / ".local/state/collab-ai/docker-initialized").exists())
            with patch("subprocess.run") as run:
                SETUP["initialize"](home)
                run.assert_called_once()
