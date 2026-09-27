#!/bin/bash
# Runs only inside the disposable, unprivileged Incus build container.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Prevent package post-install scripts from starting a network SSH listener.
printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
chmod 755 /usr/sbin/policy-rc.d
apt-get -o APT::Update::Error-Mode=any -o Acquire::Retries=3 update
apt-get install -y --no-install-recommends ca-certificates curl python3 git \
    openssh-server ripgrep tmux less locales
python3 /root/build/infra/image/install-tools.py
export PATH="/usr/local/go/bin:$PATH" GOTOOLCHAIN=local CGO_ENABLED=0
cd /root/build
go mod download
go mod verify
go test ./... -timeout=60s
for spec in broker:broker codex:collab-codex mcp:collab-mcp collab:collab; do
    go build -trimpath -buildvcs=false -o "/usr/local/bin/${spec#*:}" "./cmd/${spec%%:*}"
done

# No agent process, login or model request runs during the build.
codex --version
codex app-server --help >/dev/null
claude --version
addgroup --gid 1001 agent
adduser --uid 1001 --gid 1001 --disabled-password --gecos '' agent
install -d -o agent -g agent /workspace /var/lib/collab-ai
install -d /etc/collab-ai /etc/ssh/authorized_keys /usr/local/share/collab-ai
cp infra/image/tools.lock.json /usr/local/share/collab-ai/
cp build-source.json /usr/local/share/collab-ai/
cp docs/skills/collab-ai/SKILL.md /usr/local/share/collab-ai/SKILL.md
cp infra/image/claude-mcp.json /etc/collab-ai/claude-mcp.json
cp infra/image/sshd_config /etc/ssh/sshd_config.collab-ai
cp infra/image/collab-broker.service /etc/systemd/system/
printf 'd /run/sshd 0755 root root -\n' >/etc/tmpfiles.d/collab-ssh.conf
printf 'DISABLE_UPDATES=1\nDISABLE_AUTOUPDATER=1\n' >>/etc/environment
printf 'export DISABLE_UPDATES=1 DISABLE_AUTOUPDATER=1\ncd /workspace\n' >/etc/profile.d/collab-ai.sh
systemctl disable ssh.service ssh.socket || true
systemctl mask ssh.service ssh.socket
systemctl enable collab-broker.service
dpkg-query -W >/usr/local/share/collab-ai/os-packages.txt

# Images must not share SSH host keys, machine identities, credentials or state.
rm -f /etc/ssh/ssh_host_* /var/lib/dbus/machine-id
truncate -s 0 /etc/machine-id
go clean -cache -modcache -testcache
apt-get clean
rm -rf /var/lib/apt/lists/* /root/.cache /root/.codex /root/.claude /root/.ssh
rm -f /root/.claude.json /root/.bash_history /usr/sbin/policy-rc.d
find /var/log -type f -exec truncate -s 0 {} +
rm -rf /root/build /root/source.tar /tmp/* /var/tmp/*
