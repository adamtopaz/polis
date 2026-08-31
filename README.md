# Polis

Polis is a small lifecycle kernel for fleets of autonomous agents on
Kubernetes. It keeps agent runtimes alive, gives them durable identity and
communication, and stays out of how they decide what to do.

Polis deliberately has no task model, workflow engine, project model, planner,
or model abstraction. An agent is only:

- a stable identity and charter;
- an arbitrary runtime command;
- a durable workspace;
- a durable mailbox and event journal;
- an `active`, `paused`, or `terminated` desired state.

The project is experimental. The current implementation favors a small,
understandable consistency model over premature scale machinery.

## The lifecycle model

An agent is a persistent logical identity, not an immortal Unix process. One
dedicated worker supervises one incarnation of one declared agent. If the
process, pod, or node disappears, the dedicated pod resumes the same identity,
workspace, mailbox, and runtime configuration from its PVC.

Desired state belongs to the operator:

| State | Meaning |
| --- | --- |
| `active` | The agent's dedicated worker maintains a running incarnation. |
| `paused` | No incarnation runs; identity, workspace, mailbox, and history remain. |
| `terminated` | The agent is permanently stopped and cannot be restarted or messaged. |

The reported phase is derived rather than commanded:

| Phase | Meaning |
| --- | --- |
| `ready` | Active and waiting for its dedicated worker to acquire it. |
| `running` | A worker owns a live incarnation lease. |
| `backoff` | Active, but waiting briefly after an incarnation exited. |
| `paused` | Desired state is paused. |
| `terminated` | Desired state is terminated. |

Applying an agent configuration makes a new agent active but does not send it a
first message. Its runtime can initialize and wait without invoking an LLM.
Work begins when an operator, another agent, or a scheduled self-message
supplies a trigger.

## Components

| Program | Audience | Purpose |
| --- | --- | --- |
| `polis` | Agent runtime | Capability CLI for the current agent's mailbox, journal, scheduling, and messaging. |
| `polisctl` | Operator | Applies, inspects, messages, pauses, resumes, and terminates agents. |
| `polis-controller` | Infrastructure | Stores state and serves the HTTP API. |
| `polis-worker` | Infrastructure | Is pinned to one agent and supervises its runtime process. |
| `polis-pi-agent` | Agent runtime | Persistent TypeScript runtime built directly on the Pi SDK. |
| `polis-demo-agent` | Tests | Deterministic persistent runtime distributed only in the demo worker image; it is not an AI agent. |

The controller stores agent records, mailboxes, scheduled messages, journals,
and fenced leases in one bbolt database. A worker requests only its configured
agent ID and executes that agent's runtime argument vector directly in its
mounted workspace. There is no intervening shell or workflow engine and no
multi-agent worker mode.

Each runtime receives:

```text
POLIS_URL
POLIS_AGENT_ID
POLIS_AGENT_TOKEN
POLIS_WORKSPACE
POLIS_CHARTER_PATH
```

`POLIS_AGENT_TOKEN` is both a short-lived API capability and an incarnation
fence. The worker renews it while the runtime is healthy. If renewal is lost,
the worker stops the process before the lease deadline so an old incarnation
cannot continue alongside its replacement.

## Operator workflow

`polisctl` is a normal Go executable. It reads the controller URL from
`POLIS_URL` (default `http://localhost:8080`) and the operator credential from
`POLIS_OPERATOR_TOKEN_FILE`, or from `POLIS_OPERATOR_TOKEN` for local
development. Every command emits JSON.

Apply a stable Pi agent configuration:

```console
polisctl agent apply \
  --id researcher \
  --charter 'Investigate your assigned subject autonomously and preserve useful findings.' \
  --runtime '["polis-pi-agent","--model","openai-codex/gpt-5.5","--thinking","high"]'
```

`agent apply` requires a stable ID. It creates the logical record if absent and
idempotently updates its charter and runtime if present without changing its
desired state. Kubernetes topology remains responsible for providing the
matching dedicated pod and PVC. Applying configuration intentionally does not
create a mailbox trigger.

