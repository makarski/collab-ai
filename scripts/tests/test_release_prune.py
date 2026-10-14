from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import release_prune as prune  # noqa: E402


def release(tag, created, **flags):
    return {"tagName": tag, "createdAt": created, "isDraft": False, "isPrerelease": False, **flags}


class PruneTests(unittest.TestCase):
    def test_only_older_automatic_builds_are_pruned(self):
        releases = [
            release("workspace-v0.0.0-build.150.1", "2026-10-14T11:26:00Z"),
            release("workspace-v0.0.0-build.184.1", "2026-10-14T15:16:00Z"),
            release("workspace-v0.0.0-build.147.1", "2026-10-14T09:23:00Z"),
            release("workspace-v0.0.0-build.176.1", "2026-10-14T14:30:00Z"),
            release("workspace-v1.2.0", "2026-10-01T00:00:00Z"),
            release("workspace-v0.0.0-build.190.1", "2026-10-14T16:00:00Z", isDraft=True),
            release("workspace-v0.0.0-build.1.1", "2026-09-01T00:00:00Z", isPrerelease=True),
        ]
        self.assertEqual(prune.prunable(releases, 2),
                         ["workspace-v0.0.0-build.150.1", "workspace-v0.0.0-build.147.1"])
        self.assertEqual(prune.prunable(releases, 10), [])

    def test_prune_deletes_each_victim_with_its_tag(self):
        calls = []

        def run(command, **kwargs):
            calls.append(command)
            if command[:3] == ["gh", "release", "list"]:
                return type("R", (), {"stdout": '[{"tagName": "workspace-v0.0.0-build.2.1", "createdAt": "b"},'
                                                '{"tagName": "workspace-v0.0.0-build.1.1", "createdAt": "a"}]'})()
            return type("R", (), {"stdout": ""})()
        self.assertEqual(prune.prune("o/r", 1, run), ["workspace-v0.0.0-build.1.1"])
        self.assertIn(["gh", "release", "delete", "workspace-v0.0.0-build.1.1", "--repo", "o/r", "--yes", "--cleanup-tag"], calls)


if __name__ == "__main__":
    unittest.main()
