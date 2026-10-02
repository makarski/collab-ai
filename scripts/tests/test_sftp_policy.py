"""Exercise real macOS SFTP boundary checks, without changing host permissions."""

from pathlib import Path
import platform
import shlex
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_sftp_policy import server_command


@unittest.skipUnless(platform.system() == "Darwin", "macOS sandbox policy")
class SftpPolicyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.shared = self.root / "shared"
        self.shared.mkdir()
        self.private = self.root / "private"
        self.private.mkdir()
        self.input = self.private / "secret"
        self.input.write_text("private-content")

    def sftp(self, batch, readonly=False):
        command = server_command(self.shared, readonly)
        return subprocess.run(["/usr/bin/sftp", "-D", shlex.join(command), "-b", "-"],
                              input=batch, text=True, capture_output=True, timeout=15)

    def test_permitted_write_and_readonly_denial(self):
        target = self.shared / "from-agent"
        batch = f'put "{self.input}" "{target}"\n'
        result = self.sftp(batch)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(target.read_text(), "private-content")
        target.unlink()
        self.assertNotEqual(self.sftp(batch, readonly=True).returncode, 0)
        self.assertFalse(target.exists())

    def test_direct_and_symlink_escapes_are_denied(self):
        (self.shared / "escape").symlink_to(self.private, target_is_directory=True)
        for source in (self.private, self.shared / "escape"):
            with self.subTest(source=source):
                result = self.sftp(f'get "{source}/secret" "{self.root}/copied"\n')
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.root / "copied").exists())
                result = self.sftp(f'put "{self.input}" "{source}/from-agent"\n')
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.private / "from-agent").exists())

    def test_sibling_and_dotdot_escapes_are_denied(self):
        sibling = self.root / "shared-other"
        sibling.mkdir()
        for target in (sibling / "escaped", self.shared / "../private/escaped"):
            with self.subTest(target=target):
                self.assertNotEqual(self.sftp(f'put "{self.input}" "{target}"\n').returncode, 0)
                self.assertFalse(target.exists())
