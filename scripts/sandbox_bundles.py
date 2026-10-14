"""Optional tool bundles, built inside a container of the pinned base image.

A bundle is one digest-pinned release asset the tool store installs offline:
the Playwright MCP closure, the apt packages Chromium needs, and Docker's apt
packages. Building them in the base image makes the package closure exactly
what the slim image lacks.
"""

import json
from pathlib import Path
import re

from sandbox_artifacts import sha256

ROOT = Path(__file__).resolve().parents[1]
DEBS = "/root/debs"
TAR_FLAGS = ["--sort=name", "--owner=0", "--group=0", "--numeric-owner", "--mtime=@0"]

# Playwright's Ubuntu 24.04 dependency table for Chromium, without xvfb: the
# MCP runs Chromium headless.
CHROMIUM_LIBS = [
    "libasound2t64", "libatk-bridge2.0-0t64", "libatk1.0-0t64", "libatspi2.0-0t64", "libcairo2",
    "libcups2t64", "libdbus-1-3", "libdrm2", "libgbm1", "libglib2.0-0t64", "libnspr4", "libnss3",
    "libpango-1.0-0", "libx11-6", "libxcb1", "libxcomposite1", "libxdamage1", "libxext6", "libxfixes3",
    "libxkbcommon0", "libxrandr2",
]
CHROMIUM_FONTS = [
    "fonts-noto-color-emoji", "fonts-unifont", "libfontconfig1", "libfreetype6", "xfonts-cyrillic",
    "xfonts-scalable", "fonts-liberation", "fonts-ipafont-gothic", "fonts-wqy-zenhei",
    "fonts-tlwg-loma-otf", "fonts-freefont-ttf",
]
DOCKER_SUPPORT = ["uidmap", "dbus-user-session", "slirp4netns", "fuse-overlayfs", "bubblewrap",
                  "apparmor", "apparmor-profiles"]
DOCKER_KEY = "https://download.docker.com/linux/ubuntu/gpg"
BUNDLE_KINDS = ("playwright", "chromium-deps", "docker")


def bundle_file(kind, arch):
    return f"collab-ai-bundle-{kind}-linux-{arch}.tar.gz"


def manifest_file(arch):
    return f"collab-ai-bundles-linux-{arch}.json"


def docker_packages(root=ROOT):
    return (root / "infra/image/docker-packages.txt").read_text().split()


def deb_inventory(listing):
    """{package: version} from the archive file names apt downloaded."""
    inventory = {}
    for name in listing.split():
        match = re.fullmatch(r"([a-z0-9][a-z0-9+.-]*)_([^_]+)_[a-z0-9]+\.deb", name)
        if match is None:
            raise ValueError(f"Unexpected file in the package download directory: {name}")
        inventory[match.group(1)] = match.group(2).replace("%3a", ":")
    return inventory


def archive(builder, kind, source, arch, output):
    name = bundle_file(kind, arch)
    builder.execute("tar", *TAR_FLAGS, "-czf", "/root/" + name, "-C", source, ".")
    builder.pull("/root/" + name, output / name)
    return {"file": name, "sha256": sha256(output / name)}


def deb_bundle(builder, kind, packages, arch, output):
    """Download packages plus the dependencies the base image lacks; the
    archive holds .deb files only, installed offline with dpkg later."""
    builder.execute("rm", "-rf", DEBS)
    builder.execute("mkdir", "-p", DEBS)
    builder.execute("apt-get", "-o", f"Dir::Cache::archives={DEBS}", "install", "--download-only", "-y",
                    "--no-install-recommends", *packages)
    builder.execute("sh", "-c", f"rm -rf {DEBS}/partial {DEBS}/lock")
    details = archive(builder, kind, DEBS, arch, output)
    details["packages"] = deb_inventory(builder.output("ls", DEBS))
    return details


def docker_repository(builder):
    builder.execute("apt-get", "install", "-y", "--no-install-recommends", "ca-certificates", "curl")
    builder.execute("install", "-d", "-m", "0755", "/etc/apt/keyrings")
    builder.execute("curl", "--fail", "--silent", "--show-error", "--location", "--retry", "3",
                    DOCKER_KEY, "-o", "/etc/apt/keyrings/docker.asc")
    arch = builder.output("dpkg", "--print-architecture").strip()
    builder.execute("sh", "-c", f"printf 'deb [arch={arch} signed-by=/etc/apt/keyrings/docker.asc] "
                    "https://download.docker.com/linux/ubuntu noble stable\\n' "
                    ">/etc/apt/sources.list.d/docker.list")
    builder.execute("apt-get", "-o", "APT::Update::Error-Mode=any", "-o", "Acquire::Retries=3", "update")


def playwright_bundle(builder, arch, output, root=ROOT):
    builder.execute("mkdir", "-p", "/root/bundle-src")
    for name in ("tool_download.py", "tool_lock.py", "tools.lock.json", "build-playwright-bundle.py",
                 "mcp/package.json", "mcp/package-lock.json"):
        builder.push(root / "infra/image" / name, "/root/bundle-src/" + Path(name).name)
    builder.execute("apt-get", "install", "-y", "--no-install-recommends", "ca-certificates", "python3")
    builder.execute("python3", "/root/bundle-src/build-playwright-bundle.py")
    details = archive(builder, "playwright", "/root/playwright-bundle", arch, output)
    details.update(json.loads(builder.output("cat", "/root/playwright-bundle/bundle.json")))
    return details


def build_bundles(builder, arch, output, kinds=BUNDLE_KINDS, root=ROOT):
    """Build the requested bundles and write their manifest next to them."""
    builder.execute("apt-get", "-o", "APT::Update::Error-Mode=any", "-o", "Acquire::Retries=3", "update")
    bundles = {}
    if "playwright" in kinds:
        bundles["playwright"] = playwright_bundle(builder, arch, output, root)
    if "chromium-deps" in kinds:
        bundles["chromium-deps"] = deb_bundle(builder, "chromium-deps", CHROMIUM_LIBS + CHROMIUM_FONTS, arch, output)
    if "docker" in kinds:
        docker_repository(builder)
        bundles["docker"] = deb_bundle(builder, "docker", docker_packages(root) + DOCKER_SUPPORT, arch, output)
    manifest = {"architecture": arch, "bundles": bundles}
    (output / manifest_file(arch)).write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest
