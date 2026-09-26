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
incus version
