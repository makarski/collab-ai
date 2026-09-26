import importlib.util
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("sandbox_ssh", Path(__file__).resolve().parents[1] / "sandbox-ssh.py")
ssh = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ssh)


class SSHTests(unittest.TestCase):
    def test_host_key_rotation_requires_operator_action(self):
        with tempfile.TemporaryDirectory() as directory:
            known_hosts = Path(directory) / "known_hosts"
            ssh.pin_host_key(known_hosts, "ssh-ed25519 AAAA first-comment")
            ssh.pin_host_key(known_hosts, "ssh-ed25519 AAAA changed-comment")
            with self.assertRaisesRegex(ValueError, "host key changed"):
                ssh.pin_host_key(known_hosts, "ssh-ed25519 BBBB")
            self.assertEqual(known_hosts.read_text(), "collab-workspace ssh-ed25519 AAAA\n")

    def test_state_cannot_be_reused_for_another_endpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "ssh"
            ssh.private_state(state, {"project": "collab-one"})
            with self.assertRaisesRegex(ValueError, "another endpoint"):
                ssh.private_state(state, {"project": "collab-two"})

    def test_symlinked_private_key_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            (state / "id_ed25519").symlink_to(state / "elsewhere")
            with self.assertRaisesRegex(ValueError, "symlinked SSH state"):
                ssh.private_state(state, {})

    def test_config_is_strict_and_escapes_paths_with_spaces_and_percent(self):
        config = ssh.ssh_config(Path("/tmp/my %h workspace"), ["/path with spaces/incus", "exec", "local:workspace"])
        self.assertIn('IdentityFile "/tmp/my %%h workspace/id_ed25519"', config)
        self.assertIn("ProxyCommand '/path with spaces/incus' exec local:workspace", config)
        self.assertIn("StrictHostKeyChecking yes", config)
        self.assertIn("IdentityAgent none", config)
        self.assertIn("ClearAllForwardings yes", config)


if __name__ == "__main__":
    unittest.main()
