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
# The mount smoke test maps just its fixture owner into the container. Ubuntu's
# newuidmap/newgidmap also require this host-level allowance, independently of
# the restricted Incus project's UID/GID allowlist.
usermod --add-subuids 1001-1001 --add-subgids 1001-1001 root
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
