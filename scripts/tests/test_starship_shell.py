from pathlib import Path
import shlex
import subprocess
import unittest


STARTUP = Path(__file__).resolve().parents[2] / "infra/image/collab-starship.sh"


class StarshipShellTests(unittest.TestCase):
    def shell(self, uid=1001, interactive=True):
        # The init stub installs a visible prompt hook; it must run only once.
        command = (
            f'id() {{ echo {uid}; }}\n'
            'function /usr/local/bin/starship() {\n'
            '  test "$*" = "init bash" || return 1\n'
            '  echo "PROMPT_COMMAND=starship_precmd; calls=$((calls + 1))"\n'
            '}\n'
            f'. {shlex.quote(str(STARTUP))}\n'
            f'. {shlex.quote(str(STARTUP))}\n'
            'printf "%s:%s" "${calls:-0}" "${PROMPT_COMMAND:-}"'
        )
        return subprocess.run(
            ["bash", "--noprofile", "--norc", "-ic" if interactive else "-c", command],
            check=True, capture_output=True, text=True).stdout

    def test_repeated_shell_startup_installs_one_prompt_hook(self):
        self.assertEqual(self.shell(), "1:starship_precmd")

    def test_scripts_and_control_shells_do_not_initialize_a_prompt(self):
        for uid, interactive in ((1001, False), (0, True)):
            with self.subTest(uid=uid, interactive=interactive):
                self.assertEqual(self.shell(uid, interactive), "0:")
