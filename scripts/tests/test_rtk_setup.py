from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/collab-rtk-setup"))


class RTKSetupTests(unittest.TestCase):
    def test_initializes_both_agents_once_without_resetting_user_opt_out(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            with patch.object(subprocess, "run") as run:
                SETUP["initialize"](home)
                SETUP["initialize"](home)
            self.assertEqual([call.args[0] for call in run.call_args_list], [
                ["/usr/local/bin/rtk", "init", "-g", "--codex"],
                ["/usr/local/bin/rtk", "init", "-g", "--auto-patch"],
            ])
            for call in run.call_args_list:
                self.assertEqual(call.kwargs["env"]["HOME"], str(home))
                self.assertEqual(call.kwargs["env"]["RTK_TELEMETRY_DISABLED"], "1")
                self.assertEqual(call.kwargs["stdin"], subprocess.DEVNULL)
            self.assertEqual((home / ".local/share/rtk").stat().st_mode & 0o777, 0o700)

    def test_failed_setup_can_be_retried(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            with patch.object(subprocess, "run", side_effect=subprocess.CalledProcessError(1, ["rtk"])):
                with self.assertRaises(subprocess.CalledProcessError):
                    SETUP["initialize"](home)
            self.assertFalse((home / ".local/state/collab-ai/rtk-initialized").exists())
            with patch.object(subprocess, "run") as run:
                SETUP["initialize"](home)
            self.assertEqual(run.call_count, 2)
