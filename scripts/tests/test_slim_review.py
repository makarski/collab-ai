import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock
from urllib.error import HTTPError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_artifacts as artifacts
import sandbox_tools_install as install


class SlimModeReviewTests(unittest.TestCase):
    def test_mode_transport_failure_does_not_select_baked_tools(self):
        container = Mock()
        container.output.side_effect = subprocess.CalledProcessError(1, ["incus", "exec"])
        with self.assertRaises((ValueError, subprocess.SubprocessError)):
            install.store_mode(container)

    def test_unknown_mode_is_not_treated_as_baked(self):
        for value in ("", "unknown\n", "store\nbaked\n"):
            with self.subTest(value=value):
                container = Mock()
                container.output.return_value = value
                with self.assertRaises(ValueError):
                    install.store_mode(container)


class BundleDownloadReviewTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.data = b"verified bundle"
        self.manifest = {"architecture": "arm64", "bundles": {"docker": {
            "file": "docker.tar.gz", "sha256": hashlib.sha256(self.data).hexdigest(),
            "packages": {"example": "1.0"}}}}

    def test_http_failure_is_not_cached_as_missing_bundles(self):
        for code in (403, 429, 500, 503):
            with self.subTest(status=code):
                obtain = Mock(side_effect=HTTPError("https://example.invalid/manifest", code, "error", {}, None))
                with self.assertRaises(HTTPError):
                    artifacts.fetch_bundles(self.root, "aarch64", obtain, artifacts.BundleSource("https://example.invalid/"))
                self.assertFalse((self.root / "bundle-source.json").exists())

    def test_ambiguous_zip_manifest_is_not_cached_as_missing(self):
        obtain = Mock(side_effect=ValueError("expected exactly one manifest"))
        with self.assertRaises(ValueError):
            artifacts.fetch_bundles(self.root, "aarch64", obtain)
        self.assertFalse((self.root / "bundle-source.json").exists())

    def test_interrupted_local_bundle_copy_can_be_retried(self):
        interrupted = True

        def obtain(name, target):
            data = json.dumps(self.manifest).encode() if name.endswith(".json") else self.data
            with target.open("xb") as stream:
                stream.write(data)
            if name.endswith(".gz") and interrupted:
                raise OSError("interrupted bundle copy")

        with self.assertRaises(OSError):
            artifacts.fetch_bundles(self.root, "aarch64", obtain)
        self.assertFalse((self.root / "bundle-source.json").exists())
        interrupted = False
        result = artifacts.fetch_bundles(self.root, "aarch64", obtain)
        self.assertEqual(result, self.manifest)
        self.assertEqual((self.root / "docker.tar.gz").read_bytes(), self.data)

    def test_bundle_paths_cannot_escape_or_overwrite_image_metadata(self):
        for name in ("../escape.tar.gz", "workspace.tar.gz", "manifest.json", "bundle-source.json"):
            with self.subTest(name=name):
                self.manifest["bundles"]["docker"]["file"] = name
                requested = []

                def obtain(asset, target):
                    requested.append(asset)
                    if asset == artifacts.bundles_name("aarch64"):
                        target.write_text(json.dumps(self.manifest))
                    else:
                        raise AssertionError("unsafe bundle path reached the download callback")

                with self.assertRaises(ValueError):
                    artifacts.fetch_bundles(self.root, "aarch64", obtain)
                self.assertEqual(requested, [artifacts.bundles_name("aarch64")])

    def test_bundle_architecture_must_match_image(self):
        self.manifest["architecture"] = "amd64"

        def obtain(name, target):
            target.write_text(json.dumps(self.manifest))

        with self.assertRaises(ValueError):
            artifacts.fetch_bundles(self.root, "aarch64", obtain, artifacts.BundleSource("https://example.invalid/"))
        self.assertFalse((self.root / "bundle-source.json").exists())
