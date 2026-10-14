import concurrent.futures
import hashlib
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_tools as tools


class ToolCacheTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.cache = Path(self.temporary.name)
        self.good = b"verified tool archive"
        self.item = {"name": "example", "version": "1.0.0",
                     "url": "https://example.invalid/tool.tar.gz",
                     "sha256": hashlib.sha256(self.good).hexdigest()}

    def download(self, url, destination):
        self.assertEqual(url, self.item["url"])
        with destination.open("xb") as stream:
            stream.write(self.good)

    def test_cache_reuses_verified_hit_and_repairs_corruption(self):
        path = tools.cache_artifact(self.item, self.cache, self.download)
        with patch.object(tools, "fetch", side_effect=AssertionError("unexpected download")) as fetch:
            self.assertEqual(tools.cache_artifact(self.item, self.cache, fetch), path)
        path.write_bytes(b"corrupt")
        self.assertEqual(tools.cache_artifact(self.item, self.cache, self.download).read_bytes(), self.good)
        self.assertEqual(list(path.parent.iterdir()), [path])

    def test_failed_downloads_publish_nothing_and_remove_staging(self):
        for failure in (False, True):
            with self.subTest(transfer_error=failure):
                def obtain(url, destination):
                    destination.write_bytes(b"incomplete")
                    if failure:
                        raise OSError("transfer interrupted")
                with self.assertRaises(OSError if failure else ValueError):
                    tools.cache_artifact(self.item, self.cache, obtain)
                self.assertEqual(list((self.cache / self.item["sha256"]).iterdir()), [])

    def test_concurrent_bad_download_cannot_replace_verified_staging(self):
        checked = threading.Event()
        bad_written = threading.Event()
        published = threading.Event()
        real_hash = tools.sha256
        caller = threading.local()

        def wait(event):
            self.assertTrue(event.wait(5), "concurrent download did not reach barrier")

        def hash_then_pause(path):
            digest = real_hash(path)
            if getattr(caller, "good", False):
                checked.set()
                wait(bad_written)
            return digest

        def good_call():
            caller.good = True
            try:
                return tools.cache_artifact(self.item, self.cache, self.download).read_bytes()
            finally:
                published.set()

        def bad_download(url, destination):
            destination.write_bytes(b"unverified replacement")
            bad_written.set()
            wait(published)

        with patch.object(tools, "sha256", side_effect=hash_then_pause):
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                good_result = pool.submit(good_call)
                wait(checked)
                bad_result = pool.submit(tools.cache_artifact, self.item, self.cache, bad_download)
                self.assertEqual(good_result.result(timeout=6), self.good)
                with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
                    bad_result.result(timeout=6)
        directory = self.cache / self.item["sha256"]
        self.assertEqual([p.name for p in directory.iterdir()], ["tool.tar.gz"])
        self.assertEqual((directory / "tool.tar.gz").read_bytes(), self.good)
