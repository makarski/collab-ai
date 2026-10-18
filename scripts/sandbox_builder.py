"""Disposable Incus build containers from the pinned Ubuntu base image."""

import argparse
import json
import re
import subprocess
import uuid

from sandbox_base_image import DEFAULT_REPO, ensure_base


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def server_architecture(remote):
    server = json.loads(run(["incus", "query", f"{remote}:/1.0"], capture_output=True, text=True).stdout)
    return server["environment"]["architectures"][0]


def cleanup_project(remote, project):
    # Explicit deletion works with the Incus 6.0 LTS client too. All resources
    # are confined to the UUID project successfully created by this invocation.
    scoped = ["incus", "--project", project]
    instances = json.loads(run(scoped + ["list", f"{remote}:", "--format", "json"],
                               capture_output=True, text=True).stdout)
    for instance in instances:
        run(scoped + ["delete", f"{remote}:{instance['name']}", "--force"])
    images = json.loads(run(scoped + ["image", "list", f"{remote}:", "--format", "json"],
                            capture_output=True, text=True).stdout)
    for image in images:
        run(scoped + ["image", "delete", f"{remote}:{image['fingerprint']}"])
    run(["incus", "project", "delete", f"{remote}:{project}"])


class Builder:
    """One unprivileged container in its own UUID project; `cleanup` is the
    whole cleanup boundary, and only runs after the project was created."""

    NAME = "builder"

    def __init__(self, remote, prefix, storage_pool, network):
        self.remote = remote
        self.project = prefix + "-" + uuid.uuid4().hex[:12]
        self.target = f"{remote}:{self.NAME}"
        self.command = ["incus", "--project", self.project]
        self.storage_pool, self.network = storage_pool, network
        self.created = False

    def create(self, base, repo=DEFAULT_REPO, disk="12GiB", memory="4GiB"):
        run(["incus", "project", "create", f"{self.remote}:{self.project}",
             "-c", "features.images=true", "-c", "features.profiles=true",
             "-c", "features.networks=false", "-c", "features.storage.volumes=true"])
        self.created = True
        image = ensure_base(self.remote, self.project, base, repo).ref
        run(self.command + ["init", image, self.target, "--no-profiles",
                            "--storage", self.storage_pool, "--network", self.network,
                            "-d", f"root,size={disk}", "-c", "security.privileged=false",
                            "-c", "security.nesting=false", "-c", "security.guestapi=false",
                            "-c", "security.idmap.isolated=true", "-c", "limits.cpu=4",
                            "-c", f"limits.memory={memory}", "-c", "limits.processes=1024"])
        run(self.command + ["start", self.target])

    def execute(self, *args, **kwargs):
        return run(self.command + ["exec", self.target, "-T", "--", *args], **kwargs)

    def output(self, *args):
        return self.execute(*args, capture_output=True, text=True).stdout

    def push(self, source, destination):
        run(self.command + ["file", "push", str(source), self.target + destination])

    def pull(self, source, destination):
        run(self.command + ["file", "pull", self.target + source, str(destination)])

    def cleanup(self):
        if self.created:
            cleanup_project(self.remote, self.project)
