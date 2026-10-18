"""Independent acceptance cases for the host/image layout boundary.

The first slice recognizes legacy images and rejects unimplemented layouts.
Actual two-layout upgrade proofs belong to the atomic image rename slice.
"""

import importlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_tools_install as install
from test_sandbox_tools_install import FakeContainer, LOCK
from test_sandbox_ssh import ssh


class LayoutDiscoveryAcceptance(unittest.TestCase):
    def setUp(self):
        self.layout = importlib.import_module("sandbox_layout")

    def discover(self, response):
        container = SimpleNamespace(output=Mock(return_value=response))
        result = self.layout.discover(container)
        container.output.assert_called_once_with("python3", "-c", self.layout.PROBE)
        return result

    def test_confirmed_absence_and_explicit_legacy_select_identical_contract(self):
        for response in ('{"absent":true}\n', '{"marker":{"layout":1}}\n'):
            with self.subTest(response=response):
                selected = self.discover(response)
                self.assertEqual(selected, self.layout.LEGACY)
                self.assertEqual(selected.version, 1)
                self.assertEqual(selected.tools_ready, "/usr/local/bin/collab-tools-ready")
                self.assertEqual(selected.tools_activate, "/usr/local/bin/collab-tools-activate")
                self.assertEqual(selected.share_dir, "/usr/local/share/collab-ai")
                self.assertEqual(selected.etc_dir, "/etc/collab-ai")
                self.assertEqual(selected.sshd_config, "/etc/ssh/sshd_config.collab-ai")
                self.assertEqual(selected.units["workspace_setup"], "collab-workspace-setup.service")

    def test_missing_or_ambiguous_probe_evidence_never_means_legacy(self):
        replies = ("", "\n", "connection refused", "null", "[]", "{}", "true",
                   '{"absent":false}', '{"absent":1}', '{"absent":null}',
                   '{"marker":null}', '{"absent":true,"marker":{"layout":1}}',
                   '{"absent":true,"extra":1}', '{"marker":{"layout":1},"extra":1}',
                   '{"absent":true}\n{"absent":true}',
                   'diagnostic\n{"absent":true}', '{"absent":true}\ntrailer')
        for response in replies:
            with self.subTest(response=response):
                container = SimpleNamespace(output=Mock(return_value=response))
                with self.assertRaises(ValueError):
                    self.layout.discover(container)
                self.assertEqual(container.output.call_count, 1)

    def test_present_marker_requires_exact_integer_version_schema(self):
        for marker in (None, [], True, 1, "1", {}, {"layout":True}, {"layout":1.0},
                       {"layout":"1"}, {"layout":None}, {"version":1},
                       {"layout":1, "extra":"ignored?"}):
            with self.subTest(marker=marker):
                with self.assertRaises(ValueError):
                    self.discover(json.dumps({"marker":marker}) + "\n")

    def test_unimplemented_versions_require_newer_host(self):
        for version in (0, 2, 3, -1, 999):
            with self.subTest(version=version):
                with self.assertRaisesRegex(ValueError, "newer host"):
                    self.discover(json.dumps({"marker":{"layout":version}}))

    def test_transport_and_permission_failures_are_not_retried(self):
        for failure in (subprocess.CalledProcessError(1, ["incus"]),
                        subprocess.CalledProcessError(255, ["incus"]),
                        PermissionError("denied"), OSError("disconnected")):
            with self.subTest(failure=failure):
                container = SimpleNamespace(output=Mock(side_effect=failure))
                with self.assertRaises((OSError, subprocess.SubprocessError, ValueError)):
                    self.layout.discover(container)
                self.assertEqual(container.output.call_count, 1)

    def test_selected_contract_cannot_be_changed_for_other_operations(self):
        selected = self.discover('{"absent":true}')
        with self.assertRaises((AttributeError, TypeError)):
            selected.tools_ready = "/bin/true"
        with self.assertRaises(TypeError):
            selected.units["tools_ready"] = "unrelated.service"


