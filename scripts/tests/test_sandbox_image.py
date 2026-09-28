import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
import sys
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


image = module("sandbox_image", "scripts/sandbox-image.py")
installer = module("install_tools", "infra/image/install-tools.py")


class ImageTests(unittest.TestCase):
    def test_archive_cannot_assign_installed_code_to_the_agent(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "vendor.tar.gz"
            destination = Path(directory) / "installed"
            destination.mkdir()
            with tarfile.open(archive, "w:gz") as out:
                info = tarfile.TarInfo("executable")
                info.uid = info.gid = 1001
                info.mode = 0o777
                info.size = 4
                out.addfile(info, io.BytesIO(b"code"))
            installer.extract(archive, destination)
            actual = (destination / "executable").stat()
            self.assertEqual(actual.st_uid, os.getuid())
            self.assertEqual(actual.st_mode & 0o777, 0o755)

    def test_archive_contains_only_build_inputs_and_records_their_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "source.tar"
            provenance = image.source_archive(archive)
            with tarfile.open(archive) as source:
                names = source.getnames()
                self.assertIn("cmd/broker/main.go", names)
                self.assertIn("infra/image/tools.lock.json", names)
                self.assertIn("infra/image/collab-secured-setup.service", names)
                self.assertIn("infra/image/collab-supervised-codex", names)
                self.assertIn("infra/image/collab-dev-executor.socket", names)
                self.assertIn("infra/image/secured-environments.toml", names)
                self.assertNotIn(".git/config", names)
                self.assertNotIn("infra/incus/sandbox.auto.tfvars", names)
                self.assertNotIn("scripts/sandbox-image.py", names)
                for name, digest in provenance["files_sha256"].items():
                    self.assertEqual(hashlib.sha256(source.extractfile(name).read()).hexdigest(), digest)

    def test_download_rejects_wrong_content_before_installation(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(installer.urllib.request, "urlopen", return_value=io.BytesIO(b"wrong")):
                with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                    installer.download("https://example.invalid/tool", "0" * 64, Path(directory) / "tool")

    def test_failed_project_creation_never_deletes_an_existing_project(self):
        from argparse import Namespace
        calls = []

        def run(args, **kwargs):
            calls.append(args)
            if args[1] == "query":
                return subprocess.CompletedProcess(args, 0, json.dumps({"environment": {"architectures": ["aarch64"]}}))
            raise subprocess.CalledProcessError(1, args)

        with tempfile.TemporaryDirectory() as directory, patch.object(image, "run", side_effect=run):
            with patch.object(image, "source_archive", return_value={}):
                with self.assertRaises(subprocess.CalledProcessError):
                    image.build(Namespace(remote="local", network="incusbr0"), Path(directory))
        self.assertFalse(any("delete" in call for call in calls))

    def test_lock_pins_both_architectures_with_valid_checksums(self):
        lock = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
        self.assertEqual(set(lock["architectures"]), {"aarch64", "x86_64"})
        for arch in lock["architectures"].values():
            for key in ("base_image", "go_sha256", "codex_sha256", "claude_sha256"):
                self.assertRegex(arch[key], r"^[0-9a-f]{64}$")

    def test_build_failure_cleans_only_the_project_created_by_this_run(self):
        from argparse import Namespace
        calls = []

        def run(args, **kwargs):
            calls.append((args, kwargs))
            if args[1] == "query":
                return subprocess.CompletedProcess(args, 0, json.dumps({"environment": {"architectures": ["aarch64"]}}))
            if "init" in args:
                raise subprocess.CalledProcessError(1, args)
            return subprocess.CompletedProcess(args, 0, "[]")

        with tempfile.TemporaryDirectory() as directory, patch.object(image, "run", side_effect=run):
            with patch.object(image, "source_archive", return_value={}):
                with self.assertRaises(subprocess.CalledProcessError):
                    image.build(Namespace(remote="local", network="incusbr0", storage_pool="default"), Path(directory))
        created = next(args[3] for args, _ in calls if args[1:3] == ["project", "create"])
        deleted = [(args, kwargs) for args, kwargs in calls if args[1:3] == ["project", "delete"]]
        self.assertEqual(len(deleted), 1)
        self.assertEqual(deleted[0][0][3], created)
        self.assertTrue(created.startswith("local:collab-build-"))
        self.assertNotIn("--force", deleted[0][0])

    def test_cleanup_deletes_resources_only_in_owned_project(self):
        def result(command, **kwargs):
            resources = []
            if command[3:5] == ["list", "local:"]:
                resources = [{"name": "builder"}]
            if command[3:5] == ["image", "list"]:
                resources = [{"fingerprint": "a" * 64}]
            return subprocess.CompletedProcess(command, 0, json.dumps(resources))

        with patch.object(image, "run", side_effect=result) as run:
            image.cleanup_project("local", "collab-build-test")
        commands = [call.args[0] for call in run.call_args_list]
        self.assertIn(["incus", "--project", "collab-build-test", "delete", "local:builder", "--force"], commands)
        self.assertIn(["incus", "--project", "collab-build-test", "image", "delete", "local:" + "a" * 64], commands)
        self.assertEqual(commands[-1], ["incus", "project", "delete", "local:collab-build-test"])


if __name__ == "__main__":
    unittest.main()
