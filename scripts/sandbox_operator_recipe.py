"""Refresh generated deployment code while retaining operator settings and state."""

from pathlib import Path
import tempfile

from sandbox_operator_config import ROOT, provisioning_files
from sandbox_operator_state import private_write


def recipe_files(source):
    return {name: content for name, content in provisioning_files(source).items()
            if name.endswith((".tf", ".tf.json", ".py")) or name == ".terraform.lock.hcl"}


def previous_recipe(target):
    if target.is_symlink():
        raise ValueError(f"Expected a regular recipe file: {target}")
    if not target.exists():
        return None
    if not target.is_file():
        raise ValueError(f"Expected a regular recipe file: {target}")
    return target.read_bytes()


def recipe_changes(directory, source):
    changes = {}
    for name, content in recipe_files(source).items():
        previous = previous_recipe(directory / name)
        if previous != content:
            changes[name] = (previous, content)
    return changes


def refresh_recipe(directory, source=ROOT / "infra/incus"):
    changes = recipe_changes(directory, source)
    if not changes:
        return
    backup = Path(tempfile.mkdtemp(prefix="recipe-backup-", dir=directory))
    for name, (previous, _) in changes.items():
        if previous is not None:
            private_write(backup / name, previous)
    for name, (_, content) in changes.items():
        private_write(directory / name, content)
    print(f"Updated provisioning recipe; previous files: {backup}", flush=True)
