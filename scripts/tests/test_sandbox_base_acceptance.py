"""Independent acceptance tests for durable, fingerprint-pinned base images."""

from concurrent.futures import ThreadPoolExecutor
import copy
import hashlib
from io import BytesIO
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch
from urllib.error import HTTPError, URLError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_base_image as base


META = b"pinned incus metadata\n"
ROOTFS = b"pinned root filesystem\n"
FP = hashlib.sha256(META + ROOTFS).hexdigest()
FILES = {"incus.tar.xz": META, "rootfs.squashfs": ROOTFS}


def release():
    prefix = f"https://github.com/owner/repo/releases/download/base-{FP}/"
    return {"tag_name": "base-" + FP, "draft": False, "assets": [
        {"name": name, "browser_download_url": prefix + name,
         "digest": "sha256:" + hashlib.sha256(content).hexdigest()}
        for name, content in FILES.items()
    ]}


def download(url, destination):
    Path(destination).write_bytes(FILES[url.rsplit("/", 1)[-1]])


def populate(cache):
    directory = cache / FP
    directory.mkdir()
    for name, content in FILES.items():
        (directory / name).write_bytes(content)
    return directory


def upstream_fixture():
    products = {}
    for arch, pin in (("arm64", "a" * 64), ("amd64", "b" * 64)):
        versions = {}
        for serial, fp in (("20261018_05:25", pin), ("20261016_07:42", "c" * 64)):
            versions[serial] = {"items": {
                "incus.tar.xz": {"ftype": "incus.tar.xz", "combined_squashfs_sha256": fp,
                                 "path": f"images/ubuntu/noble/{arch}/default/{serial}/incus.tar.xz",
                                 "sha256": hashlib.sha256(META).hexdigest()},
                "root.squashfs": {"ftype": "squashfs",
                                  "path": f"images/ubuntu/noble/{arch}/default/{serial}/rootfs.squashfs",
                                  "sha256": hashlib.sha256(ROOTFS).hexdigest()}}}
        products[f"ubuntu:noble:{arch}:default"] = {
            "arch": arch, "os": "Ubuntu", "release": "noble", "variant": "default", "versions": versions}
    return {"products": products}


class BaseCacheAcceptanceTests(unittest.TestCase):
    def test_valid_cache_is_reused_without_network(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "fetch") as fetch:
            cache = Path(tmp)
            expected = populate(cache)
            self.assertEqual(base.cached_pair(cache, FP, None), expected)
            fetch.assert_not_called()

    def test_cache_hit_rehashes_both_files_each_time(self):
        for name in FILES:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                cache = Path(tmp)
                directory = populate(cache)
                self.assertEqual(base.cached_pair(cache, FP, None), directory)
                (directory / name).write_bytes(b"corrupt")
                with self.assertRaises(base.IntegrityError):
                    base.cached_pair(cache, FP, None)

    def test_cache_miss_without_mirror_is_explicit_absence(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertIsNone(base.cached_pair(Path(tmp), FP, None))

    def test_pair_is_verified_in_metadata_then_rootfs_order(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "fetch", side_effect=download):
            cache = Path(tmp)
            result = base.cached_pair(cache, FP, release())
            self.assertEqual(result, cache / FP)
            for name, content in FILES.items():
                self.assertEqual((result / name).read_bytes(), content)
            self.assertEqual(list(cache.iterdir()), [result])

    def test_mismatched_download_never_publishes_cache(self):
        def corrupt(url, destination):
            Path(destination).write_bytes(b"different image")
        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "fetch", side_effect=corrupt):
            cache = Path(tmp)
            with self.assertRaises(base.IntegrityError):
                base.cached_pair(cache, FP, release())
            self.assertFalse((cache / FP).exists())
            self.assertEqual(list(cache.iterdir()), [])

    def test_interrupted_second_download_leaves_no_partial_pair(self):
        def interrupted(url, destination):
            if url.endswith("rootfs.squashfs"):
                raise OSError("connection interrupted")
            download(url, destination)
        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "fetch", side_effect=interrupted):
            cache = Path(tmp)
            with self.assertRaises((OSError, base.Unavailable)):
                base.cached_pair(cache, FP, release())
            self.assertEqual(list(cache.iterdir()), [])

    def test_concurrent_downloads_have_private_staging_and_one_verified_winner(self):
        rendezvous = threading.Barrier(2)
        destinations = []
        guard = threading.Lock()

        def concurrent_download(url, destination):
            destination = Path(destination)
            with guard:
                destinations.append(destination)
            if url.endswith("incus.tar.xz"):
                rendezvous.wait(timeout=5)
            download(url, destination)

        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "fetch", side_effect=concurrent_download):
            cache = Path(tmp)
            with ThreadPoolExecutor(max_workers=2) as pool:
                pending = [pool.submit(base.cached_pair, cache, FP, release()) for _ in range(2)]
                results = [future.result(timeout=10) for future in pending]
            self.assertEqual(results, [cache / FP, cache / FP])
            self.assertEqual(len(set(destinations)), 4)
            self.assertEqual(len({p.parent for p in destinations}), 2)
            self.assertEqual(list(cache.iterdir()), [cache / FP])
            self.assertEqual(base.cached_pair(cache, FP, None), cache / FP)


