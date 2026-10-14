import concurrent.futures
import os
import runpy
import subprocess
import threading
from unittest.mock import patch

from test_tool_store import ROOT, StoreHarness, tool_store


class StoreReviewTests(StoreHarness):
    def test_artifact_bytes_are_flushed_before_ready_commit(self):
        manifest = self.manifest(self.deliver("tool", b"payload", kind="binary", entry="tool"))
        flushed = []
        real_fsync = os.fsync
        real_write_state = tool_store.write_state

        def record_flush(fd):
            info = os.fstat(fd)
            flushed.append((info.st_dev, info.st_ino))
            real_fsync(fd)

        def check_ready(value, path):
            if value["state"] == "ready":
                info = self.store.resolve("tool").stat()
                self.assertIn((info.st_dev, info.st_ino), flushed,
                              "ready was committed without flushing the artifact contents")
            real_write_state(value, path)

        with patch.object(tool_store.os, "fsync", side_effect=record_flush), \
             patch.object(tool_store, "write_state", side_effect=check_ready):
            self.store.activate(manifest, self.incoming)

    def test_waiting_activator_rechecks_terminal_state_under_lock(self):
        first = self.manifest(self.deliver("tool", b"one", kind="binary", entry="tool"))
        second = self.manifest(self.deliver("other", b"two", kind="binary", entry="other"))
        admission = threading.Barrier(2)
        real_flock = tool_store.fcntl.flock

        def simultaneous_call(fd, operation):
            admission.wait(timeout=5)
            return real_flock(fd, operation)

        def activate(manifest):
            try:
                return self.store.activate(manifest, self.incoming)
            except tool_store.StoreError:
                return None

        with patch.object(tool_store.fcntl, "flock", side_effect=simultaneous_call):
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                futures = [pool.submit(activate, manifest) for manifest in (first, second)]
                results = [future.result(timeout=6) for future in futures]
        successful = [result for result in results if result is not None]
        self.assertEqual(len(successful), 1, "a waiting activator mutated a terminal ready root")
        self.assertEqual(self.store.status()["current"], successful[0])

    def test_bundle_tar_root_directory_is_accepted(self):
        source = self.incoming / "source"
        source.mkdir()
        (source / "tool").write_bytes(b"tool contents")
        archive = self.incoming / "bundle.tar.gz"
        subprocess.run(["tar", "-czf", str(archive), "-C", str(source), "."], check=True)
        manifest = self.manifest(self.deliver("tool", archive.read_bytes(), entry="tool"))
        self.store.activate(manifest, self.incoming)
        self.assertEqual(self.store.resolve("tool").read_bytes(), b"tool contents")

    def test_go_environment_points_to_toolchain_root(self):
        launcher = runpy.run_path(str(ROOT / "infra/image/collab-tool"))
        base = self.store.artifacts / ("a" * 64)
        entry = base / "go/bin/go"
        env = launcher["environment"]("go", entry, base)
        self.assertEqual(env["GOROOT"], str(base / "go"))