Send the first trigger and inspect progress:

```console
polisctl message researcher '{"task":"Compare durable coordination strategies."}'
polisctl agent get researcher
polisctl events researcher
```

Pause, resume, or permanently terminate it:

```console
polisctl agent state researcher paused
polisctl agent state researcher active
polisctl agent state researcher terminated
```

The full operator surface is:

| Command | Effect |
| --- | --- |
| `polisctl agent apply --id ID --charter TEXT --runtime JSON` | Idempotently create or update a declaratively managed agent. |
| `polisctl agent list` | List all agents. |
| `polisctl agent get ID` | Return one agent's configuration, desired state, phase, and current lease information. |
| `polisctl agent state ID active\|paused\|terminated` | Change desired state. Termination is irreversible. |
| `polisctl message [--sender LABEL] ID JSON` | Append a durable message. The default sender is `operator`. |
| `polisctl events [ID]` | Return fleet-wide events or only one agent's lifecycle and journal events. |

Flags such as `--sender` and `--url` must precede positional arguments.

## Agent capabilities

`polis` is also a normal Go executable, but it is intended only for code running
inside an agent incarnation. It reads `POLIS_URL` and `POLIS_AGENT_TOKEN`
automatically and emits JSON.

| Command | Effect |
| --- | --- |
| `polis inspect` | Return the current agent's identity, charter, runtime, state, phase, and lease information. |
| `polis messages` | Return mailbox messages after the acknowledgement cursor. |
| `polis ack MESSAGE_ID` | Acknowledge that message and every earlier message. The cursor cannot move backward. |
| `polis send AGENT_ID JSON` | Send a durable message to another non-terminated agent. |
| `polis schedule DELAY JSON` | Schedule a durable message to self. Delays use Go duration syntax and must be at least one second. |
| `polis journal KIND JSON` | Append a durable, agent-authored event. |

Examples:

```console
polis inspect
polis messages
polis ack 42
polis send another-agent '{"question":"What did you find?"}'
polis schedule 30m '{"reason":"Review progress and continue."}'
polis journal decision.made '{"decision":"Continue the experiment."}'
```

Agents cannot pause, resume, or terminate themselves. Those are operator
decisions. A message sent while the recipient is busy remains queued for its
next turn. A scheduled message that becomes due while the agent is busy behaves
the same way.

## Pi SDK runtime

`polis-pi-agent` embeds the Pi SDK directly; it does not shell out to the `pi`
CLI. One runtime process and one Pi `AgentSession` remain alive for the whole
incarnation:

1. resume the most recent session from
   `<workspace>/.polis/pi-sessions`;
2. append the stable agent identity and charter to Pi's system prompt;
3. long-poll the durable mailbox without making LLM calls;
4. supply each queued batch to Pi as one trigger;
5. let Pi reason and use its read, Bash, edit, write, grep, find, and ls tools
   until the turn reaches a stop reason;
6. journal completion, acknowledge the batch, and return to mailbox waiting in
   the same process and session.

Messages arriving during a turn remain queued for the next turn. From Bash, the
model can use `polis` to communicate, journal decisions, or schedule a future
self-trigger.

Select a model with runtime arguments:

```console
polisctl agent apply --id agent-id \
  --charter 'Pursue this work carefully and autonomously.' \
  --runtime '["polis-pi-agent","--model","provider/model","--thinking","high"]'
```

