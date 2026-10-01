"""Prove dev data retention and explicit deletion in disposable Incus projects."""

from pathlib import Path
import subprocess
import sys

from sandbox_secured_checks import Deployment, run, denied
import sandbox_rtk_checks
import sandbox_docker_checks


ROOT = Path(__file__).resolve().parents[1]
FILES = ("/workspace/persistence-proof/repository.txt",
         "/home/agent/.codex/persistence-proof/session.jsonl",
         "/home/agent/.claude/persistence-proof/memory.md",
         "/home/agent/.claude.json")
CONTENTS = '{"retention_proof":true}\n'


def prepare(args, directory, project):
    deployment = Deployment(args, directory, project)
    run(deployment.execute("workspace", "systemctl", "start", "collab-workspace-setup"))
    script = ("from pathlib import Path\n"
              f"for name in {FILES!r}:\n"
              f" p=Path(name); p.parent.mkdir(parents=True, exist_ok=True); p.write_text({CONTENTS!r})\n"
              "with Path('/home/agent/.profile').open('a') as out: out.write('\\n# retention-proof\\n')\n")
    run(deployment.execute("workspace", "runuser", "-u", "agent", "--", "python3", "-c", script))


def check_data(deployment):
    run(deployment.execute("workspace", "systemctl", "start", "collab-workspace-setup"))
    script = ("from pathlib import Path\n"
              f"assert all(Path(name).read_text() == {CONTENTS!r} for name in {FILES!r})\n"
              "assert '# retention-proof' in Path('/home/agent/.profile').read_text()\n")
    run(deployment.execute("workspace", "runuser", "-u", "agent", "--", "python3", "-c", script))
    check_volume_ownership(deployment)
    denied(deployment.execute("workspace", "cat", FILES[1], uid=65534))
    denied(deployment.execute("workspace", "ls", "/var/lib/collab-ai-docker", uid=65534))


def check_volume_ownership(deployment):
    # Docker changes its data-root to 0710 at startup: only the agent's own
    # group gains traversal, with no group read/write or access for others.
    for path, modes in (("/workspace", ("750",)), ("/home/agent", ("700",)),
                        ("/var/lib/collab-ai-docker", ("700", "710"))):
        observed = run(deployment.execute("workspace", "stat", "-c", "%a %u %g", path)).strip()
        if observed not in {f"{mode} 1001 1001" for mode in modes}:
            raise ValueError(f"Unsafe persistent volume ownership: {path}: {observed}")


def verify(args, directory, project):
    deployment = Deployment(args, directory, project)
    check_data(deployment)
    backup(deployment, directory)
    guard = subprocess.run(deployment.tofu + ["plan", "-destroy", "-input=false"],
                           capture_output=True, text=True, timeout=120)
    if guard.returncode == 0 or "prevent_destroy" not in guard.stderr:
        raise ValueError("Persistent dev volumes were not protected from destruction")
    run(deployment.execute("workspace", "touch", "/root/replacement-proof"))
    run(deployment.tofu + ["apply", "-auto-approve", "-input=false", "-replace=incus_instance.workspace"])
    run(deployment.execute("workspace", "test", "!", "-e", "/root/replacement-proof"))
    check_data(deployment)
    restore(deployment, directory)
    check_data(deployment)
    check_ssh_replacement(args, directory, project)
    run(deployment.tofu + ["plan", "-input=false", "-detailed-exitcode"])
    if args.secured_check:
        for path in FILES:
            run(deployment.execute("secured", "test", "!", "-e", path))
    print("PASS: projects/home survive dev replacement, private ownership, SSH re-pinning and explicit deletion guard.", flush=True)


def backup(deployment, directory):
    for name, path in (("workspace", "/workspace"), ("home", "/home/agent")):
        with (directory / f"{name}.tgz").open("wb") as archive:
            subprocess.run(deployment.execute("workspace", "tar", "-C", path,
                "--one-file-system", "-czf", "-", ".", uid=1001), stdout=archive, check=True, timeout=30)


def restore(deployment, directory):
    run(deployment.execute("workspace", "rm", *FILES, uid=1001))  # Only this proof's disposable files.
    for name, path in (("workspace", "/workspace"), ("home", "/home/agent")):
        with (directory / f"{name}.tgz").open("rb") as archive:
            subprocess.run(deployment.execute("workspace", "tar", "-C", path,
                "--no-same-owner", "--no-overwrite-dir", "-xzf", "-", uid=1001), stdin=archive, check=True, timeout=30)
    print("PASS: operator backup/restore preserves workspace and private home files as agent.", flush=True)


def check_ssh_replacement(args, directory, project):
    ssh = directory / "ssh"
    command = [sys.executable, str(ROOT / "scripts/sandbox-ssh.py"), "--remote", args.remote,
               "--project", project,
               "--state-dir", str(ssh)]
    result = subprocess.run(command, capture_output=True, text=True, timeout=30)
    if result.returncode == 0 or "SSH host key changed" not in result.stderr:
        raise ValueError("Replacement did not require explicit SSH host-key renewal")
    (ssh / "known_hosts").unlink()  # Only after this test's verified replacement.
    run(command)
    run(["ssh", "-F", str(ssh / "config"), "workspace", "test -f /workspace/persistence-proof/repository.txt"])
    sandbox_rtk_checks.verify(["ssh", "-F", str(ssh / "config"), "workspace"])
    sandbox_docker_checks.verify(["ssh", "-F", str(ssh / "config"), "workspace"])


def allow_test_teardown(directory, project):
    if not project.startswith("collab-smoke-"):
        raise ValueError("Refusing to remove data protection outside a disposable smoke project")
    recipe = directory / "storage.tf"
    if recipe.exists():
        recipe.write_text(recipe.read_text().replace("prevent_destroy = true", "prevent_destroy = false"))
