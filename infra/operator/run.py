#!/usr/bin/env python3
"""Run only the prepared plan or apply inside the trusted operator container."""

import json
import os
from pathlib import Path
import subprocess
import sys


settings = json.loads(Path("/operator/settings.json").read_text())
env = dict(os.environ, TF_IN_AUTOMATION="1", TF_INPUT="0", TF_HTTP_USERNAME="operator",
           TF_HTTP_PASSWORD=settings["token"], TF_HTTP_ADDRESS="http://127.0.0.1:8080/state",
           TF_HTTP_LOCK_ADDRESS="http://127.0.0.1:8080/state",
           TF_HTTP_UNLOCK_ADDRESS="http://127.0.0.1:8080/state")
base = ["/operator/bin/tofu", "-chdir=/operator/config"]
subprocess.run(base + ["init", "-input=false", "-lockfile=readonly"], env=env, check=True)
if settings["action"] == "plan":
    command = ["plan", "-input=false", "-out=/operator/approved.tfplan",
               "-var-file=/operator/variables.json"]
    if settings.get("destroy"):
        command.append("-destroy")
    if settings["replace"]:
        command.append("-replace=incus_instance.workspace")
else:
    command = ["apply", "-input=false", "/operator/approved.tfplan"]
sys.exit(subprocess.run(base + command, env=env).returncode)
