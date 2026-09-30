# Interactive shells only; services and scripted CLI calls use native binaries.
case $- in
  *i*)
    alias dashboard='/usr/local/bin/collab dashboard'
    if [ "$(id -u)" = 1001 ]; then
      alias codex='/usr/local/bin/collab-codex --codex /usr/local/bin/codex --agent-id codex-1 --socket /tmp/collab-ai.sock --terminal -- --add-dir /home/agent/.local/share/rtk'
      alias claude='/usr/local/bin/claude --strict-mcp-config --mcp-config /etc/collab-ai/claude-mcp.json --dangerously-load-development-channels server:collab'
    fi
    ;;
esac
