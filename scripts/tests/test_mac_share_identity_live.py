"""Disposable hosted-macOS CI proof of the real account and inherited ACLs."""

import os
import json
from pathlib import Path
import platform
import pwd
import select
import shlex
import subprocess
import sys
import tempfile
import time
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
                probe = subprocess.check_output(["sudo", "-n", "/usr/bin/python3", "-I", str(HELPER), user, "--identity", "false", str(os.getpid())], text=True)
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
                self.verify_operator_death(shared, user)
            finally:
                for kind in ("Users", "Groups"):
                    subprocess.run(["sudo", "-n", "dscl", ".", "-delete", f"/{kind}/{user}"], capture_output=True)

    def verify_operator_death(self, shared, user):
        code = ("import sys,subprocess; from pathlib import Path; "
                "sys.path.insert(0,sys.argv[1]); from sandbox_sftp_policy import server_command; "
                "subprocess.Popen(server_command(Path(sys.argv[2]),False,sys.argv[3])).wait()")
        scripts = str(Path(__file__).resolve().parents[1])
        controller = subprocess.Popen([sys.executable, "-c", code, scripts, str(shared), user],
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=0)
        try:
            # Establish SFTP before killing the operator; keep our request pipe
            # open so EOF cleanup cannot accidentally make this proof pass.
            controller.stdin.write(b"\x00\x00\x00\x05\x01\x00\x00\x00\x03")
            header = read_bytes(controller.stdout, 4)
            self.assertEqual(len(header), 4)
            packet = read_bytes(controller.stdout, int.from_bytes(header, "big"))
            self.assertEqual(packet[:5], b"\x02\x00\x00\x00\x03")
            controller.kill()
            controller.wait(timeout=5)
            self.assertEqual(read_bytes(controller.stdout, 1), b"")
        finally:
            if controller.poll() is None:
                controller.kill()
                controller.wait()
            controller.stdin.close()
            controller.stdout.close()


def read_bytes(stream, count):
    result = b""
    deadline = time.monotonic() + 10
    while len(result) < count:
        remaining = max(0, deadline - time.monotonic())
        if not select.select([stream], [], [], remaining)[0]:
            raise ValueError("File helper did not respond or stop before timeout")
        chunk = os.read(stream.fileno(), count - len(result))
        if not chunk:
            break
        result += chunk
    return result
