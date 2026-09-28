"""Offline fixture: a descendant changes session and ignores graceful shutdown."""

import os
from pathlib import Path
import signal
import subprocess
import sys
import time


if sys.argv[1] == "parent":
    subprocess.Popen([sys.executable, __file__, "child"], start_new_session=True)
else:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    Path("/var/lib/collab-ai-secured/orphan.pid").write_text(str(os.getpid()))
while True:
    time.sleep(1)
