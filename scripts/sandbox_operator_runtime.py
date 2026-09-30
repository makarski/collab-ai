"""Disposable Incus operator with pinned OpenTofu and private admin/state proxies."""

import hashlib
import json
from pathlib import Path
import subprocess
import urllib.request
import uuid
import zipfile

from sandbox_operator_config import ROOT


class Operator:
    def __init__(self, remote, server, pool, network):
        self.remote = remote + ":"
        self.project = "collab-operator-" + uuid.uuid4().hex[:16]
        self.target = self.remote + "operator"
        self.base = ["incus", "--project", self.project]
        self.server, self.pool, self.network = server, pool, network
        self.created = False

    def run(self, *args, **kwargs):
        return subprocess.run(self.base + list(args), check=True, **kwargs)

    def execute(self, *args, **kwargs):
        return self.run("exec", self.target, "-T", "--", *args, **kwargs)

    def push(self, source, destination):
        self.run("file", "push", str(source), self.target + destination)

    def create(self):
        arch = self.server["architectures"][0]
        image_lock = json.loads((ROOT / "infra/image/tools.lock.json").read_text())
        base = image_lock["architectures"][arch]["base_image"]
        subprocess.run(["incus", "project", "create", self.remote + self.project,
                        "-c", "features.images=true", "-c", "features.profiles=true",
                        "-c", "features.networks=false"], check=True)
        self.created = True
        self.run("init", f"images:{base}", self.target, "--no-profiles", "--storage", self.pool,
                 "--network", self.network, "-c", "security.privileged=false",
                 "-c", "security.idmap.isolated=true", "-c", "security.nesting=false",
                 "-c", "security.guestapi=false", "-c", "boot.autostart=false",
                 "-c", "limits.cpu=2", "-c", "limits.memory=2GiB", "-c", "limits.processes=256",
                 "-d", "root,size=8GiB")
        self.run("start", self.target)
        self.execute("mkdir", "-p", "/operator/config", "/operator/bin")
        self.execute("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-o", "Acquire::Retries=3", "update", "-qq")
        self.execute("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y",
                     "--no-install-recommends", "python3", "ca-certificates")

    def install(self, temporary):
        lock = json.loads((ROOT / "infra/operator/tools.lock.json").read_text())
        item = lock["architectures"][self.server["architectures"][0]]
        url = f"https://github.com/opentofu/opentofu/releases/download/v{lock['tofu_version']}/{item['archive']}"
        archive = temporary / "tofu.zip"
        with urllib.request.urlopen(url, timeout=120) as response:
            archive.write_bytes(response.read())
        if hashlib.sha256(archive.read_bytes()).hexdigest() != item["sha256"]:
            raise ValueError("OpenTofu archive checksum mismatch")
        binary = temporary / "tofu"
        with zipfile.ZipFile(archive) as package:
            binary.write_bytes(package.read("tofu"))
        self.push(binary, "/operator/bin/tofu")
        self.execute("chmod", "755", "/operator/bin/tofu")
        self.execute("/operator/bin/tofu", "version")

    def connect(self, state_connection):
        # The operator is trusted administration. No workspace or control profile
        # receives either proxy, and no admin TCP listener is created.
        self.run("config", "device", "add", self.target, "admin", "proxy", "bind=instance",
                 "listen=unix:/run/operator-incus.sock", "connect=unix:/var/lib/incus/unix.socket",
                 "uid=0", "gid=0", "mode=0600")
        self.run("config", "device", "add", self.target, "state", "proxy", "bind=instance",
                 "listen=tcp:127.0.0.1:8080", "connect=" + state_connection)

    def cleanup(self):
        if not self.created:
            return
        instances = self.run("list", self.remote, "--format=json", capture_output=True, text=True)
        for instance in json.loads(instances.stdout):
            self.run("delete", self.remote + instance["name"], "--force")
        images = self.run("image", "list", self.remote, "--format=json", capture_output=True, text=True)
        for image in json.loads(images.stdout):
            self.run("image", "delete", self.remote + image["fingerprint"])
        subprocess.run(["incus", "project", "delete", self.remote + self.project], check=True)
