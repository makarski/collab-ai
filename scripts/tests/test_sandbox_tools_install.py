import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import unittest.mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_tools_install as install  # noqa: E402

LOCK = json.loads((ROOT / "infra/image/tools.lock.json").read_text())


class FakeContainer(install.Container):
    """Answers the host's probes from a scripted store state and records pushes."""

    def __init__(self, name, **overrides):
        super().__init__("local", "proof", name, run=self.fake_run)
        self.state, self.current, self.complete, self.key, self.lock, self.activator = None, None, [], False, LOCK, True
        self.pushed, self.executed, self.activated = [], [], False
        for attribute, value in overrides.items():
            setattr(self, attribute, value)

    def fake_run(self, command, check=False, **kwargs):
        if command[3:5] == ["file", "push"]:
            self.pushed.append(Path(command[5]).name)
            return subprocess.CompletedProcess(command, 0)
        args = tuple(command[command.index("--") + 1:])
        self.executed.append(args)
        return subprocess.CompletedProcess(command, 0, stdout=self.answer(args))

    def answer(self, args):
        if args[:2] == ("collab-tools-activate", "status"):
            return json.dumps({"state": self.state, "current": self.current, "complete": self.complete})
        if args[:1] == ("cat",):
            return json.dumps(self.lock) if args[1].endswith("tools.lock.json") else '{"collab": "x"}'
        if args[:2] == ("collab-tools-activate", "activate"):
            self.activated = True
            self.state, self.current = {"state": "ready"}, self.expected
        if args[:2] == ("sh", "-c") and "test" in args:
            return self.verdict(args[args.index("test") + 1])
        return ""

    def verdict(self, flag):
        present = self.key if flag == "-e" else self.activator
        return "present\n" if present else "absent\n"


