"""Accept macOS system permissions without weakening helper protection."""

from pathlib import Path
import stat
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_share_install as install


def metadata(mode=0o755, uid=0, gid=0, kind=stat.S_IFDIR):
    return SimpleNamespace(st_mode=kind | mode, st_uid=uid, st_gid=gid)


class ProtectedHelperTests(unittest.TestCase):
    def test_standard_admin_writable_ancestors_allow_install_and_run(self):
        def info(path):
            if path in install.SYSTEM_ANCESTORS:
                return metadata(0o775, gid=80)
            if path.suffix == ".py":
                return metadata(0o644, kind=stat.S_IFREG)
            return metadata()

        with patch.object(Path, "exists", return_value=True), \
                patch.object(Path, "lstat", autospec=True, side_effect=info), \
                patch.object(Path, "is_file", return_value=True):
            self.assertEqual(len(install.install_commands()), 3)
            install.require_installed()

    def test_rejects_untrusted_or_world_writable_system_ancestors(self):
        for path in install.SYSTEM_ANCESTORS:
            for info in (metadata(0o777, gid=80), metadata(0o775, gid=20),
                         metadata(0o775, gid=60000), metadata(uid=501),
                         metadata(0o775, gid=80, kind=stat.S_IFREG)):
                with self.subTest(path=path, info=info), self.assertRaises(ValueError):
                    install.check_metadata(info, path)

    def test_helper_directories_and_files_stay_strict(self):
        for path in (install.HELPER_DIRECTORY.parent, install.HELPER_DIRECTORY,
                     install.HELPER, install.HELPER_DIRECTORY / "sandbox_sftp_policy.py"):
            for info in (metadata(0o775, gid=80), metadata(0o777), metadata(uid=501)):
                with self.subTest(path=path, info=info), self.assertRaises(ValueError):
                    install.check_metadata(info, path)

    def test_rejects_symlinks_even_for_system_ancestors(self):
        for path in (*install.SYSTEM_ANCESTORS, install.HELPER_DIRECTORY, install.HELPER):
            with self.subTest(path=path), self.assertRaises(ValueError):
                install.check_metadata(metadata(kind=stat.S_IFLNK), path)
