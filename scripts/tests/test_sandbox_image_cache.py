import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
import warnings
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from sandbox_artifacts import asset_name, from_zip
from sandbox_image_cache import prepare_image


class ImageCacheTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.cache = self.root / "images"

    def bundle(self, image=b"image", digest=None):
        name = asset_name("aarch64")
        archive = self.root / (hashlib.sha256(image).hexdigest() + ".zip")
        manifest = {"architecture": "aarch64", "image_fingerprint": digest or hashlib.sha256(image).hexdigest()}
        with zipfile.ZipFile(archive, "w") as bundle:
            bundle.writestr(name + ".json", json.dumps(manifest))
            bundle.writestr(name + ".tar.gz", image)
            bundle.writestr("../outside", "must not extract")
        return archive

    def prepare(self, archive):
        return prepare_image("aarch64", lambda name, target: from_zip(archive, name, target), cache=self.cache)[0]

    def test_zip_is_verified_without_extracting_unrelated_paths(self):
        current = self.prepare(self.bundle())
        self.assertTrue(current.is_symlink())
        variables = json.loads((current / "image.tfvars.json").read_text())
        self.assertEqual(variables["image_file"], str(current.resolve() / "workspace.tar.gz"))
        self.assertFalse((self.root / "outside").exists())
        self.assertEqual(list(self.cache.glob(".download-*")), [])

    def test_upgrades_keep_previously_selected_image_paths_valid(self):
        current = self.prepare(self.bundle(b"old"))
        selected = current.resolve()
        before = (selected / "image.tfvars.json").read_bytes()
        self.prepare(self.bundle(b"new"))
        self.assertNotEqual(current.resolve(), selected)
        self.assertEqual((selected / "workspace.tar.gz").read_bytes(), b"old")
        self.assertEqual((selected / "image.tfvars.json").read_bytes(), before)

    def test_repeated_download_reuses_verified_image(self):
        archive = self.bundle()
        current = self.prepare(archive)
        before = (current / "workspace.tar.gz").stat().st_ino
        self.prepare(archive)
        self.assertEqual((current / "workspace.tar.gz").stat().st_ino, before)

    def test_bad_download_leaves_current_selection_unchanged(self):
        current = self.prepare(self.bundle(b"keep"))
        selected = current.resolve()
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.prepare(self.bundle(b"bad", digest="a" * 64))
        self.assertEqual(current.resolve(), selected)
        self.assertEqual(list(self.cache.glob(".download-*")), [])

    def test_current_directory_and_tampered_cache_are_not_overwritten(self):
        current = self.prepare(self.bundle())
        (current / "workspace.tar.gz").write_bytes(b"tampered")
        with self.assertRaisesRegex(ValueError, "Cached image checksum mismatch"):
            self.prepare(self.bundle())
        current.unlink()
        current.mkdir()
        with self.assertRaisesRegex(ValueError, "Refusing to replace"):
            self.prepare(self.bundle())

    def test_wrong_architecture_and_duplicate_assets_are_rejected(self):
        archive = self.bundle()
        with self.assertRaisesRegex(ValueError, "matching architecture"):
            prepare_image("x86_64", lambda name, target: from_zip(archive, name, target), cache=self.cache)
        with warnings.catch_warnings(), zipfile.ZipFile(archive, "a") as bundle:
            warnings.simplefilter("ignore", UserWarning)
            bundle.writestr(asset_name("aarch64") + ".json", "{}")
        with self.assertRaisesRegex(ValueError, "exactly one"):
            self.prepare(archive)

    def test_explicit_output_still_refuses_to_overwrite(self):
        output = self.root / "existing"
        output.mkdir()
        with self.assertRaises(FileExistsError):
            prepare_image("aarch64", lambda *_: self.fail("Must not download"), output=output)
