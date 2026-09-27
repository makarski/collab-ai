"""Disposable host/project operations for the offline two-container proof."""

import json
from pathlib import Path
import platform
import re
import subprocess
import uuid

from sandbox_artifacts import sha256


def run(command, **kwargs):
    result = subprocess.run(command, capture_output=True, text=True, timeout=180, **kwargs)
    if result.returncode:
        raise ValueError(f"Proof command failed: {command}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def host_prefix(remote):
    if platform.system() == "Linux" and remote == "local":
        return []
    if platform.system() == "Darwin" and remote == "colima-collab-ai":
        return ["colima", "-p", "collab-ai", "ssh", "--", "sudo"]
    raise ValueError("Proof supports Linux local or macOS colima-collab-ai only")


class BoundaryHost:
    def __init__(self, args):
        self.args = args
        self.remote = args.remote + ":"
        self.project = "collab-boundary-" + uuid.uuid4().hex[:12]
        self.base = ["incus", "--project", self.project]
        self.host = host_prefix(args.remote)
        self.root = None
        self.created = False
        self.profiles = []

    def execute(self, role, *command):
        return self.base + ["exec", self.remote + role, "-T", "--", *command]

    def exe(self, role, *command):
        return run(self.execute(role, *command))

    def push(self, role, source, destination):
        run(self.base + ["file", "push", str(source), self.remote + role + destination])

    def prepare(self):
        image = self.args.image_dir / "workspace.tar.gz"
        manifest = json.loads((self.args.image_dir / "manifest.json").read_text())
        if sha256(image) != manifest["image_fingerprint"]:
            raise ValueError("Proof image does not match its manifest")
        self.root = run(self.host + ["mktemp", "-d", "/tmp/collab-boundary-XXXXXXXX"]).strip()
        if not re.fullmatch(r"/tmp/collab-boundary-[a-zA-Z0-9]+", self.root):
            raise ValueError("Unexpected proof directory returned by host")
        run(self.host + ["chmod", "711", self.root])
        for name in ("status", "executor"):
            run(self.host + ["mkdir", self.root + "/" + name])
            run(self.host + ["chmod", "777", self.root + "/" + name])
        self.create_project()
        run(self.base + ["image", "import", str(image), self.remote])
        for role in ("secured", "dev"):
            self.create_instance(role, manifest["image_fingerprint"])

    def create_project(self):
        run(["incus", "project", "create", self.remote + self.project])
        self.created = True
        config = {"features.images": "true", "features.profiles": "true", "restricted": "true",
                  "restricted.containers.privilege": "isolated", "restricted.containers.nesting": "block",
                  "restricted.devices.disk": "allow", "restricted.devices.disk.paths": self.root + "/status," + self.root + "/executor",
                  "restricted.devices.nic": "block", "restricted.devices.proxy": "block",
                  "limits.containers": "2", "limits.virtual-machines": "0"}
        run(["incus", "project", "edit", self.remote + self.project],
            input=json.dumps({"config": config, "description": "Disposable offline boundary proof"}))

    def create_instance(self, role, fingerprint):
        run(self.base + ["profile", "create", self.remote + role])
        self.profiles.append(role)
        run(self.base + ["profile", "edit", self.remote + role], input=json.dumps(self.profile(role)))
        run(self.base + ["init", self.remote + fingerprint, self.remote + role, "--profile", role])
        run(self.base + ["start", self.remote + role])
        self.exe(role, "mkdir", "-p", "/opt/proof")

    def profile(self, role):
        devices = {"root": {"type": "disk", "path": "/", "pool": self.args.storage_pool, "size": "10GiB"}}
        for name in ("status", "executor"):
            devices[name] = {"type": "disk", "source": self.root + "/" + name, "path": "/mnt/proof-" + name,
                             "readonly": str((role == "dev") == (name == "status")).lower()}
        config = {"security.privileged": "false", "security.idmap.isolated": "true", "security.nesting": "false",
                  "security.guestapi": "false", "boot.autostart": "false", "limits.memory": "2GiB",
                  "limits.cpu": "1", "limits.processes": "256"}
        return {"config": config, "devices": devices}

    def cleanup(self):
        if self.created:
            instances = json.loads(run(self.base + ["list", self.remote, "--format", "json"]))
            for instance in instances:
                run(self.base + ["delete", self.remote + instance["name"], "--force"])
            for profile in self.profiles:
                run(self.base + ["profile", "delete", self.remote + profile])
            images = json.loads(run(self.base + ["image", "list", self.remote, "--format", "json"]))
            for image in images:
                run(self.base + ["image", "delete", self.remote + image["fingerprint"]])
            run(["incus", "project", "delete", self.remote + self.project])
        if self.root and re.fullmatch(r"/tmp/collab-boundary-[a-zA-Z0-9]+", self.root):
            run(self.host + ["rm", "-rf", "--", self.root])
