"""Regression tests from the independent base mirror implementation review."""

from io import BytesIO
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import sandbox_base_image as base
import sandbox_base_mirror as mirror
import sandbox_base_refresh as refresh
from test_sandbox_base_acceptance import FILES, FP, download, release, upstream_fixture


class SourceValidationTests(unittest.TestCase):
    def test_malformed_store_records_never_select_upstream(self):
        for records in ([None], [{}], [{"fingerprint": None}]):
            with self.subTest(records=records), patch.object(base, "run", return_value=
                    subprocess.CompletedProcess([], 0, json.dumps(records))):
                with self.assertRaises(base.Unavailable):
                    base.in_store("remote", "project", FP)

    def test_json_null_is_not_http_404(self):
        with patch.object(base, "urlopen", return_value=BytesIO(b"null")):
            with self.assertRaises(base.Unavailable):
                base.mirror_release("owner/repo", FP)

    def test_missing_or_non_boolean_draft_field_is_not_a_published_release(self):
        for draft in (None, "false", 0):
            data = release()
            if draft is None:
                del data["draft"]
            else:
                data["draft"] = draft
            with self.subTest(draft=draft), patch.object(base, "urlopen", return_value=BytesIO(json.dumps(data).encode())):
                with self.assertRaises((base.Unavailable, base.IntegrityError)):
                    base.mirror_release("owner/repo", FP)

    def test_refresh_rejects_invalid_fingerprint_without_writing_lock(self):
        index = upstream_fixture()
        index["products"]["ubuntu:noble:amd64:default"]["versions"]["20261018_05:25"]["items"]["incus.tar.xz"]["combined_squashfs_sha256"] = "invalid-pin"
        lock = {"architectures": {"aarch64": {"base_image": "1" * 64}, "x86_64": {"base_image": "2" * 64}}}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "lock.json"
            original = json.dumps(lock)
            path.write_text(original)
            with self.assertRaises(ValueError):
                refresh.refresh(path, index)
            self.assertEqual(path.read_text(), original)


class PublisherReviewTests(unittest.TestCase):
    def state(self, draft=False, names=None):
        data = release()
        data["draft"] = draft
        data["assets"] = [{"id": i + 1, **asset} for i, asset in enumerate(data["assets"])
                          if names is None or asset["name"] in names]
        return data

    def pull(self, repo, asset, destination):
        Path(destination).write_bytes(FILES[asset["name"]])

    def test_new_mirror_stages_verified_pair_and_publishes_only_after_readback(self):
        index = upstream_fixture()
        index["products"]["ubuntu:noble:arm64:default"]["versions"]["20261018_05:25"]["items"]["incus.tar.xz"]["combined_squashfs_sha256"] = FP
        events = []
        uploaded = {}

        def run(command, **kwargs):
            events.append(command[2])
            self.assertIn("--repo", command)
            if command[2] == "create":
                self.assertIn("--draft", command)
                self.assertIn("--latest=false", command)
            elif command[2] == "upload":
                for path in command[-2:]:
                    uploaded[Path(path).name] = Path(path).read_bytes()
                self.assertEqual(uploaded, FILES)
            elif command[2] == "edit":
                self.assertIn("--draft=false", command)
                self.assertIn("--latest=false", command)
                self.assertEqual(events[-3:-1], ["readback", "readback"])

        def readback(repo, asset, destination):
            events.append("readback")
            Path(destination).write_bytes(uploaded[asset["name"]])

        with patch.object(mirror, "find_release", side_effect=[None, self.state(draft=True)]), \
                patch.object(mirror, "fetch", side_effect=download), patch.object(mirror, "run", side_effect=run), \
                patch.object(mirror, "pull_asset", side_effect=readback), \
                patch.object(mirror, "mirror_release", return_value=self.state()):
            self.assertEqual(mirror.publish_one("owner/repo", "aarch64", FP, lambda: index), "published")
        self.assertEqual(events, ["create", "upload", "readback", "readback", "edit"])

    def test_verified_published_release_survives_upstream_pruning_without_writes(self):
        with patch.object(mirror, "find_release", return_value=self.state()), \
                patch.object(mirror, "pull_asset", side_effect=self.pull), patch.object(mirror, "run") as run:
            result = mirror.publish_one("owner/repo", "aarch64", FP,
                                        lambda: self.fail("published mirror must not consult upstream"))
        self.assertEqual(result, "verified")
        run.assert_not_called()

    def test_corrupt_published_release_is_never_overwritten(self):
        def corrupt(repo, asset, destination):
            Path(destination).write_bytes(b"corrupt")
        with patch.object(mirror, "find_release", return_value=self.state()), \
                patch.object(mirror, "pull_asset", side_effect=corrupt), patch.object(mirror, "run") as run:
            with self.assertRaises(base.IntegrityError):
                mirror.publish_one("owner/repo", "aarch64", FP,
                                   lambda: self.fail("corruption must not trigger upstream fallback"))
        run.assert_not_called()

    def test_complete_draft_is_verified_before_publication_without_upstream(self):
        events = []

        def pull(repo, asset, destination):
            events.append(("pull", asset["name"]))
            self.pull(repo, asset, destination)

        def run(command, **kwargs):
            events.append(("write", command))

        with patch.object(mirror, "find_release", return_value=self.state(draft=True)), \
                patch.object(mirror, "pull_asset", side_effect=pull), patch.object(mirror, "run", side_effect=run), \
                patch.object(mirror, "mirror_release", return_value=self.state()):
            self.assertEqual(mirror.publish_one("owner/repo", "aarch64", FP,
                             lambda: self.fail("verified draft must survive upstream pruning")), "published")
        self.assertEqual([kind for kind, _ in events], ["pull"] * 4 + ["write"])
        command = events[-1][1]
        self.assertEqual(command[:3], ["gh", "release", "edit"])
        self.assertIn("--draft=false", command)
        self.assertIn("--latest=false", command)

    def test_second_read_detects_corrupted_upload_and_does_not_publish(self):
        count = 0

        def pull(repo, asset, destination):
            nonlocal count
            count += 1
            Path(destination).write_bytes(FILES[asset["name"]] if count <= 2 else b"changed on server")

        with patch.object(mirror, "find_release", return_value=self.state(draft=True)), \
                patch.object(mirror, "pull_asset", side_effect=pull), patch.object(mirror, "run") as run:
            with self.assertRaises(base.IntegrityError):
                mirror.publish_one("owner/repo", "aarch64", FP,
                                   lambda: self.fail("valid initial pair needs no upstream"))
        run.assert_not_called()

    def test_incomplete_draft_retains_valid_asset_when_upstream_is_gone(self):
        with patch.object(mirror, "find_release", return_value=self.state(draft=True, names=["incus.tar.xz"])), \
                patch.object(mirror, "pull_asset", side_effect=self.pull), patch.object(mirror, "run") as run:
            with self.assertRaisesRegex(ValueError, "refresh"):
                mirror.publish_one("owner/repo", "aarch64", FP, lambda: {"products": {}})
        run.assert_not_called()

    def test_availability_of_published_mirror_never_depends_on_upstream(self):
        lock = {"architectures": {"aarch64": {"base_image": FP}}}
        with patch.object(mirror, "mirror_release", return_value=self.state()), \
                patch.object(mirror, "upstream_index", side_effect=AssertionError("upstream pruned")):
            self.assertEqual(mirror.check("owner/repo", lock), {FP: "mirror"})


if __name__ == "__main__":
    unittest.main()
