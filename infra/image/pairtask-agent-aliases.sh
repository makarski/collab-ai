# Interactive shells only; services and scripted CLI calls use native binaries.
case $- in
  *i*)
    alias dashboard='/usr/local/bin/pairtask dashboard'
    if [ "$(id -u)" = 1001 ]; then
      alias codex='/usr/local/lib/pairtask/pairtask-codex --codex /usr/local/bin/codex --agent-id codex-1 --socket /tmp/collab-ai.sock --terminal --'
      alias claude='/usr/local/bin/claude --strict-mcp-config --mcp-config /etc/pairtask/claude-mcp.json --dangerously-load-development-channels server:pairtask'
    fi
    ;;
esac
