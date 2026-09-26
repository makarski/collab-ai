#!/bin/bash
# Only for a disposable GitHub-hosted Ubuntu runner, never a developer machine.
set -euo pipefail
test "${GITHUB_ACTIONS:-}" = true
test "${RUNNER_ENVIRONMENT:-}" = github-hosted
test "$EUID" = 0
if test -e /var/lib/incus/database; then
    echo 'Refusing to initialize an existing Incus server' >&2
    exit 1
fi
apt-get update
apt-get install -y incus btrfs-progs
modprobe btrfs
systemctl start incus.socket
incus admin init --preseed <infra/image/ci-host.json
# GitHub runners also run Docker, whose forwarding policy can block Incus.
# Scope the exception to the disposable builder bridge; do not change the
# default policy or any developer host's firewall.
# https://linuxcontainers.org/incus/docs/main/howto/network_bridge_firewalld/
forward_chain=FORWARD
if iptables -S DOCKER-USER >/dev/null 2>&1; then
    forward_chain=DOCKER-USER
fi
iptables -I "$forward_chain" -i incusbr0 -j ACCEPT
iptables -I "$forward_chain" -o incusbr0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
incus version