class BaseResolverAcceptanceTests(unittest.TestCase):
    def test_existing_project_image_avoids_host_cache_and_network(self):
        with patch.object(base, "in_store", return_value=True) as store, \
                patch.object(base, "cached_pair") as cache, patch.object(base, "mirror_release") as mirror:
            result = base.ensure_base("remote", "owned-project", FP, "owner/repo", Path("unused"))
        self.assertEqual((result.ref, result.source), ("remote:" + FP, "store"))
        store.assert_called_once_with("remote", "owned-project", FP)
        cache.assert_not_called()
        mirror.assert_not_called()

    def test_verified_host_cache_works_offline_and_import_is_project_scoped(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache = Path(tmp)
            directory = populate(cache)
            with patch.object(base, "in_store", side_effect=[False, True]), \
                    patch.object(base, "mirror_release", side_effect=AssertionError("offline cache must not use network")), \
                    patch.object(base, "run") as run:
                result = base.ensure_base("remote", "owned-project", FP, "owner/repo", cache)
            self.assertEqual((result.ref, result.source), ("remote:" + FP, "cache"))
            command = [str(value) for value in run.call_args.args[0]]
            self.assertEqual(command, ["incus", "--project", "owned-project", "image", "import",
                                       str(directory / "incus.tar.xz"), str(directory / "rootfs.squashfs"), "remote:"])

    def test_only_confirmed_absence_selects_upstream_exact_fingerprint(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "in_store", return_value=False), \
                patch.object(base, "mirror_release", return_value=None), patch.object(base, "run") as run:
            result = base.ensure_base("remote", "owned-project", FP, "owner/repo", Path(tmp))
        self.assertEqual((result.ref, result.source), ("images:" + FP, "upstream"))
        run.assert_not_called()

    def test_mirror_lookup_failure_does_not_fall_back(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(base, "in_store", return_value=False), \
                patch.object(base, "mirror_release", side_effect=base.Unavailable("HTTP 503")), \
                patch.object(base, "run") as run:
            with self.assertRaises(base.Unavailable):
                base.ensure_base("remote", "owned-project", FP, "owner/repo", Path(tmp))
        run.assert_not_called()

    def test_import_failure_does_not_fall_back(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache = Path(tmp)
            populate(cache)
            with patch.object(base, "in_store", return_value=False), \
                    patch.object(base, "run", side_effect=subprocess.CalledProcessError(1, ["incus"])):
                with self.assertRaises((subprocess.CalledProcessError, base.Unavailable)):
                    base.ensure_base("remote", "owned-project", FP, "owner/repo", cache)

    def test_successful_import_must_leave_exact_fingerprint_in_project(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache = Path(tmp)
            populate(cache)
            with patch.object(base, "in_store", return_value=False), patch.object(base, "run"):
                with self.assertRaises(base.IntegrityError):
                    base.ensure_base("remote", "owned-project", FP, "owner/repo", cache)


class BaseLookupAcceptanceTests(unittest.TestCase):
    def test_store_queries_only_target_project_and_requires_full_fingerprint(self):
        for entries, expected in (([], False), ([{"fingerprint": FP[:12]}], False),
                                  ([{"fingerprint": FP}], True)):
            with self.subTest(entries=entries), patch.object(base, "run", return_value=
                    subprocess.CompletedProcess([], 0, json.dumps(entries))) as run:
                self.assertEqual(base.in_store("remote", "owned-project", FP), expected)
                self.assertEqual(run.call_args.args[0], ["incus", "--project", "owned-project",
                                                       "image", "list", "remote:", "--format=json"])

    def test_store_query_errors_are_never_image_absence(self):
        for stderr in ("not found", "Project not found", "connection refused"):
            with self.subTest(stderr=stderr), patch.object(base, "run", side_effect=
                    subprocess.CalledProcessError(1, ["incus"], stderr=stderr)):
                with self.assertRaises(base.Unavailable):
                    base.in_store("remote", "owned-project", FP)

    def test_mirror_only_404_is_absence(self):
        for status in (401, 403, 429, 500, 503):
            with self.subTest(status=status), patch.object(base, "urlopen", side_effect=
                    HTTPError("https://api.github.com/test", status, "failure", {}, None)):
                with self.assertRaises(base.Unavailable):
                    base.mirror_release("owner/repo", FP)
        with patch.object(base, "urlopen", side_effect=HTTPError("url", 404, "missing", {}, None)):
            self.assertIsNone(base.mirror_release("owner/repo", FP))
        with patch.object(base, "urlopen", side_effect=URLError("offline")):
            with self.assertRaises(base.Unavailable):
                base.mirror_release("owner/repo", FP)

    def test_authenticated_visible_draft_is_not_consumable(self):
        draft = {"tag_name": "base-" + FP, "draft": True, "assets": []}
        with patch.object(base, "urlopen", return_value=BytesIO(json.dumps(draft).encode())):
            self.assertIsNone(base.mirror_release("owner/repo", FP))

    def test_published_mirror_requires_exact_tag_and_complete_unique_asset_pair(self):
        good = release()
        invalid = [dict(good, tag_name="base-" + "0" * 64),
                   dict(good, assets=good["assets"][:1]),
                   dict(good, assets=[good["assets"][0], good["assets"][0]]),
                   dict(good, assets=good["assets"] + [{"name": "unexpected", "browser_download_url": "https://example.invalid/extra"}])]
        for data in invalid:
            with self.subTest(data=data), patch.object(base, "urlopen", return_value=BytesIO(json.dumps(data).encode())):
                with self.assertRaises(base.IntegrityError):
                    base.mirror_release("owner/repo", FP)

    def test_published_mirror_uses_immutable_tag_lookup(self):
        with patch.object(base, "urlopen", return_value=BytesIO(json.dumps(release()).encode())) as request:
            self.assertEqual(base.mirror_release("owner/repo", FP), release())
        argument = request.call_args.args[0]
        url = argument.full_url if hasattr(argument, "full_url") else argument
        self.assertEqual(url, f"https://api.github.com/repos/owner/repo/releases/tags/base-{FP}")


class BaseRefreshAcceptanceTests(unittest.TestCase):
    def test_refresh_selects_newest_supported_product_without_changing_other_lock_fields(self):
        import sandbox_base_refresh as refresh
        index = upstream_fixture()
        unrelated = copy.deepcopy(index["products"]["ubuntu:noble:arm64:default"])
        unrelated["versions"] = {"20990101_00:00": {"items": {
            "incus.tar.xz": {"combined_squashfs_sha256": "f" * 64}}}}
        index["products"]["ubuntu:noble:arm64:cloud"] = unrelated
        lock = {"schema_version": 1, "architectures": {
            "aarch64": {"base_image": "1" * 64, "keep": "arm field"},
            "x86_64": {"base_image": "2" * 64}}, "artifacts": {"unrelated": [1, 2, 3]}}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tools.lock.json"
            path.write_text(json.dumps(lock, indent=2) + "\n")
            refresh.refresh(path, index)
            expected = copy.deepcopy(lock)
            expected["architectures"]["aarch64"]["base_image"] = "a" * 64
            expected["architectures"]["x86_64"]["base_image"] = "b" * 64
            self.assertEqual(json.loads(path.read_text()), expected)
            self.assertTrue(path.read_text().endswith("\n"))
            self.assertEqual(list(Path(tmp).iterdir()), [path])

    def test_missing_second_architecture_never_partially_updates_lock(self):
        import sandbox_base_refresh as refresh
        index = upstream_fixture()
        del index["products"]["ubuntu:noble:amd64:default"]
        original = '{"architectures":{"aarch64":{"base_image":"' + "1" * 64 + \
                   '"},"x86_64":{"base_image":"' + "2" * 64 + '"}}}\n'
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "tools.lock.json"
            path.write_text(original)
            with self.assertRaises(ValueError):
                refresh.refresh(path, index)
            self.assertEqual(path.read_text(), original)
            self.assertEqual(list(Path(tmp).iterdir()), [path])


if __name__ == "__main__":
    unittest.main()
