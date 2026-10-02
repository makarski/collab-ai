"""Restrict the macOS file server independently of the SSHFS client."""

import json
from pathlib import Path


SERVER = "/usr/libexec/sftp-server"


def literal(value):
    return json.dumps(str(value), ensure_ascii=False)


def profile(source, readonly):
    source = Path(source).resolve(strict=True)
    parents = " ".join(f"(literal {literal(path)})" for path in source.parents)
    rules = [
        "(version 1)", "(deny default)",
        f"(allow process-exec (literal {literal(SERVER)}))",
        "(allow sysctl-read)",
        '(allow mach-lookup (global-name "com.apple.system.logger") '
        '(global-name "com.apple.system.opendirectoryd.libinfo"))',
        '(allow file-read* (subpath "/usr/lib") (subpath "/System/Library") '
        '(literal "/") (literal "/dev/null") (literal "/dev/urandom") (literal "/dev/random"))',
        f"(allow file-read* (literal {literal(SERVER)}))",
        '(allow file-write* (literal "/dev/null"))',
        f"(allow file-read-metadata {parents} (literal {literal(SERVER)}))",
        f"(allow file-read* (subpath {literal(source)}))",
    ]
    if not readonly:
        rules.append(f"(allow file-write* (subpath {literal(source)}))")
    return "\n".join(rules)


def server_command(source, readonly, user=None):
    command = ["/usr/bin/sandbox-exec", "-p", profile(source, readonly), SERVER, "-d", str(source)]
    if readonly:
        command.append("-R")
    if user:
        command = ["sudo", "-n", "--", "/usr/bin/python3", "-I",
                   "/Library/Application Support/collab-ai/sharing/host.py", user, str(source), str(readonly).lower()]
    return command
