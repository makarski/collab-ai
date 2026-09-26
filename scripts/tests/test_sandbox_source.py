from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_source as source


class SourceTests(unittest.TestCase):
    def test_selected_commit_is_independent_of_dirty_checkout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            for name in ("cmd", "internal"):
                (root / name).mkdir()
                (root / name / "example.go").write_text("package example\n")
            (root / "go.mod").write_text("module original\n")
            (root / "go.sum").write_text("")
            source.git("add", ".", root=root)
            source.git("-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                       "commit", "-qm", "Initial", root=root)
            revision = source.commit("HEAD", root)
            (root / "go.mod").write_text("module modified\n")
            (root / "cmd/untracked-secret").write_text("do not package")
            files, metadata = source.app_source(root, revision)
            self.assertEqual(files["go.mod"], b"module original\n")
            self.assertFalse(metadata["working_tree"])
            files, metadata = source.app_source(root, "working-tree")
            self.assertEqual(files["go.mod"], b"module modified\n")
            self.assertTrue(metadata["dirty"])
            self.assertNotIn("cmd/untracked-secret", files)

    def test_local_symlink_cannot_import_host_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "source.go").symlink_to("/etc/passwd")
            with self.assertRaisesRegex(ValueError, "symlink"):
                source.repository_file(root, "source.go")
