import hashlib
import io
import json
import os
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
import tool_store  # noqa: E402


def tar_bytes(entries):
    """entries: (name, kind, payload-or-target). kind: file|dir|sym|hard|dev."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
        for name, kind, value in entries:
            info = tarfile.TarInfo(name)
            if kind == "file":
                data = value.encode()
                info.size, info.mode = len(data), 0o755
                tar.addfile(info, io.BytesIO(data))
                continue
            info.type = {"dir": tarfile.DIRTYPE, "sym": tarfile.SYMTYPE, "hard": tarfile.LNKTYPE,
                         "dev": tarfile.CHRTYPE}[kind]
            if kind in ("sym", "hard"):
                info.linkname = value
            tar.addfile(info)
    return buffer.getvalue()


def digest(data):
    return hashlib.sha256(data).hexdigest()


class StoreHarness(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.incoming = root / "incoming"
        self.incoming.mkdir()
        self.store = tool_store.Store(root / "store", root / "state.json")
        self.mode = root / "tools-mode"
        self.mode.write_text("store\n")
        self.lock, self.source = root / "lock.json", root / "source.json"
        self.lock.write_text("{}"), self.source.write_text("{}")

    def deliver(self, name, data, **spec):
        (self.incoming / f"{name}.bin").write_bytes(data)
        tool = {"kind": "archive", "entry": "bin/tool", "version": "1.0.0", **spec,
                "sha256": digest(data), "file": f"{name}.bin"}
        return {name: tool}

    def manifest(self, tools, selection=None):
        return {"release": digest(b"{}"), "lock_sha256": digest(b"{}"), "architecture": "aarch64",
                "selection": selection or {}, "tools": tools}

    def ready(self):
        return tool_store.ready(self.store, self.lock, self.source, self.mode)


class ActivationTests(StoreHarness):
    def test_activation_builds_a_generation_and_releases_the_root(self):
        tools = self.deliver("go", tar_bytes([("bin", "dir", None), ("bin/tool", "file", "#!/bin/sh\n")]))
        self.assertEqual(self.ready(), "sandbox tools are in maintenance; run the rollout to activate them")
        generation = self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(self.store.resolve("go").read_text(), "#!/bin/sh\n")
        self.assertEqual(tool_store.read_state(self.store.state_path)["state"], "ready")
        self.assertEqual(self.store.status()["current"], generation)
        self.assertIsNone(self.ready())
        self.assertEqual(oct(self.store.artifacts.stat().st_mode & 0o777), "0o755")

    def test_ready_is_terminal_and_a_released_root_refuses_another_activation(self):
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", "a")]))
        self.store.activate(self.manifest(tools), self.incoming)
        with self.assertRaisesRegex(tool_store.StoreError, "already has an activated generation"):
            self.store.activate(self.manifest(tools), self.incoming)

    def test_digest_mismatch_leaves_maintenance_state_and_nothing_staged(self):
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", "a")]))
        tools["go"]["sha256"] = "0" * 64
        with self.assertRaisesRegex(tool_store.StoreError, "does not match its digest"):
            self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(tool_store.read_state(self.store.state_path)["state"], "maintenance")
        self.assertEqual(self.store.complete_artifacts(), set())
        self.assertFalse(self.store.current.exists())
        self.assertTrue(self.ready())

    def test_release_mismatch_is_not_ready_in_store_mode_and_ignored_in_baked_mode(self):
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", "a")]))
        self.store.activate(self.manifest(tools), self.incoming)
        self.source.write_text('{"other": true}')
        self.assertIn("different release", self.ready())
        self.mode.write_text("baked\n")
        self.assertIsNone(self.ready())

    def test_binary_and_zip_kinds_install_their_entry_executable(self):
        tools = self.deliver("claude", b"ELF", kind="binary", entry="claude")
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as package:
            package.writestr("cs-mcp-linux-aarch64", "mcp")
            package.writestr("../escape", "no")
        tools.update(self.deliver("cs-mcp", archive.getvalue(), kind="zip", entry="cs-mcp-linux-aarch64"))
        self.store.activate(self.manifest(tools), self.incoming)
        for name in ("claude", "cs-mcp"):
            path = self.store.resolve(name)
            self.assertTrue(path.is_file() and os.access(path, os.X_OK), name)
        self.assertFalse(list(self.store.artifacts.rglob("escape")))


class ArchivePolicyTests(StoreHarness):
    def rejects(self, entries, pattern):
        tools = self.deliver("go", tar_bytes(entries))
        with self.assertRaisesRegex(tool_store.StoreError, pattern):
            self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual(self.store.complete_artifacts(), set())

    def test_escaping_and_absolute_names_are_rejected_before_extraction(self):
        self.rejects([("../bin/tool", "file", "a")], "escapes")
        self.rejects([("/bin/tool", "file", "a")], "escapes")
        self.rejects([("bin/../../tool", "file", "a")], "escapes")

    def test_duplicates_and_case_collisions_are_rejected(self):
        self.rejects([("bin/tool", "file", "a"), ("./bin/tool", "file", "b")], "Duplicate")
        self.rejects([("bin/tool", "file", "a"), ("bin/TOOL", "file", "b")], "colliding")

    def test_only_safe_member_types_and_inside_links_are_accepted(self):
        self.rejects([("bin/tool", "file", "a"), ("dev/null", "dev", None)], "Unsupported")
        self.rejects([("bin/tool", "file", "a"), ("bin/link", "sym", "../../etc/passwd")], "escapes")
        self.rejects([("bin/tool", "file", "a"), ("bin/link", "sym", "/etc/passwd")], "Absolute")
        self.rejects([("bin/link", "hard", "bin/missing"), ("bin/tool", "file", "a")], "unknown member")
        tools = self.deliver("go", tar_bytes([("bin", "dir", None), ("bin/tool", "file", "a"),
                                              ("bin/alias", "sym", "tool"), ("bin/copy", "hard", "bin/tool")]))
        self.store.activate(self.manifest(tools), self.incoming)
        self.assertEqual((self.store.resolve("go").parent / "alias").read_text(), "a")


class GenerationTests(StoreHarness):
    def activate_fresh(self, data):
        self.store.state_path.unlink(missing_ok=True)  # a new root
        tools = self.deliver("go", tar_bytes([("bin/tool", "file", data)]))
        return self.store.activate(self.manifest(tools), self.incoming), tools["go"]["sha256"]

    def test_prune_keeps_current_and_previous_generations_only(self):
        first, first_sha = self.activate_fresh("one")
        second, second_sha = self.activate_fresh("two")
        third, third_sha = self.activate_fresh("three")
        remaining = {path.name for path in self.store.generations.iterdir()}
        self.assertEqual(remaining, {second, third})
        self.assertEqual(self.store.complete_artifacts(), {second_sha, third_sha})
        self.assertEqual(self.store.resolve("go").read_text(), "three")

    def test_incomplete_leftovers_are_removed_before_staging(self):
        (self.store.artifacts).mkdir(parents=True)
        (self.store.artifacts / "garbage").mkdir()
        (self.store.generations).mkdir()
        (self.store.generations / "half").mkdir()
        self.activate_fresh("one")
        self.assertFalse((self.store.artifacts / "garbage").exists())
        self.assertFalse((self.store.generations / "half").exists())


if __name__ == "__main__":
    unittest.main()
