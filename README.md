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

`ready`, `running`, and crash `backoff` are derived from the desired state and
incarnation lease. There are no tasks, workflows, project records, planners, or
model abstractions in Polis.

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

Every runtime can use the agent API without a language-specific SDK:

```console
polis self inspect
polis self messages
polis self ack 42
polis self send another-agent '{"hello":"there"}'
polis self schedule 30m '{"reason":"continue"}'
polis self spawn --charter 'Investigate independently.' --runtime '["agent-runtime"]'
polis self journal decision.made '{"decision":"continue"}'
```

These commands read `POLIS_URL` and `POLIS_AGENT_TOKEN` automatically and emit
JSON. Agents cannot pause or terminate themselves; the operator owns desired
state. Scheduling creates a durable future mailbox message.

The lease token is both the incarnation fence and the agent's capability for the
self API. A worker renews it while the process runs. If renewal is lost, the
worker terminates the process before the lease deadline. A crash leads to a new
incarnation using the same identity and workspace after a short backoff. An
agent can wait for messages, schedule a future message to itself, message or
spawn another agent, and append to its journal.

Operator routes use a separate bearer token. The controller and control CLI read
it from `POLIS_OPERATOR_TOKEN_FILE`, or from `POLIS_OPERATOR_TOKEN` for local
development. Workers and agent runtimes must never receive this token.

The included `demo-agent` is a deterministic persistent smoke-test runtime, not
an AI agent.
Real agents are independent executables or runner images and remain free to pick
their own models, tools, memory formats, repositories, and decision loops.

## Pi runtime

`polis-pi-agent` is the first real runtime. It embeds the Pi SDK directly; it
does not invoke the `pi` CLI or put another workflow engine between Pi and
Polis. A runtime process and one Pi `AgentSession` stay alive for the whole
incarnation:

1. resumes the agent's most recent Pi session from
   `<workspace>/.polis/pi-sessions`;
2. adds the stable identity and charter to Pi's system prompt;
3. long-polls the durable mailbox without making any LLM call;
4. on a message, supplies the queued batch to Pi and lets it take one complete
   autonomous turn with its read, bash, edit, write, grep, find, and ls tools;
5. acknowledges the batch after success, journals completion, and waits again
   in the same session.

Messages received during a turn remain queued for the next turn. The agent can
schedule its own future trigger with `polis self schedule`. A model can be
selected with `POLIS_PI_MODEL` using Pi's `provider/model[:thinking]` syntax. Pi
otherwise restores the session model or selects an available model. Provider
API-key environment variables are inherited by the runtime. An existing Pi
`auth.json` can instead be supplied with `POLIS_PI_AUTH_FILE`; that file must be
writable because Pi may refresh OAuth credentials.

Create a Pi-backed agent by making its arbitrary runtime command the custom
runner:

```console
polis agent create \
  --charter 'Explore this workspace and pursue useful work autonomously.' \
  --runtime '["polis-pi-agent"]'
```

The controller image remains `polis`. Workers that execute this runtime use the
separate `polis-pi` image so the controller stays small.

## Development

This repository is a Nix flake:

```console
nix develop
go test ./...
npm --prefix runtime/pi test
nix flake check
nix build .#container
nix build .#pi-container
```

Run a local controller and worker:

```console
POLIS_OPERATOR_TOKEN=local-development-only nix run -- server --db ./polis.db
nix run -- worker --workspace-root ./workspaces
```

Create the smoke runtime:

```console
POLIS_OPERATOR_TOKEN=local-development-only nix run -- agent create \
  --id example \
  --charter 'Exercise the autonomous-agent lifecycle.' \
  --runtime '["polis","demo-agent"]'
```

`/v1/agents` and `/v1/events` require the operator token. `/v1/worker` and
`/v1/self` use incarnation lease tokens, while `/healthz` is unauthenticated.
This is intentionally one simple authorization boundary rather than a roles or
policy system.

## Deliberate limits

The initial controller is a single bbolt writer, so it is not highly available.
That keeps the consistency model obvious while we learn. Every active persistent
agent occupies one worker slot, so active capacity scales by adding workers or
slots. Paused agents consume no slot. Moving state to a replicated database
should happen only when measurements make that necessary.
