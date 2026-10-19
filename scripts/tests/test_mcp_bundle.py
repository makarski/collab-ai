import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import tomllib
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))


def module(name, filename):
    loader = importlib.machinery.SourceFileLoader(name, str(ROOT / "infra/image" / filename))
    spec = importlib.util.spec_from_loader(name, loader)
    result = importlib.util.module_from_spec(spec)
    loader.exec_module(result)
    return result


import tool_store  # noqa: E402

login = module("codescene_login", "pairtask-codescene-login")


class MCPBundleTests(unittest.TestCase):
    def test_both_agents_get_same_servers_and_keep_existing_defaults(self):
        with tempfile.TemporaryDirectory() as temporary:
            etc, share = Path(temporary) / "etc", Path(temporary) / "share"
            share.mkdir()
            # The base files install.sh ships; the activator derives both configurations from them.
            for source, name in (("claude-mcp.json", "claude-mcp.base.json"), ("codex-config.toml", "codex-config.base.toml"),
                                 ("mcp/servers.json", "mcp-servers.json")):
                (share / name).write_bytes((ROOT / "infra/image" / source).read_bytes())
            tool_store.write_configuration({"cs-mcp": True, "playwright": True, "docker": True}, share, etc)
            claude = json.loads((etc / "pairtask/claude-mcp.json").read_text())["mcpServers"]
            codex = tomllib.loads((etc / "codex/config.toml").read_text())
            self.assertEqual(set(codex["mcp_servers"]), {"codescene", "playwright"})
            self.assertIn("pairtask", claude)
            for name, config in codex["mcp_servers"].items():
                self.assertEqual(claude[name], config)
            self.assertIn("/home/agent/.local/share/rtk", codex["sandbox_workspace_write"]["writable_roots"])

    def test_token_storage_preserves_settings_and_is_private(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "codehealth-mcp"
            directory.mkdir()
            path = directory / "config.json"
            path.write_text('{"account_id":"123","instance_id":"existing"}')
            login.save_token(directory, " test-only-token ")
            data = json.loads(path.read_text())
            self.assertEqual(data, {"account_id": "123", "instance_id": "existing", "access_token": "test-only-token"})
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(list(directory.iterdir()), [path])

    def test_token_write_failure_preserves_existing_credentials(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            path = directory / "config.json"
            path.write_text('{"access_token":"old-test-token"}')
            with patch.object(os, "replace", side_effect=OSError("write failed")):
                with self.assertRaises(OSError):
                    login.save_token(directory, "new-test-token")
            self.assertEqual(json.loads(path.read_text())["access_token"], "old-test-token")
            self.assertEqual(list(directory.iterdir()), [path])

    def test_token_input_cannot_follow_config_symlink(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            target = directory / "unrelated"
            target.write_text("unchanged")
            (directory / "config.json").symlink_to(target)
            with self.assertRaisesRegex(ValueError, "symlinked"):
                login.save_token(directory, "test-only-token")
            self.assertEqual(target.read_text(), "unchanged")
