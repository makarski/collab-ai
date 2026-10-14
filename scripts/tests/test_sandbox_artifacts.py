import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile
import warnings

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_artifacts as artifacts


class ArtifactTests(unittest.TestCase):
    def installer(self, image, architecture="aarch64", digest=None):
        manifest = {"architecture": architecture, "image_fingerprint": digest or hashlib.sha256(image).hexdigest()}

        def obtain(name, destination):
            destination.write_bytes(json.dumps(manifest).encode() if name.endswith(".json") else image)
        return obtain

    def test_install_produces_local_pinned_variables(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            artifacts.install_artifact(output, "aarch64", self.installer(b"image"))
            variables = json.loads((output / "image.tfvars.json").read_text())
            self.assertEqual(variables["image_file"], str((output / "workspace.tar.gz").resolve()))
            self.assertEqual(variables["image_fingerprint"], hashlib.sha256(b"image").hexdigest())

    def test_corrupt_download_never_becomes_an_installable_image(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                artifacts.install_artifact(output, "aarch64", self.installer(b"corrupt", digest="0" * 64))
            self.assertFalse((output / "workspace.tar.gz").exists())
            self.assertFalse((output / "image.tfvars.json").exists())
            self.assertFalse((output / "workspace.tar.gz.partial").exists())

    def test_wrong_architecture_rejected_before_image_download(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            with self.assertRaisesRegex(ValueError, "architecture"):
                artifacts.install_artifact(output, "aarch64", self.installer(b"image", architecture="x86_64"))
            self.assertFalse((output / "workspace.tar.gz.partial").exists())


class BundleSourceTests(unittest.TestCase):
    def obtain_from(self, files, fetched):
        def obtain(name, target):
            fetched.append(name)
            if name not in files:
                raise FileNotFoundError(name)
            Path(target).write_bytes(files[name])
        return obtain

    def test_a_release_without_bundles_is_remembered_as_such(self):
        with tempfile.TemporaryDirectory() as directory:
            fetched = []
            self.assertIsNone(artifacts.fetch_bundles(Path(directory), "aarch64", self.obtain_from({}, fetched)))
            self.assertEqual(json.loads((Path(directory) / "bundle-source.json").read_text()), {"base": None})

    def test_a_remote_base_records_the_source_without_downloading_bundles(self):
        manifest = {"architecture": "arm64", "bundles": {"docker": {
            "file": "docker.tar.gz", "sha256": hashlib.sha256(b"d").hexdigest(), "packages": {"docker-ce": "5:29"}}}}
        files = {"collab-ai-bundles-linux-arm64.json": json.dumps(manifest).encode(), "docker.tar.gz": b"d"}
        with tempfile.TemporaryDirectory() as directory:
            fetched = []
            artifacts.fetch_bundles(Path(directory), "aarch64", self.obtain_from(files, fetched), "https://r/")
            self.assertEqual(fetched, ["collab-ai-bundles-linux-arm64.json"])
            self.assertEqual(json.loads((Path(directory) / "bundle-source.json").read_text())["base"], "https://r/")

    def test_a_local_source_copies_and_verifies_the_bundles(self):
        manifest = {"architecture": "arm64", "bundles": {"docker": {
            "file": "docker.tar.gz", "sha256": hashlib.sha256(b"d").hexdigest(), "packages": {"docker-ce": "5:29"}}}}
        files = {"collab-ai-bundles-linux-arm64.json": json.dumps(manifest).encode(), "docker.tar.gz": b"d"}
        with tempfile.TemporaryDirectory() as directory:
            artifacts.fetch_bundles(Path(directory), "aarch64", self.obtain_from(files, []))
            self.assertEqual((Path(directory) / "docker.tar.gz").read_bytes(), b"d")
            self.assertTrue(json.loads((Path(directory) / "bundle-source.json").read_text())["base"].startswith("file://"))
            files["docker.tar.gz"] = b"tampered"
            with tempfile.TemporaryDirectory() as other, self.assertRaisesRegex(ValueError, "does not match"):
                artifacts.fetch_bundles(Path(other), "aarch64", self.obtain_from(files, []))

    def test_unsafe_bundle_manifests_are_rejected_before_any_file_is_written(self):
        good = {"file": "docker.tar.gz", "sha256": "a" * 64, "packages": {"docker-ce": "5:29"}}
        cases = [({"architecture": "amd64", "bundles": {"docker": good}}, "another architecture"),
                 ({"architecture": "arm64", "bundles": {"docker": {**good, "file": "../outside"}}}, "unsafe"),
                 ({"architecture": "arm64", "bundles": {"docker": {**good, "file": "manifest.json"}}}, "reserved"),
                 ({"architecture": "arm64", "bundles": {"docker": {**good, "packages": {}}}}, "package inventory"),
                 ({"architecture": "arm64", "bundles": {"vim": good}}, "unknown bundle")]
        for manifest, message in cases:
            with self.subTest(message), tempfile.TemporaryDirectory() as directory:
                files = {"collab-ai-bundles-linux-arm64.json": json.dumps(manifest).encode()}
                with self.assertRaisesRegex(ValueError, message):
                    artifacts.fetch_bundles(Path(directory), "aarch64", self.obtain_from(files, []))
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_a_zip_without_bundles_is_absence_but_duplicates_are_errors(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "release.zip"
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr("collab-ai-workspace-linux-arm64.json", "{}")
            output = root / "image"
            output.mkdir()
            obtain = lambda name, target: artifacts.from_zip(archive, name, target)
            self.assertIsNone(artifacts.fetch_bundles(output, "aarch64", obtain))
            self.assertEqual(json.loads((output / "bundle-source.json").read_text()), {"base": None})
            with warnings.catch_warnings(), zipfile.ZipFile(archive, "a") as bundle:
                warnings.simplefilter("ignore", UserWarning)
                for _ in range(2):
                    bundle.writestr("collab-ai-bundles-linux-arm64.json", "{}")
            other = root / "other"
            other.mkdir()
            with self.assertRaisesRegex(ValueError, "exactly one"):
                artifacts.fetch_bundles(other, "aarch64", obtain)
            self.assertFalse((other / "bundle-source.json").exists())

