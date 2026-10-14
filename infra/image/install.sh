#!/bin/bash
# Runs only inside the disposable, unprivileged Incus build container.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Prevent package post-install scripts from starting a network SSH listener.
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 755 /usr/sbin/policy-rc.d
apt-get -o APT::Update::Error-Mode=any -o Acquire::Retries=3 update
apt-get install -y --no-install-recommends ca-certificates curl python3 git gh \
    openssh-server sshfs ripgrep tmux less locales apparmor
# The rootful daemon never runs here, even once the Docker deb set is installed.
systemctl mask docker.service docker.socket containerd.service
# Go only compiles collab here; the toolchain is removed again below.
python3 /root/build/infra/image/install-build-go.py
export PATH="/usr/local/go/bin:$PATH" GOTOOLCHAIN=local CGO_ENABLED=0
cd /root/build
go mod download
go mod verify
go test ./... -timeout=60s
for spec in broker:broker codex:collab-codex mcp:collab-mcp collab:collab; do
    go build -trimpath -buildvcs=false -o "/usr/local/bin/${spec#*:}" "./cmd/${spec%%:*}"
done
go clean -cache -modcache -testcache
rm -rf /usr/local/go
gh --version
addgroup --gid 1001 agent
adduser --uid 1001 --gid 1001 --disabled-password --gecos '' agent
addgroup --gid 1002 broker
addgroup --gid 1003 collab-clients
adduser --uid 1002 --gid 1002 --disabled-password --gecos '' --no-create-home --home /nonexistent --shell /usr/sbin/nologin broker
usermod -aG collab-clients agent
# Keep subordinate IDs inside the workspace's 131072-ID Incus mapping.
sed -i '/^agent:/d' /etc/subuid /etc/subgid
printf 'agent:65536:65536\n' >>/etc/subuid
printf 'agent:65536:65536\n' >>/etc/subgid
install -d -o agent -g agent /workspace /var/lib/collab-ai
install -d /etc/collab-ai /etc/codex /etc/ssh/authorized_keys /usr/local/share/collab-ai
install -m 0644 infra/image/codex-config.toml /etc/codex/config.toml
cp infra/image/tools.lock.json /usr/local/share/collab-ai/
cp build-source.json /usr/local/share/collab-ai/
# The tool store (docs/sandbox-tools.md): this image carries no third-party
# tools; the rollout installs the selected ones into the store, and the tool
# names below launch from it.
printf 'store\n' >/usr/local/share/collab-ai/tools-mode
install -d -m 0755 /usr/local/lib/collab-ai /var/lib/collab-ai-tools
install -m 0644 infra/image/tool_lock.py infra/image/tool_store.py /usr/local/lib/collab-ai/
install -m 0755 infra/image/collab-tools-activate infra/image/collab-tools-ready infra/image/collab-tool /usr/local/bin/
for name in go gofmt codex claude node npm npx cs-mcp rtk starship; do
    ln -s collab-tool "/usr/local/bin/$name"
done
cp infra/image/collab-tools-setup.service infra/image/collab-tools-ready.service /etc/systemd/system/
bash infra/image/install-skill.sh docs/skills/collab-ai /usr/local/share/collab-ai
cp infra/image/claude-mcp.json /etc/collab-ai/claude-mcp.json
# The activator writes the agents' MCP configuration and the tool markers from
# the tool selection at rollout; these are its base files.
cp infra/image/claude-mcp.json /usr/local/share/collab-ai/claude-mcp.base.json
cp infra/image/codex-config.toml /usr/local/share/collab-ai/codex-config.base.toml
cp infra/image/mcp/servers.json /usr/local/share/collab-ai/mcp-servers.json
install -d /etc/collab-ai/tools
install -m 0755 infra/image/collab-playwright infra/image/collab-codescene-login /usr/local/bin/
install -m 0644 infra/image/collab-chromium.apparmor /etc/apparmor.d/collab-chromium
cp infra/image/sshd_config /etc/ssh/sshd_config.collab-ai
cp infra/image/collab-broker.service infra/image/collab-secured-setup.service /etc/systemd/system/
cp infra/image/collab-secured-broker.service infra/image/collab-dev-setup.service \
    infra/image/collab-dev-executor.socket infra/image/collab-dev-executor@.service \
    infra/image/collab-client-setup.service infra/image/collab-workspace-setup.service /etc/systemd/system/
