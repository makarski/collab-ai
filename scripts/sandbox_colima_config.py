"""Read managed Colima settings, including Colima's expanded YAML format."""

import json
import subprocess


# Colima writes these defaults when saving a minimal configuration. Accept only
# known inert values, rather than ignoring extra settings such as provision.
DEFAULTS = {
    "modelRunner": "", "hostname": "colima-collab-ai",
    "docker": {}, "portForwarder": "ssh", "rosetta": False, "binfmt": True,
    "nestedVirtualization": False, "mountInotify": False, "cpuType": "",
    "provision": None, "sshPort": 0, "diskImage": "", "forceDiskImage": False,
    "rootDisk": 20, "env": {},
    "network": {
        "mode": "", "interface": "", "preferredRoute": False, "dns": None,
        "dnsHosts": {}, "hostAddresses": False, "gatewayAddress": None,
    },
}
YAML_TO_JSON = "require 'yaml'; require 'json'; puts JSON.generate(YAML.safe_load(STDIN.read, aliases: false))"


def read_settings(path):
    text = path.read_text()
    try:
        settings = json.loads(text)
    except json.JSONDecodeError:
        settings = read_yaml(text, path)
    if not isinstance(settings, dict):
        raise ValueError(f"Colima configuration must be a mapping: {path}")
    return settings


def read_yaml(text, path):
    # macOS ships Ruby/Psych: no pip install or extra host package is needed.
    try:
        result = subprocess.run(["/usr/bin/ruby", "-e", YAML_TO_JSON], input=text,
                                capture_output=True, text=True, check=True, timeout=30)
        return json.loads(result.stdout)
    except (OSError, subprocess.SubprocessError, ValueError) as error:
        raise ValueError(f"Cannot read Colima YAML at {path} using macOS /usr/bin/ruby; inspect the file") from error


def check_settings(path, expected):
    settings = read_settings(path)
    check_kubernetes(settings.pop("kubernetes", None), path)
    allowed = {**DEFAULTS, **expected}
    allowed["network"] = {**DEFAULTS["network"], **expected["network"]}
    compare_settings(settings, allowed, "", path)
    require_settings(settings, expected, "", path)


def check_kubernetes(config, path):
    if config is None:
        return
    if not isinstance(config, dict) or config.get("enabled") is not False:
        drift("kubernetes.enabled", path)
    # Version and arguments are inactive while Kubernetes is disabled.
    if set(config) - {"enabled", "version", "k3sArgs", "port"}:
        drift("kubernetes", path)


def compare_settings(actual, allowed, prefix, path):
    for key, value in actual.items():
        name = prefix + key
        if key not in allowed:
            drift(name, path)
        wanted = allowed[key]
        if isinstance(wanted, dict) and isinstance(value, dict):
            compare_settings(value, wanted, name + ".", path)
        elif type(value) is not type(wanted) or value != wanted:
            drift(name, path)


def require_settings(actual, expected, prefix, path):
    for key, value in expected.items():
        if key not in actual:
            drift(prefix + key, path)
        if isinstance(value, dict):
            require_settings(actual[key], value, prefix + key + ".", path)


def drift(key, path):
    raise ValueError(f"Colima configuration drifted at {key}: inspect {path} before proceeding")
