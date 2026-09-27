"""Exercise pinned native clients against fake providers in the offline secured container."""

import http.server
import json
from pathlib import Path
import subprocess
import sys
import threading

from models import Model, claude_events, codex_events


def codex_command(directory, port):
    (directory / "environments.toml").write_text('''default = "dev"
include_local = false
[[environments]]
id = "dev"
program = "python3"
args = ["/opt/proof/relay.py", "client", "codex"]
''')
    (directory / "config.toml").write_text('''model = "gpt-5.4"
model_provider = "proof"
approval_policy = "never"
sandbox_mode = "danger-full-access"
[features]
shell_snapshot = false
hooks = false
plugins = false
apps = false
multi_agent = false
code_mode = false
code_mode_host = false
enable_request_compression = false
[model_providers.proof]
name = "Offline fixture"
base_url = "http://127.0.0.1:''' + str(port) + '''/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
''')
    return ["codex", "exec", "--skip-git-repo-check", "--json", "-C", "/workspace",
            "Execute the deterministic offline fixture."], {"CODEX_HOME": str(directory), "RUST_LOG": "error"}


def claude_command(directory, port):
    config = directory / "mcp.json"
    config.write_text(json.dumps({"mcpServers": {"dev": {
        "command": "python3", "args": ["/opt/proof/relay.py", "client", "mcp"]}}}))
    command = ["claude", "-p", "--restricted", "--tools", "", "--strict-mcp-config", "--mcp-config", str(config),
               "--permission-mode", "dontAsk", "--allowedTools", "mcp__dev__run_command", "--disable-slash-commands",
               "--setting-sources", "", "--settings", '{"disableAllHooks":true}', "--model", "claude-sonnet-4-6",
               "--output-format", "stream-json", "--verbose", "Execute the deterministic offline fixture."]
    environment = {"CLAUDE_CONFIG_DIR": str(directory / "config"), "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{port}",
                   "ANTHROPIC_API_KEY": "offline-fixture-not-a-credential", "DISABLE_AUTOUPDATER": "1",
                   "DISABLE_NON_ESSENTIAL_MODEL_CALLS": "1"}
    return command, environment


def verify(kind, result, observed):
    if result.returncode or len(observed) != 3:
        raise ValueError(f"{kind} fixture failed: {result.stdout}\n{result.stderr}")
    verify_files(kind, result)
    verify_local_rejection(kind, result)


def verify_files(kind, result):
    if Path("/var/lib/collab-proof/private").read_text() != "controller-only":
        raise ValueError("Native tool modified secured-container state")
    marker = "proof-native" if kind == "codex" else "proof-claude"
    if Path("/workspace", marker).exists() or "isolated" not in result.stdout:
        raise ValueError(f"Remote execution failed or ran locally: {result.stdout}\n{result.stderr}")


def verify_local_rejection(kind, result):
    if kind == "codex" and "unknown turn environment id `local`" not in result.stderr:
        raise ValueError("Codex did not explicitly reject the local environment")
    if kind == "claude" and "No such tool available: Bash" not in result.stdout:
        raise ValueError("Claude did not explicitly reject local Bash")


def main(kind):
    directory = Path(f"/var/lib/collab-native-proof-{kind}")
    directory.mkdir(mode=0o700)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Model)
    server.observed = []
    server.events = codex_events if kind == "codex" else claude_events
    threading.Thread(target=server.serve_forever, daemon=True).start()
    configure = codex_command if kind == "codex" else claude_command
    command, environment = configure(directory, server.server_port)
    environment.update({"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": str(directory)})
    try:
        result = subprocess.run(command, cwd="/workspace", env=environment, capture_output=True, text=True, timeout=60)
        (directory / "requests.json").write_text(json.dumps(server.observed))
        (directory / "output.log").write_text(result.stdout + "\nSTDERR\n" + result.stderr)
        verify(kind, result, server.observed)
        print(f"PASS: native {kind} executes in dev and rejects the tested local-execution bypass.", flush=True)
    finally:
        server.shutdown()


if __name__ == "__main__":
    main(sys.argv[1])