cp infra/image/secured-codex.toml infra/image/secured-environments.toml /etc/collab-ai/
install -m 0755 infra/image/collab-executor-connect /usr/local/bin/collab-executor-connect
install -m 0755 infra/image/collab-supervised-codex /usr/local/bin/collab-supervised-codex
install -m 0755 infra/image/collab-broker-setup /usr/local/bin/collab-broker-setup
install -m 0755 infra/image/collab-workspace-setup /usr/local/bin/collab-workspace-setup
install -m 0755 infra/image/collab-rtk-setup /usr/local/bin/collab-rtk-setup
install -m 0644 infra/image/collab-rtk-setup.service /etc/systemd/system/
install -m 0755 infra/image/collab-docker-setup /usr/local/bin/collab-docker-setup
install -m 0644 infra/image/collab-docker-{session,setup}.service /etc/systemd/system/
install -d /etc/systemd/system/user@1001.service.d
install -m 0644 infra/image/collab-user-workspace.conf /etc/systemd/system/user@1001.service.d/
printf 'd /run/sshd 0755 root root -\n' >/etc/tmpfiles.d/collab-ssh.conf
printf 'DISABLE_UPDATES=1\nDISABLE_AUTOUPDATER=1\n' >>/etc/environment
printf 'export DISABLE_UPDATES=1 DISABLE_AUTOUPDATER=1\ncd /workspace\n' >/etc/profile.d/collab-ai.sh
install -m 0644 infra/image/collab-agent-aliases.sh /etc/profile.d/collab-agent-aliases.sh
# Login shells read profile.d; non-login Bash shells (including tmux) read bash.bashrc.
printf '\n. /etc/profile.d/collab-agent-aliases.sh\n' >>/etc/bash.bashrc
install -m 0644 infra/image/collab-starship.sh /etc/profile.d/collab-starship.sh
printf '\n. /etc/profile.d/collab-starship.sh\n' >>/etc/bash.bashrc
systemctl disable ssh.service ssh.socket || true
systemctl mask ssh.service ssh.socket
systemctl enable collab-broker.service collab-secured-setup.service
systemctl enable collab-secured-broker.service collab-dev-executor.socket
systemctl enable collab-client-setup.service
systemctl enable collab-workspace-setup.service
systemctl enable collab-tools-setup.service collab-tools-ready.service
systemctl enable collab-rtk-setup.service
systemctl enable collab-docker-session.service collab-docker-setup.service
dpkg-query -W >/usr/local/share/collab-ai/os-packages.txt

# Images must not share SSH host keys, machine identities, credentials or state.
rm -f /etc/ssh/ssh_host_* /var/lib/dbus/machine-id
truncate -s 0 /etc/machine-id
apt-get clean
rm -rf /var/lib/apt/lists/* /root/.cache /root/.npm /root/.codex /root/.claude /root/.ssh
# Documentation and foreign locales have no reader in a sandbox image.
rm -rf /usr/share/doc/* /usr/share/man/* /usr/share/info/*
find /usr/share/locale -mindepth 1 -maxdepth 1 ! -name 'en*' ! -name 'locale.alias' -exec rm -rf {} +
rm -f /root/.claude.json /root/.bash_history /usr/sbin/policy-rc.d
find /var/log -type f -exec truncate -s 0 {} +
rm -rf /root/build /root/source.tar /tmp/* /var/tmp/*
