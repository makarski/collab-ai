import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_tools_install as install  # noqa: E402

LOCK = json.loads((ROOT / "infra/image/tools.lock.json").read_text())


class FakeContainer(install.Container):
    """Answers the host's probes from a scripted store state and records pushes."""

    def __init__(self, name, state=None, current=None, complete=(), key=False, lock=LOCK):
        super().__init__("local", "proof", name, run=self.fake_run)
        self.state, self.current, self.complete, self.key, self.lock = state, current, list(complete), key, lock
        self.pushed, self.executed, self.activated = [], [], False

    def fake_run(self, command, check=False, **kwargs):
        args = tuple(command[command.index("--") + 1:]) if "--" in command else tuple(command)
        if command[3:5] == ["file", "push"]:
            self.pushed.append(Path(command[5]).name)
            return subprocess.CompletedProcess(command, 0)
        self.executed.append(tuple(args))
        output = ""
        if args[:2] == ("collab-tools-activate", "status"):
            output = json.dumps({"state": self.state, "current": self.current, "complete": self.complete})
        elif args[:1] == ("cat",):
            output = json.dumps(self.lock) if args[1].endswith("tools.lock.json") else '{"collab": "x"}'
        elif args[:2] == ("collab-tools-activate", "activate"):
            self.activated = True
            self.state, self.current = {"state": "ready"}, self.expected
        elif args[:1] == ("test",):
            return subprocess.CompletedProcess(command, 0 if self.key else 1)
        return subprocess.CompletedProcess(command, 0, stdout=output)


class SelectionTests(unittest.TestCase):
    def test_required_tools_are_always_on_and_unknown_names_are_rejected(self):
        selected = install.selection(["cs-mcp"])
        self.assertTrue(all(selected[name] for name in install.REQUIRED_TOOLS))
        self.assertEqual(install.tools_for("workspace", install.selection([])), list(install.REQUIRED_TOOLS))
        self.assertEqual(install.tools_for("secured", selected), ["codex", "claude"])
        with self.assertRaisesRegex(ValueError, "Unknown tools"):
            install.selection(["vim"])


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.cache = Path(self.temporary.name)
        self.selected = install.selection(["cs-mcp"])
        self.patch_cache = unittest.mock.patch.object(install, "cache_artifact", side_effect=self.cached)
        self.patch_cache.start()
        self.addCleanup(self.patch_cache.stop)

    def cached(self, item, cache=None):
        path = self.cache / item["sha256"]
        path.write_bytes(item["name"].encode())
        return path

    def expected(self, container):
        _, manifest = install.build_manifest(container, LOCK, "aarch64", self.selected)
        return install.generation_id(manifest)

    def test_fresh_root_receives_only_missing_artifacts_and_reaches_ready(self):
        container = FakeContainer("workspace", complete=[LOCK["artifacts"]["go"]["architectures"]["aarch64"]["sha256"]])
        container.expected = self.expected(container)
        generation = install.install(container, LOCK, "aarch64", self.selected, self.cache)
        self.assertEqual(generation, container.expected)
        self.assertTrue(container.activated)
        self.assertNotIn(LOCK["artifacts"]["go"]["architectures"]["aarch64"]["sha256"], container.pushed)
        self.assertIn(LOCK["artifacts"]["codex"]["architectures"]["aarch64"]["sha256"], container.pushed)
        self.assertIn("manifest.json", container.pushed)
        self.assertIn(("rm", "-rf", install.INCOMING), container.executed)

    def test_secured_gets_its_own_subset(self):
        container = FakeContainer("secured")
        container.expected = self.expected(container)
        install.install(container, LOCK, "aarch64", self.selected, self.cache)
        pushed_digests = {name for name in container.pushed if len(name) == 64}
        self.assertEqual(pushed_digests, {LOCK["artifacts"][name]["architectures"]["aarch64"]["sha256"]
                                          for name in ("codex", "claude")})

    def test_released_root_is_never_mutated(self):
        container = FakeContainer("workspace", state={"state": "ready"}, current="other")
        with self.assertRaisesRegex(ValueError, "needs a rollout"):
            install.install(container, LOCK, "aarch64", self.selected, self.cache)
        self.assertFalse(container.activated)
        self.assertEqual(container.pushed, [])

    def test_released_root_with_the_same_generation_is_left_alone(self):
        container = FakeContainer("workspace")
        container.state, container.current = {"state": "ready"}, self.expected(container)
        self.assertIsNone(install.install(container, LOCK, "aarch64", self.selected, self.cache))
        self.assertEqual(container.pushed, [])

    def test_image_lock_must_match_the_release_manifest(self):
        other = json.loads(json.dumps(LOCK))
        other["artifacts"]["go"]["version"] = "9.9.9"
        container = FakeContainer("workspace", lock=other)
        with self.assertRaisesRegex(ValueError, "differs from the release"):
            install.install(container, LOCK, "aarch64", self.selected, self.cache)

    def test_a_root_that_admitted_agents_is_never_mutated_even_without_ready_state(self):
        container = FakeContainer("workspace", state={"state": "maintenance"}, key=True)
        with self.assertRaisesRegex(ValueError, "released to agents without a ready tool state"):
            install.install(container, LOCK, "aarch64", self.selected, self.cache)
        self.assertFalse(container.activated)

    def test_probe_failures_are_errors_not_proof_of_absence(self):
        container = FakeContainer("workspace")
        original = container.fake_run

        def failing(command, check=False, **kwargs):
            if "test" in command:
                return subprocess.CompletedProcess(command, 255)
            return original(command, check=check, **kwargs)
        container.run = failing
        with self.assertRaisesRegex(ValueError, "could not probe"):
            install.never_released(container, None)

    def test_gated_units_are_released_after_activation_and_on_a_ready_root(self):
        container = FakeContainer("workspace")
        container.expected = self.expected(container)
        install.install(container, LOCK, "aarch64", self.selected, self.cache)
        self.assertIn(("systemctl", "start", *install.GATED_UNITS["workspace"]), container.executed)
        ready = FakeContainer("secured")
        ready.state, ready.current = {"state": "ready"}, self.expected(ready)
        install.install(ready, LOCK, "aarch64", self.selected, self.cache)
        self.assertIn(("systemctl", "start", "collab-tools-ready.service"), ready.executed)

    def test_build_provenance_must_match_the_release_when_given(self):
        container = FakeContainer("workspace")
        with self.assertRaisesRegex(ValueError, "build source differs"):
            install.install(container, LOCK, "aarch64", self.selected, self.cache, provenance={"collab": "other"})
        container = FakeContainer("workspace")
        container.expected = self.expected(container)
        install.install(container, LOCK, "aarch64", self.selected, self.cache, provenance={"collab": "x"})
        self.assertTrue(container.activated)

    def test_legacy_releases_without_an_artifact_table_keep_their_baked_tools(self):
        self.assertFalse(install.supports_tool_store({"go_version": "1.25.14"}))
        self.assertTrue(install.supports_tool_store(LOCK))

    def test_never_released_requires_no_ready_state_and_no_agent_key(self):
        self.assertTrue(install.never_released(FakeContainer("workspace"), None))
        self.assertTrue(install.never_released(FakeContainer("workspace"), {"state": "maintenance"}))
        self.assertFalse(install.never_released(FakeContainer("workspace", key=True), None))
        self.assertFalse(install.never_released(FakeContainer("workspace"), {"state": "ready"}))


if __name__ == "__main__":
    unittest.main()
