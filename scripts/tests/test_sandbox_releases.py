from io import BytesIO
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_releases as releases


class ReleaseTests(unittest.TestCase):
    def response(self, tag="workspace-v1.2.3", **kwargs):
        return BytesIO(json.dumps(dict(tag_name=tag, draft=False, **kwargs)).encode())

    def test_latest_is_resolved_once_and_both_assets_use_the_same_tag(self):
        def prepare(arch, obtain, output):
            obtain("image.json", Path("manifest"))
            obtain("image.tar.gz", Path("archive"))
            return Path("/cache/current"), {"image_fingerprint": "a" * 64}
        with patch.object(releases, "urlopen", return_value=self.response()) as api, \
             patch.object(releases, "prepare_image", side_effect=prepare), patch.object(releases, "fetch") as fetch, \
             patch.object(releases, "record_bundles") as bundles:
            directory = releases.download_release("aarch64")
        self.assertEqual(api.call_count, 1)
        self.assertTrue(api.call_args.args[0].full_url.endswith("/releases/latest"))
        self.assertEqual(directory, Path("/cache") / ("a" * 64))
        self.assertEqual(bundles.call_args.args[0], directory)  # bundles are recorded for the frozen selection
        for call in fetch.call_args_list:
            self.assertIn("/releases/download/workspace-v1.2.3/", call.args[0])

    def test_explicit_tag_does_not_follow_latest(self):
        with patch.object(releases, "urlopen", return_value=self.response()) as api:
            self.assertEqual(releases.resolve_release("owner/repo", "workspace-v1.2.3"), "workspace-v1.2.3")
        self.assertTrue(api.call_args.args[0].full_url.endswith("/releases/tags/workspace-v1.2.3"))

    def test_release_lookup_authentication_and_token_precedence(self):
        cases = (
            ({}, None),
            ({"GITHUB_TOKEN": "test-github"}, "Bearer test-github"),
            ({"GH_TOKEN": "test-gh"}, "Bearer test-gh"),
            ({"GITHUB_TOKEN": "test-github", "GH_TOKEN": "test-gh"}, "Bearer test-github"),
            ({"GITHUB_TOKEN": "", "GH_TOKEN": "test-gh"}, "Bearer test-gh"),
        )
        for environment, expected in cases:
            with self.subTest(environment=environment), patch.dict(releases.os.environ, environment, clear=True), \
                 patch.object(releases, "urlopen", return_value=self.response()) as api:
                self.assertEqual(releases.resolve_release("owner/repo", "latest"), "workspace-v1.2.3")
                request = api.call_args.args[0]
                self.assertEqual(request.get_header("Authorization"), expected)
                self.assertEqual(request.full_url, "https://api.github.com/repos/owner/repo/releases/latest")

    def test_rate_limit_errors_stop_before_asset_download(self):
        for code in (403, 429):
            error = HTTPError("url", code, "Rate limit", {"X-RateLimit-Remaining": "0"}, None)
            with self.subTest(code=code), patch.object(releases, "urlopen", side_effect=error), \
                 patch.object(releases, "prepare_image") as prepare:
                with self.assertRaisesRegex(ValueError, "GitHub API rate limit reached") as raised:
                    releases.download_release("aarch64")
                self.assertIn("GITHUB_TOKEN", str(raised.exception))
                self.assertNotIn("Cannot find published release", str(raised.exception))
                prepare.assert_not_called()

    def test_missing_release_fails_before_download(self):
        error = HTTPError("url", 404, "Not found", {}, None)
        with patch.object(releases, "urlopen", side_effect=error), patch.object(releases, "prepare_image") as prepare:
            with self.assertRaisesRegex(ValueError, "Cannot find published release") as raised:
                releases.download_release("aarch64")
            self.assertIn("HTTP 404", str(raised.exception))
            self.assertIn("successful main release build", str(raised.exception))
            self.assertNotIn("rate limit", str(raised.exception))
        prepare.assert_not_called()

    def test_wrong_tag_and_moving_tag_are_rejected(self):
        for tag in ("latest", "workspace-v9.9.9"):
            with self.subTest(tag=tag), patch.object(releases, "urlopen", return_value=self.response(tag)):
                with self.assertRaisesRegex(ValueError, "versioned"):
                    releases.resolve_release("owner/repo", "workspace-v1.2.3")
