import os
from pathlib import Path
import runpy
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/collab-workspace-setup"))


class StorageTests(unittest.TestCase):
    def test_home_initialization_preserves_settings_and_never_follows_symlinks(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home, skeleton = root / "home", root / "skel"
            home.mkdir()
            skeleton.mkdir()
            for name in (".bashrc", ".profile", ".bash_logout"):
                (skeleton / name).write_text("default")
            (home / ".profile").write_text("user configuration")
            outside = root / "private"
            outside.write_text("keep")
            (home / ".bashrc").symlink_to(outside)
            for _ in range(2):
                SETUP["seed_home"](home, skeleton, os.getuid(), os.getgid())
            self.assertEqual((home / ".profile").read_text(), "user configuration")
            self.assertEqual(outside.read_text(), "keep")
            self.assertTrue((home / ".bashrc").is_symlink())
            self.assertEqual((home / ".bash_logout").read_text(), "default")
            self.assertEqual((home / ".bash_logout").stat().st_mode & 0o777, 0o600)
