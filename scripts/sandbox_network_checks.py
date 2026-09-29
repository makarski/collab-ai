"""Exercise dev connectivity and its opt-out without model requests or public endpoints."""

import json
import time

from sandbox_secured_checks import Deployment, run


def prepare(directory, project):
    # A second instance named workspace cannot share the operator's bridge DNS.
    # Own the test bridge in this disposable state so teardown also removes it.
    suffix = project.removeprefix("collab-smoke-")
    name = "cs-" + suffix[:12]
    # Incus auto subnet probes cannot work behind every VM network (e.g. Lima).
    # Use benchmark address space without NAT, only for the disposable bridge.
    address = f"198.18.{int(suffix[-2:], 16)}.1/24"
    resources = {
        "incus_network": {"smoke": {
            "name": name, "project": "default",
            "config": {"ipv4.address": address, "ipv4.nat": "false", "ipv6.address": "none"},
        }},
    }
    (directory / "smoke-network.tf.json").write_text(json.dumps({"resource": resources}))
    main = directory / "main.tf"
    original = "depends_on = [terraform_data.secured_preflight]"
    configuration = main.read_text()
    if configuration.count(original) != 1:
        raise ValueError("Cannot order the disposable test project after its bridge")
    main.write_text(configuration.replace(original,
        "depends_on = [terraform_data.secured_preflight, incus_network.smoke]"))
    return name


def require_offline(deployment, role):
    links = json.loads(run(deployment.execute(role, "ip", "-json", "link")))
    if [link["ifname"] for link in links] != ["lo"]:
        raise ValueError(f"{role} unexpectedly has a network interface")


def gateway(deployment):
    for _ in range(30):
        routes = json.loads(run(deployment.execute("workspace", "ip", "-json", "-4", "route", "show", "default")))
        for route in routes:
            if route.get("dev") == "eth0" and route.get("gateway"):
                return route["gateway"]
        time.sleep(1)
    raise ValueError("Dev did not receive a DHCP default route")


def verify(args, directory, project):
    deployment = Deployment(args, directory, project)
    for enabled in (True, False):
        run([args.tofu, f"-chdir={directory}", "apply", "-auto-approve", "-input=false",
             f"-var=dev_network_enabled={str(enabled).lower()}"])
        if enabled:
            address = gateway(deployment)
            run(deployment.execute("workspace", "python3", "-c",
                                   "import socket,sys; socket.create_connection((sys.argv[1],53),5).close()", address))
        else:
            require_offline(deployment, "workspace")
        if args.secured_check:
            require_offline(deployment, "secured")
    print("PASS: dev DHCP/bridge connectivity, offline opt-out, and control without a NIC.", flush=True)
