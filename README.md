# Polis

Polis is a small lifecycle kernel for fleets of autonomous agents on Kubernetes.
It manages whether an agent incarnation exists. It does not manage the agent's
work.

An agent is only:

- a stable identity and charter;
- an arbitrary runtime command;
- a durable workspace;
- a durable mailbox and journal;
- an `active`, `paused`, or `terminated` desired state.

`ready`, `running`, and `sleeping` are derived from the desired state, wake time,
and incarnation lease. There are no tasks, workflows, project records, planners,
or model abstractions in Polis.

## How it runs

The controller stores agent records, mailboxes, events, and fenced leases in one
bbolt file. Workers lease ready agents and run their configured executable in
`<workspace-root>/<agent-id>`. The executable receives:

```text
POLIS_URL
POLIS_AGENT_ID
POLIS_AGENT_TOKEN
POLIS_WORKSPACE
POLIS_CHARTER_PATH
```

The lease token is both the incarnation fence and the agent's capability for the
self API. A worker renews it while the process runs. If renewal is lost, the
worker terminates the process before the lease deadline. A crash leads to a new
incarnation using the same identity and workspace. An agent can sleep, wake from
a message, message or spawn another agent, append to its journal, or terminate
itself.

The included `demo-agent` is a deterministic smoke-test runtime, not an AI agent.
Real agents are independent executables or runner images and remain free to pick
their own models, tools, memory formats, repositories, and decision loops.

## Development

This repository is a Nix flake:

```console
nix develop
go test ./...
nix flake check
nix build .#container
```

Run a local controller and worker:

```console
nix run -- server --db ./polis.db
nix run -- worker --workspace-root ./workspaces
```

Create the smoke runtime:

```console
nix run -- agent create \
  --id example \
  --charter 'Exercise the autonomous-agent lifecycle.' \
  --runtime '["polis","demo-agent"]'
```

The control API is intentionally unauthenticated in this first experimental
kernel; bind it only to a trusted network. Incarnation APIs require the current
lease token. Workspace isolation and control-plane authentication belong at the
Kubernetes boundary before exposing Polis outside that network.

## Deliberate limits

The initial controller is a single bbolt writer, so it is not highly available.
That keeps the consistency model obvious while we learn. It can coordinate many
dormant agents, while active capacity scales by adding workers or worker slots.
Moving state to a replicated database should happen only when measurements make
that necessary.
