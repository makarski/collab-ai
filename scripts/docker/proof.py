"""Offline build, Compose networking and rootless persistence proof; run as agent."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time


IMAGE = "collab-docker-proof:local"
VOLUME = "collab-docker-proof-state"
SOURCE = '''package main
import ("fmt"; "net/http"; "io"; "os")
func main() {
 switch os.Args[1] {
 case "build": if os.Getuid()!=1000 { panic("subordinate UID missing") }
 case "write": if err:=os.WriteFile("/data/proof", []byte("retained"), 0600); err!=nil { panic(err) }
 case "read": b,err:=os.ReadFile("/data/proof"); if err!=nil || string(b)!="retained" { panic("data lost") }
 case "private": if _,err:=os.ReadFile("/secret"); err!=nil { os.Exit(1) }
 case "serve":
  http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,"collab-proof")})
  panic(http.ListenAndServe(":8080",nil))
 case "get":
  r,err:=http.Get("http://server:8080"); if err!=nil { panic(err) }; defer r.Body.Close()
  b,err:=io.ReadAll(r.Body); if err!=nil || string(b)!="collab-proof" { panic("network failed") }
 }
}
'''


def run(*command, **kwargs):
    return subprocess.run(command, check=True, text=True, capture_output=True, timeout=120, **kwargs)


def check_runtime():
    if os.getuid() != 1001:
        raise ValueError("Docker proof must run as agent")
    info = docker_info()
    if "name=rootless" not in info["SecurityOptions"]:
        raise ValueError("Docker daemon must be rootless")
    if info["DockerRootDir"] != "/var/lib/collab-ai-docker":
        raise ValueError("Docker data must use its private persistent volume")
    run("docker", "compose", "version")
    run("docker", "buildx", "version")
    check_codex_sandbox()


def check_codex_sandbox():
    with tempfile.TemporaryDirectory(dir="/workspace") as allowed, \
            tempfile.TemporaryDirectory(dir=Path.home()) as outside:
        script = ("from pathlib import Path; import sys\n"
                  "Path('allowed').touch()\n"
                  "try: Path(sys.argv[1]).touch()\n"
                  "except PermissionError: sys.exit(0)\n"
                  "sys.exit('Codex sandbox permitted a write outside workspace')\n")
        run("codex", "sandbox", "-P", ":workspace", "-C", allowed, "--",
            "python3", "-c", script, str(Path(outside) / "denied"))


def docker_info():
    for attempt in range(30):
        try:
            return json.loads(run("docker", "info", "--format", "{{json .}}").stdout)
        except subprocess.CalledProcessError:
            if attempt == 29:
                raise
            time.sleep(1)


def build_fixture(directory):
    (directory / "main.go").write_text(SOURCE)
    env = dict(os.environ, CGO_ENABLED="0", GOTOOLCHAIN="local", GOPROXY="off")
    run("go", "build", "-o", str(directory / "probe"), str(directory / "main.go"), env=env)
    (directory / "Dockerfile").write_text(
        'FROM scratch\nCOPY probe /probe\nUSER 1000:1000\n'
        'RUN ["/probe", "build"]\nUSER 0:0\nENTRYPOINT ["/probe"]\n')
    run("docker", "build", "--network=none", "-t", IMAGE, str(directory))


def compose_fixture(directory):
    config = {"services": {
        "server": {"image": IMAGE, "command": ["serve"]},
        "client": {"image": IMAGE, "command": ["get"]},
        "writer": {"image": IMAGE, "command": ["write"], "volumes": ["state:/data"]},
    }, "volumes": {"state": {"name": VOLUME}}}
    path = directory / "compose.json"
    path.write_text(json.dumps(config))
    command = ["docker", "compose", "-p", "collab-docker-proof", "-f", str(path)]
    try:
        run(*command, "up", "-d", "server")
        for attempt in range(10):
            try:
                run(*command, "run", "--rm", "client")
                break
            except subprocess.CalledProcessError:
                if attempt == 9:
                    raise
                time.sleep(1)
        run(*command, "run", "--rm", "writer")
    finally:
        run(*command, "down")  # Retain the named volume for restart/replacement proof.


def verify():
    check_runtime()
    run("docker", "run", "--rm", "-v", f"{VOLUME}:/data", IMAGE, "read")
    private = subprocess.run(["docker", "run", "--rm", "-v", "/etc/shadow:/secret:ro",
                              IMAGE, "private"], capture_output=True, timeout=30)
    if private.returncode == 0:
        raise ValueError("Rootless Docker accessed a root-only workspace file")


if __name__ == "__main__":
    try:
        os.chdir(Path.home())
        if sys.argv[1] == "create":
            check_runtime()
            with tempfile.TemporaryDirectory(prefix="collab-docker-proof-") as temporary:
                directory = Path(temporary)
                build_fixture(directory)
                compose_fixture(directory)
        verify()
        print("PASS: rootless Docker, build, Compose, private-file denial and persistent data.")
    except subprocess.CalledProcessError as error:
        sys.exit(f"Docker proof failed: {error.cmd}\n{error.stdout}\n{error.stderr}")
