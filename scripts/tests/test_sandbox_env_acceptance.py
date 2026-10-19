"""Environment migration must preserve presence and keep diagnostics off stdout."""

import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_env


class EnvironmentAcceptance(unittest.TestCase):
    def test_presence_precedence(self):
        cases = (
            ({}, ("", False, False)),
            ({"PAIRTASK_OPERATOR_HOME": "/canonical"}, ("/canonical", True, False)),
            ({"COLLAB_OPERATOR_HOME": "/legacy"}, ("/legacy", True, True)),
            ({"PAIRTASK_OPERATOR_HOME": "/canonical", "COLLAB_OPERATOR_HOME": "/legacy"}, ("/canonical", True, False)),
            ({"PAIRTASK_OPERATOR_HOME": "", "COLLAB_OPERATOR_HOME": "/legacy"}, ("", True, False)),
            ({"COLLAB_OPERATOR_HOME": ""}, ("", True, True)),
            ({"PAIRTASK_OPERATOR_HOME": " /spaces "}, (" /spaces ", True, False)),
        )
        for values, expected in cases:
            with self.subTest(values=values):
                self.assertEqual(sandbox_env.lookup("OPERATOR_HOME", environ=values), expected)

    def test_default_environment_is_read_when_called(self):
        with patch.dict(os.environ, {"PAIRTASK_OPERATOR_HOME": "/first"}, clear=True):
            self.assertEqual(sandbox_env.lookup("OPERATOR_HOME"), ("/first", True, False))
            os.environ["PAIRTASK_OPERATOR_HOME"] = "/second"
            self.assertEqual(sandbox_env.lookup("OPERATOR_HOME"), ("/second", True, False))

    def run_child(self, source, values):
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith(("COLLAB_", "PAIRTASK_"))}
        environment.update(values)
        return subprocess.run([sys.executable, "-c", source], cwd=ROOT / "scripts",
                              env=environment, capture_output=True, text=True)

    def test_warn_once_for_multiple_legacy_lookups_without_disclosing_values(self):
        source = ("import sandbox_env\n"
                  "for _ in range(20):\n"
                  "    sandbox_env.lookup('OPERATOR_HOME')\n"
                  "    sandbox_env.lookup('INCUS_PROJECT')\n"
                  "print('application output')\n")
        result = self.run_child(source, {"COLLAB_OPERATOR_HOME": "/private-secret-path",
                                        "COLLAB_INCUS_PROJECT": "private-project-name"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "application output\n")
        self.assertEqual(len(result.stderr.splitlines()), 1)
        self.assertIn("deprecat", result.stderr.lower())
        self.assertIn("PAIRTASK_", result.stderr)
        self.assertNotIn("private-secret-path", result.stderr)
        self.assertNotIn("private-project-name", result.stderr)

    def test_unused_legacy_variables_do_not_warn(self):
        result = self.run_child("import sandbox_env; sandbox_env.lookup('OPERATOR_HOME')",
                                {"PAIRTASK_OPERATOR_HOME": "", "COLLAB_OPERATOR_HOME": "/legacy"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr, "")

    def test_operator_registry_uses_canonical_home(self):
        result = self.run_child("import json, sandbox_operator_config as c; print(json.dumps(str(c.OPERATOR_HOME)))",
                                {"PAIRTASK_OPERATOR_HOME": "/canonical-state",
                                 "COLLAB_OPERATOR_HOME": "/legacy-state"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), "/canonical-state")
        self.assertEqual(result.stderr, "")

    def test_invalid_canonical_home_never_uses_legacy_or_current_directory(self):
        for value in ("", "relative/path"):
            with self.subTest(value=value):
                result = self.run_child("import sandbox_operator_config", {
                    "PAIRTASK_OPERATOR_HOME": value, "COLLAB_OPERATOR_HOME": "/valid-legacy-state"})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_operator_home_expansion_precedes_absolute_validation(self):
        result = self.run_child("import json, sandbox_operator_config as c; print(json.dumps(str(c.OPERATOR_HOME)))",
                                {"PAIRTASK_OPERATOR_HOME": "~/pairtask-acceptance-state"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), str(Path.home() / "pairtask-acceptance-state"))
        self.assertEqual(result.stderr, "")


if __name__ == "__main__":
    unittest.main()
