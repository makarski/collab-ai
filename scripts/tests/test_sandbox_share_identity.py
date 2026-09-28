"""Reject identities that would share operator or administrative group access."""

from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_share_identity as identity


class SharingIdentityTests(unittest.TestCase):
    def setUp(self):
        self.account = SimpleNamespace(pw_name="collab-share", pw_uid=60000,
                                       pw_gid=60000, pw_shell="/usr/sbin/nologin")
        self.group = SimpleNamespace(gr_name="collab-share", gr_mem=[])
        self.users = [self.account]
        for target, options in (
            ("pwd.getpwnam", {"return_value": self.account}),
            ("grp.getgrgid", {"return_value": self.group}),
            ("pwd.getpwall", {"side_effect": lambda: self.users}),
            ("os.getuid", {"return_value": 1000}),
            ("os.getgrouplist", {"return_value": [60000]}),
        ):
            patcher = patch("sandbox_share_identity." + target, **options)
            patcher.start()
            self.addCleanup(patcher.stop)
        environment = patch.dict(identity.os.environ, {}, clear=True)
        environment.start()
        self.addCleanup(environment.stop)

    def test_accepts_only_dedicated_ids(self):
        self.assertEqual(identity.sharing_identity("collab-share"), {"uid": 60000, "gid": 60000})

    def test_rejects_root_operator_and_sudo_operator(self):
        with patch.dict(identity.os.environ, {"SUDO_UID": "2000"}):
            for uid in (0, -1, 1000, 2000):
                self.account.pw_uid = uid
                with self.subTest(uid=uid), self.assertRaises(ValueError):
                    identity.sharing_identity("collab-share")

    def test_rejects_interactive_shell_and_supplementary_groups(self):
        self.account.pw_shell = "/bin/bash"
        with self.assertRaisesRegex(ValueError, "shell"):
            identity.sharing_identity("collab-share")
        self.account.pw_shell = "/usr/sbin/nologin"
        with patch.object(identity.os, "getgrouplist", return_value=[60000, 27]), \
                self.assertRaisesRegex(ValueError, "supplementary"):
            identity.sharing_identity("collab-share")

    def test_rejects_shared_or_admin_primary_group(self):
        for name, members in (("sudo", []), ("collab-share", ["operator"])):
            self.group.gr_name, self.group.gr_mem = name, members
            with self.subTest(name=name, members=members), self.assertRaisesRegex(ValueError, "private"):
                identity.sharing_identity("collab-share")

    def test_rejects_duplicate_uid_or_primary_gid(self):
        for uid, gid in ((60000, 50000), (50000, 60000)):
            self.users = [self.account, SimpleNamespace(pw_name="operator", pw_uid=uid, pw_gid=gid)]
            with self.subTest(uid=uid, gid=gid), self.assertRaises(ValueError):
                identity.sharing_identity("collab-share")

    def test_missing_account_has_actionable_error(self):
        with patch.object(identity.pwd, "getpwnam", side_effect=KeyError), \
                self.assertRaisesRegex(ValueError, "Create a dedicated sharing account"):
            identity.sharing_identity("missing")