class RealLayoutProbeAcceptance(unittest.TestCase):
    """Execute the actual remote program, with only its filesystem root redirected."""

    def setUp(self):
        self.layout = importlib.import_module("sandbox_layout")
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.marker = Path(temporary.name) / "layout.json"
        self.commands = []

    def output(self, *argv):
        self.commands.append(argv)
        self.assertEqual(argv[:2], ("python3", "-c"))
        self.assertIn(self.layout.MARKER, argv[2])
        program = argv[2].replace(self.layout.MARKER, str(self.marker))
        return subprocess.run([sys.executable, "-c", program], check=True,
                              capture_output=True, text=True).stdout

    def test_real_absence_is_the_only_missing_file_success(self):
        self.assertEqual(self.layout.discover(self), self.layout.LEGACY)
        self.assertEqual(len(self.commands), 1)

    def test_real_explicit_legacy_marker(self):
        self.marker.write_text('{"layout":1}\n')
        self.assertEqual(self.layout.discover(self), self.layout.LEGACY)

    def test_present_null_does_not_become_missing_file(self):
        self.marker.write_text("null\n")
        with self.assertRaises(ValueError):
            self.layout.discover(self)

    def test_real_malformed_and_nonobject_files_are_rejected(self):
        for content in ("", "{broken", "[]", '"legacy"', '{"layout":1}\n{}'):
            with self.subTest(content=content):
                self.marker.write_text(content)
                with self.assertRaises((ValueError, subprocess.CalledProcessError)):
                    self.layout.discover(self)

    def test_marker_directory_is_not_absence(self):
        self.marker.mkdir()
        with self.assertRaises((ValueError, subprocess.CalledProcessError)):
            self.layout.discover(self)

    def test_permission_failure_in_real_probe_is_not_absence(self):
        # Fault injection works under both root CI and an ordinary developer UID.
        program = ("from unittest.mock import patch\n"
                   "with patch('builtins.open', side_effect=PermissionError('denied')), "
                   "patch('io.open', side_effect=PermissionError('denied')):\n"
                   "    exec(" + repr(self.layout.PROBE) + ")\n")
        result = subprocess.run([sys.executable, "-c", program], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")


class LayoutAwareFixture(FakeContainer):
    """Existing store protocol plus one independently controlled layout reply."""

    def __init__(self, response='{"absent":true}', failure=None, **kwargs):
        super().__init__("workspace", **kwargs)
        self.layout_response, self.layout_failure = response, failure
        self.layout_queries = 0

    def answer(self, args):
        if args[:2] == ("python3", "-c") and "layout.json" in args[2]:
            self.layout_queries += 1
            if self.layout_failure:
                raise self.layout_failure
            return self.layout_response
        # The contract uses absolute helper paths; keep the prior fake's semantics.
        if args and args[0] == "/usr/local/bin/collab-tools-activate":
            args = ("collab-tools-activate", *args[1:])
        if args[:2] == ("sh", "-c") and any(arg.endswith("/tools-mode") for arg in args[3:]):
            return f"mode:{self.mode}\n"
        return super().answer(args)


class LayoutConsumerAcceptance(unittest.TestCase):
    def test_bad_layout_blocks_install_before_artifact_delivery_or_activation(self):
        for response in ('{"marker":{"layout":999}}', '{"marker":null}', ""):
            with self.subTest(response=response):
                container = LayoutAwareFixture(response)
                with patch.object(install, "cache_artifact") as cache:
                    with self.assertRaises(ValueError):
                        install.install(container, install.Release(LOCK, "aarch64"), install.selection([]))
                cache.assert_not_called()
                self.assertEqual(container.pushed, [])
                self.assertFalse(container.activated)
                self.assertEqual(container.layout_queries, 1)

    def test_failed_layout_probe_cannot_fall_back_to_baked_image(self):
        failure = subprocess.CalledProcessError(1, ["incus"])
        container = LayoutAwareFixture(failure=failure)
        with self.assertRaises((ValueError, subprocess.SubprocessError)):
            install.has_tool_store(container)
        self.assertEqual(container.layout_queries, 1)
        self.assertEqual(container.pushed, [])

    def test_legacy_store_discovery_uses_one_snapshot_per_container(self):
        container = LayoutAwareFixture(mode="store")
        self.assertTrue(install.has_tool_store(container))
        self.assertTrue(install.store_mode(container))
        install.status(container)
        self.assertEqual(container.layout_queries, 1)

    def test_failed_activation_can_retry_without_releasing_entrypoints_early(self):
        container = LayoutAwareFixture()
        release = install.Release(LOCK, "aarch64")
        selected = install.selection([])
        _, manifest = install.build_manifest(container, release, selected)
        container.expected = install.generation_id(manifest)
        original = container.answer

        def interrupted(args):
            if args[0].endswith("collab-tools-activate") and args[1] == "activate":
                container.state = {"state":"maintenance"}
                raise subprocess.CalledProcessError(1, list(args))
            return original(args)

        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "artifact"
            archive.write_bytes(b"test artifact")
            with patch.object(install, "cache_artifact", return_value=archive), \
                 patch.object(container, "answer", side_effect=interrupted):
                with self.assertRaises(subprocess.CalledProcessError):
                    install.install(container, release, selected)
            self.assertFalse(any(args[:2] == ("systemctl", "start") for args in container.executed))
            with patch.object(install, "cache_artifact", return_value=archive):
                self.assertEqual(install.install(container, release, selected), container.expected)
            self.assertEqual(container.state, {"state":"ready"})
            self.assertTrue(any(args[:2] == ("systemctl", "start") for args in container.executed))
            pushed = list(container.pushed)
            container.activated = False
            with patch.object(install, "cache_artifact") as cache:
                self.assertIsNone(install.install(container, release, selected))
            cache.assert_not_called()
            self.assertEqual(container.pushed, pushed)
            self.assertFalse(container.activated)
            self.assertEqual(container.layout_queries, 1)

    def test_readiness_failure_blocks_ssh_even_when_layout_is_supported(self):
        for response in ('{"absent":true}', '{"marker":{"layout":1}}'):
            with self.subTest(response=response), tempfile.TemporaryDirectory() as directory:
                args = SimpleNamespace(remote="local", project="proof", instance="workspace",
                                       state_dir=Path(directory) / "ssh")
                commands = []

                def run(command, **kwargs):
                    commands.append(command)
                    if "python3" in command and "-c" in command:
                        return subprocess.CompletedProcess(command, 0, stdout=response + "\n")
                    if any("collab-tools-ready" in arg for arg in command):
                        raise subprocess.CalledProcessError(75, command)
                    if "tee" in command or "ssh-keygen" in command:
                        self.fail("SSH key installation bypassed the failed readiness check")
                    return subprocess.CompletedProcess(command, 0, stdout="")

                with patch.object(ssh.shutil, "which", side_effect=lambda name: "/usr/bin/" + name), \
                     patch.object(ssh, "run", side_effect=run):
                    with self.assertRaises(subprocess.CalledProcessError):
                        ssh.setup(args)
                self.assertEqual(sum("python3" in command for command in commands), 1)
                self.assertEqual(sum(any("collab-tools-ready" in arg for arg in command)
                                     for command in commands), 1)

    def test_unknown_layout_blocks_ssh_before_key_install(self):
        with tempfile.TemporaryDirectory() as directory:
            args = SimpleNamespace(remote="local", project="proof", instance="workspace",
                                   state_dir=Path(directory) / "ssh")
            commands = []

            def run(command, **kwargs):
                commands.append(command)
                if "python3" in command and "-c" in command:
                    return subprocess.CompletedProcess(command, 0, stdout='{"marker":{"layout":999}}\n')
                # Permit harmless preliminary checks, but no authorization side effects.
                if "tee" in command or "ssh-keygen" in command:
                    self.fail("SSH admission proceeded despite an unsupported image layout")
                return subprocess.CompletedProcess(command, 0, stdout="")

            with patch.object(ssh.shutil, "which", side_effect=lambda name: "/usr/bin/" + name), \
                 patch.object(ssh, "run", side_effect=run):
                with self.assertRaisesRegex(ValueError, "newer host"):
                    ssh.setup(args)
            self.assertEqual(sum("python3" in command for command in commands), 1)


if __name__ == "__main__":
    unittest.main()