RELEASE = install.Release(LOCK, "aarch64")


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
        _, manifest = install.build_manifest(container, RELEASE, self.selected)
        return install.generation_id(manifest)

    def test_fresh_root_receives_only_missing_artifacts_and_reaches_ready(self):
        container = FakeContainer("workspace", complete=[LOCK["artifacts"]["go"]["architectures"]["aarch64"]["sha256"]])
        container.expected = self.expected(container)
        generation = install.install(container, RELEASE, self.selected, self.cache)
        self.assertEqual(generation, container.expected)
        self.assertTrue(container.activated)
        self.assertNotIn(LOCK["artifacts"]["go"]["architectures"]["aarch64"]["sha256"], container.pushed)
        self.assertIn(LOCK["artifacts"]["codex"]["architectures"]["aarch64"]["sha256"], container.pushed)
        self.assertIn("manifest.json", container.pushed)
        self.assertIn(("rm", "-rf", install.INCOMING), container.executed)

    def test_secured_gets_its_own_subset(self):
        container = FakeContainer("secured")
        container.expected = self.expected(container)
        install.install(container, RELEASE, self.selected, self.cache)
        pushed_digests = {name for name in container.pushed if len(name) == 64}
        self.assertEqual(pushed_digests, {LOCK["artifacts"][name]["architectures"]["aarch64"]["sha256"]
                                          for name in ("codex", "claude")})

    def test_released_root_is_never_mutated(self):
        container = FakeContainer("workspace", state={"state": "ready"}, current="other")
        with self.assertRaisesRegex(ValueError, "needs a rollout"):
            install.install(container, RELEASE, self.selected, self.cache)
        self.assertFalse(container.activated)
        self.assertEqual(container.pushed, [])

    def test_released_root_with_the_same_generation_is_left_alone(self):
        container = FakeContainer("workspace")
        container.state, container.current = {"state": "ready"}, self.expected(container)
        self.assertIsNone(install.install(container, RELEASE, self.selected, self.cache))
        self.assertEqual(container.pushed, [])

    def test_image_lock_must_match_the_release_manifest(self):
        other = json.loads(json.dumps(LOCK))
        other["artifacts"]["go"]["version"] = "9.9.9"
        container = FakeContainer("workspace", lock=other)
        with self.assertRaisesRegex(ValueError, "differs from the release"):
            install.install(container, RELEASE, self.selected, self.cache)

    def test_a_root_that_admitted_agents_is_never_mutated_even_without_ready_state(self):
        container = FakeContainer("workspace", state={"state": "maintenance"}, key=True)
        with self.assertRaisesRegex(ValueError, "released to agents or is in an unknown tool state"):
            install.install(container, RELEASE, self.selected, self.cache)
        self.assertFalse(container.activated)

    def test_probe_failures_are_errors_not_proof_of_absence(self):
        for outcome in (subprocess.CompletedProcess([], 255), subprocess.CompletedProcess([], 1, stdout=""),
                        subprocess.CompletedProcess([], 0, stdout="Error: not found\n")):
            container = FakeContainer("workspace")
            original = container.fake_run

            def failing(command, check=False, **kwargs):
                if "test" in command:
                    return outcome
                return original(command, check=check, **kwargs)
            container.run = failing
            with self.assertRaisesRegex(ValueError, "could not probe"):
                install.never_released(container, None)

    def test_gated_units_are_released_after_activation_and_on_a_ready_root(self):
        container = FakeContainer("workspace")
        container.expected = self.expected(container)
        install.install(container, RELEASE, self.selected, self.cache)
        self.assertIn(("systemctl", "start", *install.GATED_UNITS["workspace"]), container.executed)
        ready = FakeContainer("secured")
        ready.state, ready.current = {"state": "ready"}, self.expected(ready)
        install.install(ready, RELEASE, self.selected, self.cache)
        self.assertIn(("systemctl", "start", "collab-tools-ready.service"), ready.executed)

    def test_build_provenance_must_match_the_release_when_given(self):
        container = FakeContainer("workspace")
        with self.assertRaisesRegex(ValueError, "build source differs"):
            install.install(container, install.Release(LOCK, "aarch64", {"collab": "other"}), self.selected, self.cache)
        container = FakeContainer("workspace")
        container.expected = self.expected(container)
        install.install(container, install.Release(LOCK, "aarch64", {"collab": "x"}), self.selected, self.cache)
        self.assertTrue(container.activated)

    def test_an_image_without_a_manifest_installs_from_the_roots_own_lock(self):
        container = FakeContainer("workspace")
        unknown = install.Release(None, "aarch64")
        _, manifest = install.build_manifest(container, unknown, self.selected)
        container.expected = install.generation_id(manifest)
        self.assertEqual(install.install(container, unknown, self.selected, self.cache), container.expected)
        self.assertTrue(container.activated)

    def test_legacy_releases_without_an_artifact_table_keep_their_baked_tools(self):
        self.assertFalse(install.supports_tool_store({"go_version": "1.25.14"}))
        self.assertTrue(install.supports_tool_store(LOCK))

    def test_a_root_without_the_activator_is_recognised_as_baked(self):
        container = FakeContainer("workspace")
        self.assertTrue(install.has_tool_store(container))
        container.activator = False
        self.assertFalse(install.has_tool_store(container))

    def test_unknown_state_blocks_install_at_the_entry_point(self):
        container = FakeContainer("workspace", state={"state": "unknown"})
        with self.assertRaisesRegex(ValueError, "unknown tool state"):
            install.install(container, RELEASE, self.selected, self.cache)
        self.assertFalse(container.activated)

    def test_never_released_requires_no_ready_state_and_no_agent_key(self):
        self.assertTrue(install.never_released(FakeContainer("workspace"), None))
        self.assertTrue(install.never_released(FakeContainer("workspace"), {"state": "maintenance"}))
        self.assertFalse(install.never_released(FakeContainer("workspace", key=True), None))
        self.assertFalse(install.never_released(FakeContainer("workspace"), {"state": "ready"}))


if __name__ == "__main__":
    unittest.main()
