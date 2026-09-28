import runpy
from pathlib import Path
import unittest


COMMAND = runpy.run_path(str(Path(__file__).resolve().parents[2] / "infra/image/collab-supervised-codex"))["command"]


class SupervisedCommandTests(unittest.TestCase):
    def test_fixed_service_boundary(self):
        args = COMMAND("task-1")
        self.assertIn("--property=KillMode=control-group", args)
        self.assertIn("--property=TasksMax=128", args)
        self.assertIn("--property=Restart=no", args)
        self.assertIn("--property=TimeoutStopSec=3s", args)
        self.assertIn("--property=SendSIGKILL=yes", args)
        self.assertIn("--restricted-operator", args)
        self.assertNotIn("--scope", args)
        self.assertEqual(args[args.index("--budget") + 1], "task-1")

    def test_budget_cannot_inject_arguments_or_unit_paths(self):
        for name in ("", "../other", "--scope", "x;sh", "with space", "x\nEnvironment=bad", "a" * 65):
            with self.subTest(name=name), self.assertRaises(ValueError):
                COMMAND(name)
