"""Checksum-verified native archives shared by image installers."""

import hashlib
import subprocess
import urllib.request


def download(url, checksum, destination):
    digest = hashlib.sha256()
    with urllib.request.urlopen(url, timeout=120) as response, destination.open("wb") as out:
        while chunk := response.read(1024 * 1024):
            digest.update(chunk)
            out.write(chunk)
    if digest.hexdigest() != checksum:
        raise ValueError(f"SHA256 mismatch for {url}")


def extract(archive, destination):
    # Vendor archive UIDs must never make installed code agent-owned.
    subprocess.run(["tar", "--no-same-owner", "--no-same-permissions", "-xzf", str(archive),
                    "-C", str(destination)], check=True, umask=0o022)
