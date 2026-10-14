import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_bundles as bundles


class FakeBuilder:
    """Records container commands; `pull` writes a known file so digests are checkable."""

    def __init__(self):
        self.commands = []
        self.pushed = []

    def execute(self, *args, **kwargs):
        self.commands.append(args)

    def output(self, *args):
        self.commands.append(args)
        if args[0] == "ls":
            return "libnss3_2%3a3.98-1build1_arm64.deb\nfonts-liberation_1%3a2.1.5-3_all.deb\n"
        if args[0] == "cat":
            return json.dumps({"chromium": "browsers/chromium-1247/chrome-linux/chrome",
                               "playwright_mcp": "0.0.83", "playwright": "1.64.0"})
        return "arm64\n"

    def push(self, source, destination):
        self.pushed.append((Path(source).name, destination))

    def pull(self, source, destination):
        Path(destination).write_bytes(b"bundle:" + source.encode())


class BundleTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name)

    def test_inventory_parses_epochs_and_rejects_unexpected_files(self):
        inventory = bundles.deb_inventory("libnss3_2%3a3.98-1build1_arm64.deb fonts-liberation_1%3a2.1.5-3_all.deb")
        self.assertEqual(inventory, {"libnss3": "2:3.98-1build1", "fonts-liberation": "1:2.1.5-3"})
        with self.assertRaisesRegex(ValueError, "Unexpected file"):
            bundles.deb_inventory("lock")

    def test_manifest_records_each_bundle_digest_and_contents(self):
        builder = FakeBuilder()
        manifest = bundles.build_bundles(builder, "arm64", self.output)
        self.assertEqual(set(manifest["bundles"]), set(bundles.BUNDLE_KINDS))
        for kind, details in manifest["bundles"].items():
            path = self.output / details["file"]
            self.assertEqual(details["file"], f"collab-ai-bundle-{kind}-linux-arm64.tar.gz")
            self.assertEqual(details["sha256"], hashlib.sha256(path.read_bytes()).hexdigest())
        self.assertEqual(manifest["bundles"]["chromium-deps"]["packages"]["libnss3"], "2:3.98-1build1")
        self.assertEqual(manifest["bundles"]["playwright"]["playwright_mcp"], "0.0.83")
        written = json.loads((self.output / "collab-ai-bundles-linux-arm64.json").read_text())
        self.assertEqual(written, manifest)

    def test_deb_downloads_are_pinned_without_recommends_and_tars_are_reproducible(self):
        builder = FakeBuilder()
        bundles.build_bundles(builder, "arm64", self.output, kinds=("docker",))
        installs = [c for c in builder.commands if c[:1] == ("apt-get",) and "--download-only" in c]
        self.assertEqual(len(installs), 1)
        self.assertIn("--no-install-recommends", installs[0])
        self.assertTrue(any(p.startswith("docker-ce=") for p in installs[0]))
        tars = [c for c in builder.commands if c[:1] == ("tar",)]
        for flag in bundles.TAR_FLAGS:
            self.assertIn(flag, tars[0])

    def test_playwright_bundle_is_built_from_the_pinned_sources_only(self):
        builder = FakeBuilder()
        bundles.build_bundles(builder, "arm64", self.output, kinds=("playwright",))
        self.assertEqual([name for name, _ in builder.pushed],
                         ["tool_download.py", "tool_lock.py", "build-playwright-bundle.py",
                          "package.json", "package-lock.json", "tools.lock.json"])
        self.assertEqual(builder.pushed[-1][1], "/root/bundle-src/tools.lock.json")

    def test_a_custom_lock_file_governs_the_bundle_build(self):
        builder = FakeBuilder()
        custom = self.output / "custom-lock.json"
        custom.write_text("{}")
        bundles.BundleBuild(builder, "arm64", self.output, custom).build(("playwright",))
        self.assertEqual(builder.pushed[-1], ("custom-lock.json", "/root/bundle-src/tools.lock.json"))
        self.assertFalse(any("--with-deps" in c for c in builder.commands))


if __name__ == "__main__":
    unittest.main()
