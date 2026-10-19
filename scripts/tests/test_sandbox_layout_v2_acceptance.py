"""Layout publication must certify a complete, compatible image."""

import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = ROOT / "infra/image/layout-v2.json"
INSTALLER = ROOT / "infra/image/install-layout.py"
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_tools_install as tools_install
from test_sandbox_tools_install import FakeContainer, LOCK
from test_sandbox_ssh import ssh


class LayoutMatrixContainer(FakeContainer):
    def __init__(self, version, **kwargs):
        super().__init__("workspace", **kwargs)
        self.version = version
        self.layout_queries = 0

    def answer(self, args):
        if args[:2] == ("python3", "-c") and "layout.json" in args[2]:
            self.layout_queries += 1
            return json.dumps({"absent": True} if self.version is None else {"marker": {"layout": self.version}})
        if args and args[0] == "/usr/local/lib/pairtask/pairtask-tools-activate":
            args = ("collab-tools-activate", *args[1:])
        return super().answer(args)


class LayoutV2InstallerAcceptance(unittest.TestCase):
    def setUp(self):
        self.assertTrue(INSTALLER.is_file(), "layout installer has not been implemented")
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.root = self.directory / "image"
        self.root.mkdir()
        self.spec = json.loads(SPEC.read_text())
        self.spec_file = self.directory / "layout.json"
        self.spec_file.write_text(json.dumps(self.spec))
        self.populate()

    def inside(self, absolute):
        return self.root / absolute.lstrip("/")

    def populate(self):
        layout = self.spec["layout"]
        directories = {layout["share_dir"], layout["etc_dir"], "/usr/local/lib/pairtask"}
        for directory in directories:
            self.inside(directory).mkdir(parents=True, exist_ok=True)
        targets = {item["target"] for item in self.spec["forwarders"]}
        targets.update(value for key, value in layout.items() if key != "units")
        for target in targets - directories:
            path = self.inside(target)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("#!/bin/sh\nexit 0\n")
            path.chmod(0o755)
        for entry in self.spec["unit_aliases"]:
            path = self.inside("/etc/systemd/system/" + entry["unit"])
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("[Service]\nExecStart=/bin/true\n[Install]\nAlias=" + entry["alias"] + "\n")

    def invoke(self, action, success=True):
        result = subprocess.run([sys.executable, str(INSTALLER), action, "--root", str(self.root),
                                 "--layout", str(self.spec_file)], cwd=self.directory,
                                capture_output=True, text=True)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, "invalid image was accepted")
            self.assertFalse(self.inside(self.spec["marker"]).exists(), "failed installation advertised layout 2")
        return result

    def enable_aliases(self):
        for entry in self.spec["unit_aliases"]:
            path = self.inside("/etc/systemd/system/" + entry["alias"])
            self.assertTrue(path.is_symlink(), "apply did not create the unit alias")
            self.assertEqual(path.resolve(strict=True), self.inside("/etc/systemd/system/" + entry["unit"]).resolve(strict=True))

    def test_apply_creates_relative_forwarders_but_does_not_publish(self):
        self.invoke("apply")
        self.assertFalse(self.inside(self.spec["marker"]).exists())
        for entry in self.spec["forwarders"]:
            with self.subTest(legacy=entry["legacy"]):
                legacy, target = self.inside(entry["legacy"]), self.inside(entry["target"])
                self.assertTrue(legacy.is_symlink())
                self.assertFalse(legacy.readlink().is_absolute())
                self.assertEqual(legacy.resolve(strict=True), target.resolve(strict=True))

    def test_finalize_requires_unit_aliases_including_executor_template(self):
        self.invoke("apply")
        self.enable_aliases()
        template = self.inside("/etc/systemd/system/collab-dev-executor@.service")
        self.assertTrue(template.is_symlink(), "executor template is absent from compatibility contract")
        target = template.readlink()
        template.unlink()
        self.invoke("finalize", success=False)
        template.symlink_to(target)
        self.invoke("finalize")
        self.assertEqual(json.loads(self.inside(self.spec["marker"]).read_text()), {"layout": 2})

    def test_apply_and_finalize_are_idempotent_and_leave_stored_state_alone(self):
        sentinels = ("/var/lib/collab-ai-secured/broker/broker.db",
                     "/home/agent/.local/state/collab-ai/rtk-initialized",
                     "/var/lib/collab-ai-tools/current-generation-sentinel")
        for name in sentinels:
            path = self.inside(name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"user-owned state\x00")
        self.invoke("apply")
        self.enable_aliases()
        self.invoke("finalize")
        before = {entry["legacy"]: self.inside(entry["legacy"]).readlink() for entry in self.spec["forwarders"]}
        self.invoke("apply")
        self.invoke("finalize")
        self.assertEqual(before, {entry["legacy"]: self.inside(entry["legacy"]).readlink() for entry in self.spec["forwarders"]})
        for name in sentinels:
            self.assertEqual(self.inside(name).read_bytes(), b"user-owned state\x00")

    def test_missing_canonical_helper_prevents_install(self):
        self.inside(self.spec["layout"]["tools_ready"]).unlink()
        self.invoke("apply", success=False)

    def test_finalize_rechecks_targets_and_detects_dangling_forwarder(self):
        self.invoke("apply")
        self.enable_aliases()
        self.inside(self.spec["layout"]["tools_activate"]).unlink()
        self.invoke("finalize", success=False)

    def test_finalize_rejects_nonexecutable_canonical_readiness_helper(self):
        self.invoke("apply")
        self.inside(self.spec["layout"]["tools_ready"]).chmod(0o644)
        self.invoke("finalize", success=False)

    def test_canonical_target_cannot_be_a_symlink_outside_the_root(self):
        target = self.inside(self.spec["layout"]["tools_ready"])
        target.unlink()
        outside = self.directory / "outside-helper"
        outside.write_text("#!/bin/sh\nexit 0\n")
        outside.chmod(0o755)
        target.symlink_to(outside)
        self.invoke("apply", success=False)
        self.assertEqual(outside.read_text(), "#!/bin/sh\nexit 0\n")

    def test_existing_regular_file_is_never_replaced(self):
        path = self.inside("/usr/local/bin/collab-tools-ready")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("operator-owned file")
        self.invoke("apply", success=False)
        self.assertEqual(path.read_text(), "operator-owned file")
        self.assertFalse(path.is_symlink())

    def test_wrong_existing_link_is_never_retargeted(self):
        path = self.inside("/usr/local/bin/collab-tools-ready")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.symlink_to("/unexpected/operator-file")
        self.invoke("apply", success=False)
        self.assertEqual(str(path.readlink()), "/unexpected/operator-file")

    def test_symlink_parent_cannot_escape_the_supplied_root(self):
        outside = self.directory / "outside"
        outside.mkdir()
        (outside / "sentinel").write_text("untouched")
        self.inside("/etc").rename(self.directory / "saved-etc")
        self.inside("/etc").symlink_to(outside, target_is_directory=True)
        self.invoke("apply", success=False)
        self.assertEqual(sorted(path.name for path in outside.iterdir()), ["sentinel"])
        self.assertEqual((outside / "sentinel").read_text(), "untouched")

    def test_wrong_unit_alias_cannot_certify_the_image(self):
        self.invoke("apply")
        self.enable_aliases()
        alias = self.inside("/etc/systemd/system/collab-broker.service")
        alias.unlink()
        alias.symlink_to("pairtask-tools-ready.service")
        self.invoke("finalize", success=False)

    def test_invalid_contract_is_rejected_before_forwarders_are_written(self):
        mutations = (
            lambda data: data.update(version=True),
            lambda data: data.update(marker="/usr/local/../escaped.json"),
            lambda data: data["forwarders"][0].update(legacy="relative/path"),
            lambda data: data["forwarders"][0].update(target="/usr/../outside"),
            lambda data: data["forwarders"][0].update(lifetime="forever-ish"),
            lambda data: data["forwarders"].append(copy.deepcopy(data["forwarders"][0])),
            lambda data: data["unit_aliases"].append(copy.deepcopy(data["unit_aliases"][0])),
            lambda data: data["layout"]["units"].update(broker="../../bad.service"),
            lambda data: data["layout"].update(unexpected="/extra"),
        )
        for mutate in mutations:
            data = copy.deepcopy(self.spec)
            mutate(data)
            self.spec_file.write_text(json.dumps(data))
            with self.subTest(spec=data):
                self.invoke("apply", success=False)
                self.assertFalse(self.inside("/usr/local/bin/collab-tools-ready").is_symlink())


