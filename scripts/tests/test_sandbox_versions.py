import argparse
import copy
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_versions as versions


class VersionTests(unittest.TestCase):
    def test_default_lock_needs_no_network(self):
        with patch.object(versions, "publisher_json", side_effect=AssertionError("Unexpected network")):
            lock = versions.resolve_lock(ROOT / "infra/image/tools.lock.json", "aarch64", argparse.Namespace())
        self.assertEqual(set(lock["architectures"]), {"aarch64"})

    def test_override_resolves_a_new_checksum_without_reusing_another_architecture(self):
        original = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
        expected = copy.deepcopy(original)
        args = argparse.Namespace(codex_version="0.157.0")
        with patch.object(versions, "codex_checksum", return_value="a" * 64) as resolve:
            lock = versions.resolve_lock(ROOT / "infra/image/tools.lock.json", "aarch64", args)
        self.assertEqual(resolve.call_args.args[0], "0.157.0")
        self.assertEqual(lock["codex_version"], "0.157.0")
        self.assertEqual(lock["architectures"]["aarch64"]["codex_sha256"], "a" * 64)
        self.assertNotIn("x86_64", lock["architectures"])
        self.assertEqual(json.loads((ROOT / "infra/image/tools.lock.json").read_text()), expected)

    def test_codex_missing_publisher_digest_is_rejected(self):
        release = {"assets": [{"name": "codex-package-test.tar.gz", "digest": None}]}
        with patch.object(versions, "publisher_json", return_value=release):
            with self.assertRaisesRegex(ValueError, "No publisher SHA256"):
                versions.codex_checksum("0.157.0", {"codex_target": "test"})

    def test_claude_manifest_must_match_selected_version(self):
        with patch.object(versions, "publisher_json", return_value={"version": "2.0.0"}):
            with self.assertRaisesRegex(ValueError, "differs"):
                versions.claude_checksum("2.1.283", {})

    def test_go_archive_must_match_version_and_architecture(self):
        releases = [{"files": [
            {"filename": "go1.25.14.linux-amd64.tar.gz", "sha256": "b" * 64},
            {"filename": "go1.25.14.linux-arm64.tar.gz", "sha256": "a" * 64},
        ]}]
        with patch.object(versions, "publisher_json", return_value=releases):
            self.assertEqual(versions.go_checksum("1.25.14", {"go_arch": "arm64"}), "a" * 64)
            with self.assertRaisesRegex(ValueError, "unavailable"):
                versions.go_checksum("1.24.0", {"go_arch": "arm64"})

    def test_moving_versions_are_rejected(self):
        for value in ("latest", "stable", "1.25", "v0.156.1", "0.156.1/../../file"):
            with self.assertRaises(argparse.ArgumentTypeError):
                versions.version(value)
