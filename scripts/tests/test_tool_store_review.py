import concurrent.futures
import runpy
import subprocess
import threading
from unittest.mock import patch

from test_tool_store import ROOT, StoreHarness, tool_store


class StoreReviewTests(StoreHarness):
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
