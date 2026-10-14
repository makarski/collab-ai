import subprocess
from contextlib import nullcontext
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from test_sandbox_tools_install import FakeContainer, LOCK, install
import sandbox_rollout as rollout


class InstallAdmissionReviewTests(unittest.TestCase):
    def test_install_refuses_root_with_agent_key_but_missing_state(self):
        container = FakeContainer("workspace", key=True)
        selected = install.selection([])
        _, manifest = install.build_manifest(container, install.Release(LOCK, "aarch64"), selected)
        container.expected = install.generation_id(manifest)
        with patch.object(install, "cache_artifact") as cache:
            with self.assertRaises(ValueError):
                install.install(container, install.Release(LOCK, "aarch64"), selected)
        cache.assert_not_called()
        self.assertEqual(container.pushed, [])
        self.assertFalse(container.activated)

    def test_key_probe_errors_do_not_prove_never_released(self):
        container = FakeContainer("workspace")
        for code in (1, 2):
            with self.subTest(exit_code=code), patch.object(container, "run", return_value=
                    subprocess.CompletedProcess([], code, stdout="", stderr="Error: connection refused")):
                with self.assertRaises((ValueError, subprocess.SubprocessError)):
                    install.never_released(container, None)

    def test_transport_error_does_not_identify_a_legacy_image(self):
        container = FakeContainer("workspace")
        failure = subprocess.CompletedProcess([], 1, stdout="", stderr="Error: connection refused")
        with patch.object(container, "run", return_value=failure):
            with self.assertRaises((ValueError, subprocess.SubprocessError)):
                install.has_tool_store(container)

    def test_unknown_activation_state_is_not_safe_for_retry(self):
        container = FakeContainer("workspace")
        self.assertFalse(install.never_released(container, {"state": "unknown"}))

    def test_install_itself_rejects_unknown_state_before_mutation(self):
        container = FakeContainer("workspace", state={"state": "unknown"})
        selected = install.selection([])
        _, manifest = install.build_manifest(container, install.Release(LOCK, "aarch64"), selected)
        container.expected = install.generation_id(manifest)
        with patch.object(install, "cache_artifact") as cache:
            with self.assertRaises(ValueError):
                install.install(container, install.Release(LOCK, "aarch64"), selected)
        cache.assert_not_called()
        self.assertFalse(container.activated)

    def test_retry_completes_peer_after_first_container_is_ready(self):
        selected = install.selection([])
        containers = {name: FakeContainer(name) for name in ("workspace", "secured")}
        for container in containers.values():
            _, manifest = install.build_manifest(container, install.Release(LOCK, "aarch64"), selected)
            container.expected = install.generation_id(manifest)
        workspace = containers["workspace"]
        workspace.state, workspace.current = {"state": "ready"}, workspace.expected
        args = SimpleNamespace(remote="local", project="proof", image_dir=None, tools=[])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cached = root / "archive"
            cached.write_bytes(b"fixture")
            with patch.object(rollout, "deployment_lock", return_value=nullcontext()), \
                 patch.object(rollout, "require_recovered"), \
                 patch.object(rollout, "image_selection", return_value=(None, None, root / "image.tfvars.json")), \
                 patch.object(rollout, "instances", return_value={name: "Running" for name in containers}), \
                 patch.object(rollout, "Container", side_effect=lambda remote, project, name: containers[name]), \
                 patch.object(rollout, "release_lock_from", return_value=LOCK), \
                 patch.object(install, "cache_artifact", return_value=cached), \
                 patch.object(rollout, "configure_ssh") as ssh:
                rollout.tools_retry(args, root, {"architectures": ["aarch64"]})
            ssh.assert_called_once()
        self.assertEqual(workspace.pushed, [])
        self.assertFalse(workspace.activated)
        self.assertTrue(containers["secured"].activated)
