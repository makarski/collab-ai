# Provision without host Terraform

For automatic download and deployment, run `python3 scripts/sandbox-provision.py rollout`.
To preview changes separately, run from the repository on your host:

```sh
python3 scripts/sandbox-download.py --release latest
python3 scripts/sandbox-provision.py plan --image-dir dist/images/current
# Review, then apply the saved plan:
python3 scripts/sandbox-provision.py apply
```

Later plans reuse the selected image unless you pass another `--image-dir`. `--replace` rebuilds dev;
`--destroy` is only for [deliberate removal](sandbox-storage.md#deliberate-removal).

## What stays where

| Host | Disposable operator | Agent workspace |
| --- | --- | --- |
| State + backup, settings, plan, SSH keys | Pinned OpenTofu, provider, configuration/image copies | Projects and agent home |

The helper prints and remembers the state directory. New deployments use
`~/.local/state/collab-ai/operator/PROJECT/`; `COLLAB_OPERATOR_HOME` changes the base.
Existing Git worktree state is discovered without moving it. Ambiguous matches need
one `--state-dir` selection. Keep the entire state directory and selected image.

State writes go directly to the host over an authenticated private connection,
with atomic writes, backups and a deployment lock. Plans are bound to their inputs,
image and Incus server; changed inputs require a new plan. Use this helper consistently,
not concurrent native Terraform commands against the same state.

The operator has Incus administration access and runs only trusted provisioning
configuration. It is separate from dev/control; neither receives its admin socket
or state connection. No state directory is mounted into the containers. On Mac,
the helper uses Colima's SSH configuration for a temporary private state tunnel;
no agent keys are forwarded. Internet access is needed for pinned tools/providers.

## Interrupted or failed apply

The helper keeps host state and retains the stopped operator if an apply fails.
If the helper was forcibly killed, recovery stops the operator first. Do not delete
it manually: it may contain OpenTofu's emergency state file.

```sh
python3 scripts/sandbox-provision.py recover
python3 scripts/sandbox-provision.py plan
```

Recovery validates and saves emergency state if present, then removes the operator
and invalidates the old plan. Missing or inconsistent state stops recovery for inspection;
it never silently starts from empty state. Review the next plan before applying.

Operator code and checksums: [infra/operator](../infra/operator).
