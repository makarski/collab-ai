"""Colima may save commented YAML after the bootstrap wrote minimal JSON."""

import json
from pathlib import Path
import platform
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_colima_config as config


EXPANDED_YAML = """# Colima-generated configuration
cpu: 4
memory: 8
disk: 40
arch: aarch64
runtime: incus
vmType: vz
mountType: virtiofs
mounts: null
sshConfig: false
forwardAgent: false
autoActivate: false
hostname: colima-collab-ai
network:
  address: false
  mode: ""
  dns: null
  dnsHosts: {}
  hostAddresses: false
kubernetes:
  enabled: false
  version: v1.35.0+k3s1
  k3sArgs:
    - --disable=traefik
  port: 0
provision: null
env: {}
docker: {}
rootDisk: 20
"""


class ColimaConfigTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.path = Path(temp.name) / "colima.yaml"
        source = Path(__file__).resolve().parents[2] / "infra/incus/colima.json"
        self.expected = {**json.loads(source.read_text()), "arch": "aarch64"}

    def test_json_needs_no_external_parser(self):
        self.path.write_text(json.dumps(self.expected))
        with patch.object(config.subprocess, "run") as run:
            config.check_settings(self.path, self.expected)
        run.assert_not_called()

    def test_expanded_defaults_are_accepted_without_rewriting(self):
        settings = {**config.DEFAULTS, **self.expected,
                    "network": {**config.DEFAULTS["network"], **self.expected["network"]}}
        contents = json.dumps(settings)
        self.path.write_text(contents)
        config.check_settings(self.path, self.expected)
        self.assertEqual(self.path.read_text(), contents)

    def test_rejects_missing_changed_and_extra_settings(self):
        changes = [{"forwardAgent": True}, {"mounts": [{"location": "~", "writable": True}]},
                   {"provision": [{"mode": "system", "script": "echo unexpected"}]},
                   {"network": {"address": False, "hostAddresses": True}},
                   {"network": {}}, {"env": {"UNEXPECTED": "value"}},
                   {"cpu": True}, {"unknownSetting": False},
                   {"kubernetes": {"enabled": True}}]
        for change in changes:
            with self.subTest(change=change):
                self.path.write_text(json.dumps({**self.expected, **change}))
                with self.assertRaisesRegex(ValueError, "drifted"):
                    config.check_settings(self.path, self.expected)
        self.path.write_text("{}")
        with self.assertRaisesRegex(ValueError, "drifted"):
            config.check_settings(self.path, self.expected)

    def test_parser_failure_names_the_file(self):
        self.path.write_text("cpu: [")
        with patch.object(config.subprocess, "run", side_effect=FileNotFoundError), \
                self.assertRaisesRegex(ValueError, str(self.path)):
            config.read_settings(self.path)

    @unittest.skipUnless(platform.system() == "Darwin", "Uses macOS system Ruby")
    def test_real_colima_yaml_and_mount_lists(self):
        self.path.write_text(EXPANDED_YAML)
        config.check_settings(self.path, self.expected)
        self.assertEqual(self.path.read_text(), EXPANDED_YAML)
        mounts = [{"location": "/Users/operator/workspace/project", "writable": False}]
        self.path.write_text(EXPANDED_YAML.replace("mounts: null", "mounts:\n  - location: /Users/operator/workspace/project\n    writable: false"))
        config.check_settings(self.path, {**self.expected, "mounts": mounts})
        self.path.write_text(self.path.read_text().replace("writable: false", "writable: true"))
        with self.assertRaisesRegex(ValueError, "mounts"):
            config.check_settings(self.path, {**self.expected, "mounts": mounts})

    @unittest.skipUnless(platform.system() == "Darwin", "Uses macOS system Ruby")
    def test_rejects_invalid_yaml_and_non_mapping_documents(self):
        for contents in ("cpu: [", "", "- cpu", "!ruby/object:Object {}", "a: &a {}\nb: *a"):
            with self.subTest(contents=contents), self.assertRaisesRegex(ValueError, "Colima"):
                self.path.write_text(contents)
                config.read_settings(self.path)
