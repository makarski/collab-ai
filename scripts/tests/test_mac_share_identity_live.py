"""Disposable hosted-macOS CI proof of the real account and inherited ACLs."""

import os
import json
from pathlib import Path
import platform
import pwd
import shlex
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_mac_shares import identity, setup
from sandbox_sftp_policy import server_command
from sandbox_share_install import HELPER


@unittest.skipUnless(platform.system() == "Darwin" and os.environ.get("COLLAB_MAC_IDENTITY_PROOF") == "1",
                     "explicit disposable macOS runner only")
class MacIdentityLiveTests(unittest.TestCase):
    def test_real_sharing_identity_and_operator_access(self):
        user = "collab-share-ci"
        self.assertNotIn(user, {account.pw_name for account in pwd.getpwall()})
        with tempfile.TemporaryDirectory(prefix="collab-share-ci-", dir=Path.home()) as directory:
            root = Path(directory).resolve()
            shared = root / "project"
            shared.mkdir()
            original = shared / "original"
            original.write_text("host")
            secret = root / "private"
            secret.write_text("secret")
            secret.chmod(0o600)
            mounts = [dict(host_path=str(shared), container_mount_path=str(shared), container_readonly=False)]
            try:
                setup(mounts, user)
                account = identity(user)
                self.assertNotEqual(account.pw_uid, os.getuid())
                probe = subprocess.check_output(["sudo", "-n", "/usr/bin/python3", "-I", str(HELPER), user, "--identity", "false"], text=True)
                self.assertEqual(json.loads(probe), dict(uid=account.pw_uid, gid=account.pw_gid, groups=[account.pw_gid]))
                command = server_command(shared, False, user)
                result = subprocess.run(["/usr/bin/sftp", "-D", shlex.join(command), "-b", "-"],
                    input=f'put "{secret}" "{shared}/from-agent"\n', text=True, capture_output=True, timeout=20)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((shared / "from-agent").stat().st_uid, account.pw_uid)
                self.assertEqual(original.stat().st_uid, os.getuid())
                (shared / "from-agent").write_text("operator can still edit")
                denied = subprocess.run(["sudo", "-n", "-u", user, "cat", str(secret)], capture_output=True)
                self.assertNotEqual(denied.returncode, 0)
            finally:
                for kind in ("Users", "Groups"):
                    subprocess.run(["sudo", "-n", "dscl", ".", "-delete", f"/{kind}/{user}"], capture_output=True)