`--thinking` accepts `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or
`max`, subject to the selected model. Runtime arguments override
`POLIS_PI_MODEL`, which accepts Pi's `provider/model[:thinking]` syntax. If no
model is specified, Pi restores the session model or selects an available one.

Provider API-key variables may be inherited by the runtime. Alternatively,
`POLIS_PI_AUTH_FILE` may point to an existing Pi `auth.json`; the file must be
writable because Pi can refresh OAuth credentials.

## Delivery and recovery

Mailbox delivery is at least once across failures. The Pi runtime records
`pi.turn.started` before invoking the model, but records `pi.turn.completed` and
advances the mailbox cursor only after the whole turn succeeds. If an
incarnation disappears mid-turn, its replacement receives the same
unacknowledged batch.

Tools may therefore have made partial durable changes before a retry. Agent
work should inspect existing state and make consequential operations idempotent
when practical.

An idle restart does not cause an LLM turn. The replacement incarnation resumes
the same workspace and most recent Pi session, then waits for a message. A brief
controller outage is retried by a running Pi runtime. If the outage lasts past
the lease deadline, the worker fences the runtime and a replacement resumes
after the controller returns.

Completion is journaled before mailbox acknowledgement. If the lease is lost
between those operations, the message remains available for retry. During a
Kubernetes controller handoff, a replacement waits for the old process to
release the bbolt file lock rather than entering a crash loop.

## Security boundaries

Polis currently uses three deliberately small bearer-token boundaries:

| Credential | Held by | Capability |
| --- | --- | --- |
| Operator token | `polisctl` and controller | Fleet administration and event inspection. |
| Worker token | Controller and dedicated workers | Acquire the worker's declared agent lease. |
| Incarnation token | One worker and its runtime | Act only as the leased agent while that lease is valid. |

The worker can consume its credential from a temporary file before starting
its agent runtime. Operator, worker, and supervisor identity variables are
removed from the child environment and replaced with the leased incarnation's
values. The controller image contains `polisctl`; both worker images
contain the agent-facing `polis` but deliberately omit `polisctl`. The
production Pi image contains no demo runtime code.

`/v1/agents` and `/v1/events` require the operator token.
`/v1/worker/acquire` requires the worker token. Heartbeats, exits, and
`/v1/self` requests require a valid incarnation token. `/healthz` is
unauthenticated.

## Build and development

Polis is developed and packaged as a Nix flake:

```console
nix develop
go test ./...
npm --prefix runtime/pi test
nix flake check
nix build .#container
nix build .#pi-container
nix build .#demo-container
```

The main flake outputs are:

| Output | Contents |
| --- | --- |
| `.#polis` | Agent capability CLI. |
| `.#polisctl` | Operator CLI. |
| `.#controller` | Controller process. |
| `.#worker` | Worker process. |
| `.#demo-agent` | Deterministic test runtime. |
| `.#pi-runtime` | Persistent Pi SDK runtime. |
| `.#container` | Controller image containing `polis-controller` and `polisctl`. |
| `.#pi-container` | Production `polis-pi` worker image containing only the Pi runtime. |
| `.#demo-container` | `polis-demo` worker image containing only the deterministic demo runtime. |

For a local process-level experiment, run the controller and worker in separate
terminals with distinct development-only credentials:

```console
POLIS_OPERATOR_TOKEN=dev-operator POLIS_WORKER_TOKEN=dev-worker \
  nix run .#controller -- --db ./polis.db
```

```console
POLIS_WORKER_TOKEN=dev-worker \
  nix run .#worker -- --agent researcher --workspace ./researcher-workspace
```

Then use `POLIS_OPERATOR_TOKEN=dev-operator nix run .#polisctl -- ...` from a
third terminal. Any configured runtime executable must be available in the
worker's environment. The companion local deployment repository provides the
tested k3s workflow and a worker image with the Pi runtime already installed.

The Pi and demo worker images are intentionally separate. Each dedicated worker
image must contain the runtime executable configured for its one agent. The
local k3s deployment uses the production Pi image.

## Deliberate limits

- The controller is one bbolt writer, not a highly available replicated
  service.
- Every declared agent has its own pod and private PVC, even while waiting
  without making LLM calls.
- Shared folders use ordinary Kubernetes PVCs and volume mounts. Cross-node
  sharing therefore depends on the cluster's storage semantics.
- There is no scheduler for projects or tasks beyond durable messages and agent
  autonomy.

These constraints keep the lifecycle and failure model obvious while real
usage establishes what needs to scale next.
