from pathlib import Path
import runpy
import stat
import subprocess
from types import SimpleNamespace
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
CONNECT = runpy.run_path(str(ROOT / "infra/image/collab-executor-connect"))
LAUNCH = runpy.run_path(str(ROOT / "infra/image/collab-supervised-codex"))
DIRECTORY = Path("/mnt/collab-executor")


def metadata(kind, mode, uid=0):
    return SimpleNamespace(st_mode=kind | mode, st_uid=uid)


class ExecutorEndpointTests(unittest.TestCase):
    def test_root_owned_private_socket_is_accepted(self):
        entries = [metadata(stat.S_IFDIR, 0o755), metadata(stat.S_IFSOCK, 0o600)]
        with patch.object(Path, "lstat", side_effect=entries):
            self.assertEqual(CONNECT["endpoint"](DIRECTORY), str(DIRECTORY / "codex.sock"))

    def test_parent_rejects_symlinks_files_agent_ownership_and_write_access(self):
        unsafe = [metadata(stat.S_IFLNK, 0o755), metadata(stat.S_IFREG, 0o755),
                  metadata(stat.S_IFDIR, 0o755, 1001), metadata(stat.S_IFDIR, 0o775),
                  metadata(stat.S_IFDIR, 0o757)]
        for entry in unsafe:
            with self.subTest(entry=entry), patch.object(Path, "lstat", return_value=entry):
                with self.assertRaises(ValueError):
                    CONNECT["endpoint"](DIRECTORY)

    def test_endpoint_rejects_symlinks_files_foreign_owner_and_nonprivate_modes(self):
        unsafe = [metadata(stat.S_IFLNK, 0o600), metadata(stat.S_IFREG, 0o600),
                  metadata(stat.S_IFSOCK, 0o600, 1001), metadata(stat.S_IFSOCK, 0o660),
                  metadata(stat.S_IFSOCK, 0o606), metadata(stat.S_IFSOCK, 0o400)]
        for entry in unsafe:
            entries = [metadata(stat.S_IFDIR, 0o755), entry]
            with self.subTest(entry=entry), patch.object(Path, "lstat", side_effect=entries):
                with self.assertRaises(ValueError):
                    CONNECT["endpoint"](DIRECTORY)

    def test_missing_endpoint_never_falls_back(self):
        entries = [metadata(stat.S_IFDIR, 0o755), FileNotFoundError("missing socket")]
        with patch.object(Path, "lstat", side_effect=entries), self.assertRaises(FileNotFoundError):
            CONNECT["endpoint"](DIRECTORY)

    def test_launcher_rejects_failed_check(self):
        failed = subprocess.CompletedProcess([], 1, "", "Connection refused")
        with patch.object(subprocess, "run", return_value=failed):
            with self.assertRaisesRegex(ValueError, "Dev executor unavailable.*Connection refused"):
                LAUNCH["require_executor"]()

    def test_launcher_rejects_missing_or_hung_connector(self):
        for error in (FileNotFoundError("missing helper"), subprocess.TimeoutExpired("helper", 10)):
            with self.subTest(error=error), patch.object(subprocess, "run", side_effect=error):
                with self.assertRaisesRegex(ValueError, "Dev executor unavailable"):
                    LAUNCH["require_executor"]()
