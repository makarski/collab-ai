import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_mounts as mounts

SPEC = importlib.util.spec_from_file_location("mount_host", Path(__file__).resolve().parents[1] / "sandbox-host.py")
host = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(host)


class MountTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.source = self.root / "project"
        self.source.mkdir()
        self.manifest = self.root / "mounts.json"
        self.output = self.root / "mounts.auto.tfvars.json"

    def manifest_with(self, **changes):
        spec = {"source": str(self.source), "path": "/workspace/project", **changes}
        self.manifest.write_text(json.dumps({"project": spec}))
        return self.manifest

    def invoke(self, action):
        argv = ["sandbox-host.py", action, "--mounts-file", str(self.manifest),
                "--output", str(self.output), "--uid", "501", "--gid", "20"]
        with patch.object(host.sys, "argv", argv), contextlib.redirect_stdout(io.StringIO()):
            host.main()

    def test_readonly_default_and_explicit_write(self):
        spec = mounts.read_mounts(self.manifest_with())
        self.assertTrue(spec["project"]["readonly"])
        self.assertFalse(mounts.colima_mounts(spec)[0]["writable"])
        spec = mounts.read_mounts(self.manifest_with(readonly=False))
        self.assertTrue(mounts.colima_mounts(spec)[0]["writable"])
        self.assertEqual(mounts.mount_variables(spec, 501, 20)["mount_owner"], {"uid": 501, "gid": 20})

    def test_rejects_bad_sources_destinations_and_types(self):
        cases = [{"source": "relative"}, {"source": "/"}, {"source": str(self.root / "missing")},
                 {"source": str(self.manifest)}, {"source": str(self.source) + ",bad"},
                 {"path": "/etc"}, {"path": "/workspace/../etc"}, {"readonly": "false"}, {"extra": True}]
        for changes in cases:
            with self.subTest(changes=changes), self.assertRaises((ValueError, OSError)):
                mounts.read_mounts(self.manifest_with(**changes))

    def test_resolves_symlinks_before_allowlisting(self):
        alias = self.root / "alias"
        alias.symlink_to(self.source, target_is_directory=True)
        spec = mounts.read_mounts(self.manifest_with(source=str(alias)))
        self.assertEqual(spec["project"]["source"], str(self.source))

    def test_rejects_overlapping_sources_and_duplicate_targets(self):
        child = self.source / "child"
        child.mkdir()
        for target in ["/workspace/project", "/workspace/child"]:
            self.manifest_with()
            data = json.loads(self.manifest.read_text())
            data["child"] = {"source": str(child), "path": target}
            self.manifest.write_text(json.dumps(data))
            with self.assertRaises(ValueError):
                mounts.read_mounts(self.manifest)

    def test_linux_plan_and_apply_do_not_invoke_colima(self):
        self.manifest_with()
        with patch.object(host.platform, "system", return_value="Linux"), patch.object(host.subprocess, "run") as run:
            self.invoke("mounts-plan")
            self.assertFalse(self.output.exists())
            self.invoke("mounts-apply")
        run.assert_not_called()
        self.assertEqual(json.loads(self.output.read_text())["mount_owner"]["uid"], 501)

    def test_overlap_is_rejected_when_sibling_sorts_between_parent_and_child(self):
        with self.assertRaisesRegex(ValueError, "overlap"):
            mounts.validate_sources(["/project", "/project-other", "/project/child"])

    def test_empty_manifest_clears_mounts_without_owner_mapping(self):
        self.assertEqual(mounts.mount_variables({}, 0, 0), {"host_mounts": {}, "mount_owner": None})
        self.assertIsNone(mounts.colima_mounts({}))
        with self.assertRaisesRegex(ValueError, "non-root"):
            mounts.mount_variables({"project": {}}, 0, 20)

    def test_mac_changes_require_stop_and_preserve_other_settings(self):
        self.manifest_with(readonly=False)
        profile = self.root / ".colima/collab-ai"
        stopped = subprocess.CompletedProcess([], 0, '{"name":"collab-ai","status":"Stopped"}\n')
        running = subprocess.CompletedProcess([], 0, '{"name":"collab-ai","status":"Running"}\n')
        with patch.object(host.Path, "home", return_value=self.root), \
                patch.object(host.platform, "system", return_value="Darwin"), \
                patch.object(host.platform, "machine", return_value="arm64"), \
                patch.dict(host.os.environ, {}, clear=True):
            host.apply_profile(profile, host.desired_config())
            with patch.object(host.subprocess, "run", return_value=running), self.assertRaisesRegex(ValueError, "Stop"):
                self.invoke("mounts-apply")
            self.assertFalse(self.output.exists())
            with patch.object(host.subprocess, "run", return_value=stopped):
                self.invoke("mounts-apply")
            configured = host.profile_config(profile)
            self.assertTrue(configured["mounts"][0]["writable"])
            self.assertEqual({**configured, "mounts": None}, host.desired_config())
            with patch.object(host.subprocess, "run") as run:
                self.invoke("mounts-apply")
            run.assert_not_called()  # unchanged configuration works while running


if __name__ == "__main__":
    unittest.main()
