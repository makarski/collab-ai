"""Mac sharing validation must not silently expand host access."""

from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_mac_shares as shares
from sandbox_share_links import uncovered_links


class MacShareTests(unittest.TestCase):
    def mount(self, source="/Users/operator/workspace/project", **changes):
        return dict(project_name="project", host_path=source, container_mount_path=source, **changes)

    def validate(self, mounts):
        with patch.object(shares, "canonical_source", side_effect=lambda source: source):
            return shares.validate_mounts(mounts)

    def test_preserved_paths_and_readonly_default(self):
        mounts = self.validate([self.mount()])
        self.assertTrue(mounts[0]["container_readonly"])
        self.assertEqual(mounts[0]["host_path"], mounts[0]["container_mount_path"])

    def test_rejects_remapping_sensitive_roots_and_overlaps(self):
        bad = [[], {}, [self.mount("/Users/operator")], [self.mount("/Users/operator/workspace")],
               [self.mount("/etc/config")], [dict(self.mount(), container_mount_path="/workspace/project")],
               [self.mount(), dict(self.mount("/Users/operator/workspace/project/child"), project_name="child")]]
        for manifest in bad:
            with self.subTest(manifest=manifest), self.assertRaises(ValueError):
                self.validate(manifest)

    def test_setup_never_runs_acl_changes_as_root_or_follows_symlinks(self):
        mounts = self.validate([self.mount(container_readonly=False)])
        commands = shares.acl_commands(mounts, "collab-share")
        self.assertTrue(commands)
        for command in commands:
            self.assertEqual(command[0], "/bin/chmod")
            if "-R" in command:
                self.assertIn("-P", command)
        self.assertTrue(any("file_inherit" in " ".join(command) for command in commands))

    def test_reports_unshared_targets_without_following_or_granting_them(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            project, reference = root / "project", root / "reference"
            project.mkdir()
            reference.mkdir()
            (project / "link").symlink_to(reference)
            (reference / "loop").symlink_to(project)
            mounts = [self.mount(str(project))]
            self.assertEqual(list(uncovered_links(mounts)), [(project / "link", reference)])
            mounts.append(dict(self.mount(str(reference)), project_name="reference"))
            self.assertEqual(list(uncovered_links(mounts)), [])


class MacIdentityTests(unittest.TestCase):
    def setUp(self):
        self.account = SimpleNamespace(pw_name="collab-share", pw_uid=60000, pw_gid=60000,
                                       pw_dir="/var/empty", pw_shell="/usr/bin/false")
        self.group = SimpleNamespace(gr_name="collab-share", gr_mem=[])

    def test_rejects_operator_root_and_login_accounts(self):
        with patch.object(shares.os, "getuid", return_value=501):
            for uid in (0, -1, 501):
                self.account.pw_uid = uid
                with self.subTest(uid=uid), self.assertRaises(ValueError):
                    shares.validate_login(self.account)
            self.account.pw_uid = 60000
            self.account.pw_shell = "/bin/zsh"
            with self.assertRaises(ValueError):
                shares.validate_login(self.account)

    def test_accepts_everyone_but_rejects_admin_and_staff_groups(self):
        with patch.object(shares.os, "getgrouplist", return_value=[60000, 12]):
            shares.validate_groups(self.account, self.group)
        for extra in (20, 80, 0):
            with self.subTest(group=extra), \
                    patch.object(shares.os, "getgrouplist", return_value=[60000, extra]), \
                    self.assertRaises(ValueError):
                shares.validate_groups(self.account, self.group)

    def test_never_adopts_occupied_account_or_ids(self):
        with patch.object(shares.pwd, "getpwall", return_value=[self.account]), \
                patch.object(shares.grp, "getgrall", return_value=[]), \
                self.assertRaises(ValueError):
            shares.unused_account("collab-share-new", 60000)
