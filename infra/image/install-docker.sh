#!/bin/bash
# Image build only. The rootful daemon is never available to workspace agents.
set -euo pipefail
apt-get install -y --no-install-recommends uidmap dbus-user-session slirp4netns \
    fuse-overlayfs bubblewrap apparmor apparmor-profiles
install -d -m 0755 /etc/apt/keyrings
curl --fail --silent --show-error --location --retry 3 \
    https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
arch=$(dpkg --print-architecture)
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable\n' \
    "$arch" >/etc/apt/sources.list.d/docker.list
apt-get -o APT::Update::Error-Mode=any -o Acquire::Retries=3 update
mapfile -t packages < /root/build/infra/image/docker-packages.txt
apt-get install -y --no-install-recommends "${packages[@]}"
systemctl mask docker.service docker.socket containerd.service
# Noble ships this profile separately. Load it at boot in the container's
# AppArmor namespace, never disable the host's user-namespace restriction.
install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict \
    /etc/apparmor.d/bwrap-userns-restrict
docker --version
docker compose version
docker buildx version
