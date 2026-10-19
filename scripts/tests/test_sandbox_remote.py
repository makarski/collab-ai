"""The host drives Linux local or the macOS Colima VM; the old VM names keep working for one release."""

import contextlib
import io
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_remote as remote


class RemoteTests(unittest.TestCase):
    def setUp(self):
        remote._warned = False

    def test_new_remote_maps_to_the_pairtask_profile_without_warning(self):
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(remote.colima_profile("colima-pairtask"), "pairtask")
        self.assertEqual(stderr.getvalue(), "")

    def test_legacy_remote_maps_to_the_old_profile_with_one_warning(self):
        with contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(remote.colima_profile("colima-collab-ai"), "collab-ai")
            self.assertEqual(remote.colima_profile("colima-collab-ai"), "collab-ai")
        self.assertEqual(stderr.getvalue().count("deprecated"), 1)
        self.assertIn("colima-pairtask", stderr.getvalue())

    def test_unknown_remote_has_no_profile(self):
        self.assertIsNone(remote.colima_profile("local"))
        self.assertIsNone(remote.colima_profile("other"))

    def test_default_remote_follows_the_platform(self):
        with patch.object(remote.platform, "system", return_value="Darwin"):
            self.assertEqual(remote.default_remote(), "colima-pairtask")
        with patch.object(remote.platform, "system", return_value="Linux"):
            self.assertEqual(remote.default_remote(), "local")


if __name__ == "__main__":
    unittest.main()