class LayoutV2HostAcceptance(unittest.TestCase):
    def test_new_host_drives_each_layout_with_one_consistent_contract(self):
        for version in (None, 1, 2):
            with self.subTest(version=version), tempfile.TemporaryDirectory() as directory:
                container = LayoutMatrixContainer(version)
                release = tools_install.Release(LOCK, "aarch64")
                selected = tools_install.selection([])
                self.assertTrue(tools_install.has_tool_store(container))
                _, manifest = tools_install.build_manifest(container, release, selected)
                container.expected = tools_install.generation_id(manifest)
                archive = Path(directory) / "artifact"
                archive.write_bytes(b"cached test artifact")
                with patch.object(tools_install, "cache_artifact", return_value=archive):
                    self.assertEqual(tools_install.install(container, release, selected), container.expected)
                expected_share = "/usr/local/share/pairtask" if version == 2 else "/usr/local/share/collab-ai"
                expected_etc = "/etc/pairtask" if version == 2 else "/etc/collab-ai"
                expected_activate = "/usr/local/lib/pairtask/pairtask-tools-activate" if version == 2 else "/usr/local/bin/collab-tools-activate"
                prefix = "pairtask-" if version == 2 else "collab-"
                self.assertIn(("cat", expected_share + "/tools.lock.json"), container.executed)
                self.assertIn((expected_activate, "status"), container.executed)
                self.assertIn(("install", "-d", "-m", "0755", expected_etc), container.executed)
                starts = [args for args in container.executed if args[:2] == ("systemctl", "start")]
                self.assertTrue(starts)
                self.assertTrue(all(unit.startswith(prefix) for args in starts for unit in args[2:]))
                self.assertEqual(container.layout_queries, 1)

    def test_layout2_missing_activator_is_an_error_not_a_baked_image(self):
        container = LayoutMatrixContainer(2, activator=False)
        with self.assertRaises((ValueError, OSError, subprocess.SubprocessError)):
            tools_install.has_tool_store(container)
        self.assertEqual(container.pushed, [])
        self.assertFalse(container.activated)

    def test_layout2_missing_or_nonexecutable_readiness_blocks_ssh_keys(self):
        for missing in (True, False):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as directory:
                gate = Path(directory) / "readiness"
                if not missing:
                    gate.write_text("#!/bin/sh\nexit 0\n")
                    gate.chmod(0o644)
                args = SimpleNamespace(remote="local", project="proof", instance="workspace",
                                       state_dir=Path(directory) / "ssh")

                def run(command, **kwargs):
                    if "python3" in command and "-c" in command:
                        return subprocess.CompletedProcess(command, 0, stdout='{"marker":{"layout":2}}\n')
                    if any("pairtask-tools-ready" in item for item in command):
                        remote = command[command.index("--") + 1:]
                        translated = [item.replace("/usr/local/lib/pairtask/pairtask-tools-ready", str(gate)) for item in remote]
                        return subprocess.run(translated, check=True, capture_output=True, text=True)
                    if "tee" in command or "ssh-keygen" in command:
                        self.fail("layout 2 admitted SSH keys without an executable readiness gate")
                    return subprocess.CompletedProcess(command, 0, stdout="")

                with patch.object(ssh.shutil, "which", side_effect=lambda name: "/usr/bin/" + name), \
                     patch.object(ssh, "run", side_effect=run):
                    with self.assertRaises((OSError, subprocess.SubprocessError, ValueError)):
                        ssh.setup(args)

    def test_required_legacy_contract_and_template_alias_are_present(self):
        spec = json.loads(SPEC.read_text())
        forwards = {entry["legacy"]: entry for entry in spec["forwarders"]}
        for legacy in ("/usr/local/bin/collab-tools-ready", "/usr/local/bin/collab-tools-activate",
                       "/usr/local/bin/collab-supervised-codex", "/usr/local/bin/collab-codex",
                       "/usr/local/share/collab-ai", "/etc/collab-ai"):
            self.assertEqual(forwards[legacy]["lifetime"], "permanent")
        self.assertEqual(forwards["/usr/local/bin/collab"]["lifetime"], "release")
        aliases = {entry["alias"]: entry["unit"] for entry in spec["unit_aliases"]}
        self.assertEqual(aliases["collab-dev-executor@.service"], "pairtask-dev-executor@.service")

    def test_host_loads_checked_in_layout_independently_of_cwd(self):
        source = ("import json, sandbox_layout as m\n"
                  "class Image:\n"
                  " def output(self, *args): return '{\"marker\":{\"layout\":2}}\\n'\n"
                  "value=m.discover(Image())\n"
                  "print(json.dumps([value.version,value.cli,value.tools_ready,value.sshd_config]))\n")
        with tempfile.TemporaryDirectory() as directory:
            environment = dict(os.environ, PYTHONPATH=str(ROOT / "scripts"))
            result = subprocess.run([sys.executable, "-c", source], cwd=directory, env=environment,
                                    capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), [2, "/usr/local/bin/pairtask",
                         "/usr/local/lib/pairtask/pairtask-tools-ready", "/etc/ssh/sshd_config.collab-ai"])

    def test_image_marker_cannot_supply_host_execution_paths(self):
        import sandbox_layout
        class Image:
            def output(self, *args):
                return '{"marker":{"layout":2,"tools_ready":"/bin/true"}}\n'
        with self.assertRaises(ValueError):
            sandbox_layout.discover(Image())

    def test_layout_installer_and_contract_are_recipe_inputs(self):
        import sandbox_source
        self.assertIn("infra/image/layout-v2.json", sandbox_source.RECIPE_PATHS)
        self.assertIn("infra/image/install-layout.py", sandbox_source.RECIPE_PATHS)


if __name__ == "__main__":
    unittest.main()
