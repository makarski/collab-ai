import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

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
