"""The configured Claude server and enabled channel must name the same endpoint."""

import json
from pathlib import Path
import sys
import tempfile
import unittest

import test_agent_aliases as agent_aliases

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
import tool_store


class RuntimeNameAcceptance(unittest.TestCase):
    def test_claude_launch_enables_exactly_the_configured_pairtask_channel(self):
        configured = json.loads((ROOT / "infra/image/claude-mcp.json").read_text())["mcpServers"]
        self.assertEqual(set(configured), {"pairtask"})
        server = configured["pairtask"]
        self.assertIn("--claude-channel", server["args"])
        self.assertIn("--auto-listen", server["args"])
        output = agent_aliases.AgentAliasTests().shell(
            'function /usr/local/bin/claude() { printf "%s\\n" "$@"; }\n'
            'claude --resume "saved session with spaces"')
        channel = output[output.index("--dangerously-load-development-channels") + 1]
        self.assertEqual(channel, "server:pairtask")
        self.assertIn(channel.removeprefix("server:"), configured)
        self.assertEqual(output[-2:], ["--resume", "saved session with spaces"])
        self.assertIn("--strict-mcp-config", output)

    def test_tool_activation_keeps_the_renamed_server_without_a_legacy_duplicate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            share, etc = root / "share", root / "etc"
            share.mkdir()
            (share / "claude-mcp.base.json").write_bytes((ROOT / "infra/image/claude-mcp.json").read_bytes())
            (share / "codex-config.base.toml").write_text("")
            (share / "mcp-servers.json").write_text("{}")
            tool_store.write_configuration({}, share=share, etc=etc)
            generated = json.loads((etc / "collab-ai/claude-mcp.json").read_text())["mcpServers"]
            self.assertEqual(set(generated), {"pairtask"})
            self.assertEqual(generated, json.loads((share / "claude-mcp.base.json").read_text())["mcpServers"])


if __name__ == "__main__":
    unittest.main()
