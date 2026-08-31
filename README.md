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

On Kubernetes, an `Agent` custom resource is the single source of truth for
identity, charter, runtime image and command, pod configuration, and private
volume claims. Runtime state and communication remain in Polis rather than in
the Kubernetes API.

The project is experimental. The current implementation favors a small,
understandable consistency model over premature scale machinery.

## The lifecycle model

An agent is a persistent logical identity, not an immortal Unix process. One
dedicated worker supervises one incarnation of one declared agent. If the
process, pod, or node disappears, the dedicated pod resumes the same identity,
workspace, and mailbox from durable state, and the same runtime configuration
from its `Agent` resource.

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
| `paused` | Desired state is paused. |
| `terminated` | Desired state is terminated. |

Creating an `Agent` resource makes a new agent active but does not send it a
first message. Its runtime can initialize and wait without invoking an LLM.
Work begins when an operator or an agent supplies a message.

## Components

| Program | Audience | Purpose |
| --- | --- | --- |
| `polis` | Agent runtime | Capability CLI for the current agent's mailbox, journal, and messaging. |
| `polisctl` | Operator | Applies, inspects, messages, pauses, resumes, and terminates agents. |
| `polis-controller` | Infrastructure | Runs the Kubebuilder controller manager and reconciles Agent pods and PVCs. |
| `polis-mailbox` | Infrastructure | Serves messaging and coordination from its own durable database. |
| `polis-worker` | Infrastructure | Is pinned to one agent and supervises its runtime process. |
| `polis-pi-agent` | Agent runtime | Persistent TypeScript runtime built directly on the Pi SDK. |

The mailbox stores dynamic agent records, messages, journals, and fenced leases
in one bbolt database. The `Agent` custom resource remains the only desired
configuration: the controller projects its charter and runtime argument vector
into the dedicated worker pod. The mailbox never stores them. There is no
intervening shell or workflow engine and no multi-agent worker mode.

An authenticated worker registers its stable identity in the mailbox on its
first lease acquisition. Consequently, `kubectl get agents.polis.dev` lists
declared topology, while `polisctl agent list` lists identities that have
connected to the mailbox at least once.

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

## Kubernetes workflow

Polis is a Kubebuilder project. Its typed API lives in `api/v1alpha1`, the
reconciler lives in `internal/controller`, and generated CRD and RBAC manifests
live under `config/`. The controller-runtime cache watches `Agent` resources,
owned Deployments, and labeled retained PVCs.

Declare an agent with ordinary Kubernetes YAML:

```yaml
apiVersion: polis.dev/v1alpha1
kind: Agent
metadata:
  name: researcher
  namespace: polis
spec:
  charter: Investigate useful subjects autonomously and preserve findings.
  runtime:
    image: ghcr.io/adamtopaz/polis-pi:main
    command:
      - /bin/polis-pi-agent
      - --model
      - openai-codex/gpt-5.5
      - --thinking
      - high
      - --compaction-reserve-tokens
      - "32768"
      - --compaction-keep-recent-tokens
      - "24000"
  volumeClaimTemplates:
    - metadata:
        name: workspace
      spec:
        accessModes: [ReadWriteOnce]
        resources:
          requests:
            storage: 5Gi
```

Apply and inspect it through Kubernetes:

```console
kubectl apply -f researcher.yaml
kubectl -n polis get agents.polis.dev,deployments,pods,pvc
kubectl -n polis describe agents.polis.dev researcher
```

The reconciler idempotently creates a retained `researcher-workspace` PVC and
exactly one `Recreate` Deployment with one agent container. It injects the
worker command, charter, runtime command, mailbox connection, private workspace
mount, and credential boundary. `podTemplate` preserves native pod settings
such as resources, affinity, tolerations, init containers, shared PVC volumes,
and volume mounts.

By default, the generated agent and credential-init containers run as numeric
UID:GID `10001:10001`, with supplemental filesystem group `10001`. They cannot
escalate privileges, have all Linux capabilities dropped, use the runtime
default seccomp profile, and receive a read-only root filesystem. Writable
state is limited to mounted volumes such as `/workspace` and `/tmp`.

Private PVCs intentionally have no owner reference. Deleting an `Agent`
garbage-collects its Deployment but retains its private claims and Polis
history. Reapplying the same name reconnects that state unless the logical
agent was permanently terminated. Shared folders are ordinary separately
declared PVCs referenced from `spec.podTemplate`; Polis adds no storage
abstraction.

The product installation is generated under `config/default`:

```console
kubectl apply --server-side -k config/default
```

It contains the CRD, controller Deployment and ServiceAccount, generated RBAC,
mailbox Deployment and Service, and mailbox PVC. Environment-specific Agent
objects, credentials, storage classes, and shared claims belong in a deployment
repository or overlay.

## Operator workflow

`polisctl` is a normal Go executable. It reads the mailbox URL from
`POLIS_URL` (default `http://localhost:8080`) and the operator credential from
`POLIS_OPERATOR_TOKEN_FILE`, or from `POLIS_OPERATOR_TOKEN` for local
development. Every command emits JSON. Agent configuration is intentionally
absent: `kubectl` and the `Agent` CR are its single declarative path.

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
| `polisctl agent list` | List all agents. |
| `polisctl agent get ID` | Return one agent's dynamic state, phase, and current lease information. |
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
| `polis inspect` | Return the current agent's identity, state, phase, and lease information. |
| `polis messages` | Return mailbox messages after the acknowledgement cursor. |
| `polis ack MESSAGE_ID` | Acknowledge that message and every earlier message. The cursor cannot move backward. |
| `polis send AGENT_ID JSON` | Send a durable message to any non-terminated agent, including yourself. |
| `polis journal KIND JSON` | Append a durable, agent-authored event. |

Examples:

```console
polis inspect
polis messages
polis ack 42
polis send another-agent '{"question":"What did you find?"}'
polis send "$POLIS_AGENT_ID" '{"reason":"Continue in another turn."}'
polis journal decision.made '{"decision":"Continue the experiment."}'
```

Agents cannot pause, resume, or terminate themselves. Those are operator
decisions. A message sent while the recipient is busy remains queued for its
next turn. Self-messages and messages to other agents are the same kind of
durable mailbox message.

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
model can use `polis` to communicate or journal decisions. Polis does not
schedule messages. An agent that wants a later trigger can use Bash to arrange
for `polis send "$POLIS_AGENT_ID" JSON` to run later; it may target another agent
instead. The agent owns the choice and durability of that mechanism.

Select a model with the `Agent` runtime command:

```yaml
spec:
  runtime:
    image: ghcr.io/adamtopaz/polis-pi:main
    command:
      - /bin/polis-pi-agent
      - --model
      - provider/model
      - --thinking
      - high
      - --compaction-reserve-tokens
      - "32768"
      - --compaction-keep-recent-tokens
      - "24000"
```

`--thinking` accepts `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or
`max`, subject to the selected model. Runtime arguments override
`POLIS_PI_MODEL`, which accepts Pi's `provider/model[:thinking]` syntax. If no
model is specified, Pi restores the session model or selects an available one.

Automatic context compaction remains enabled. `--compaction-reserve-tokens`
controls how much of the model's context window Pi reserves before triggering
compaction; Pi's default is `16384`. `--compaction-keep-recent-tokens` controls
how much recent conversation Pi retains verbatim beside the summary; Pi's
default is `20000`. Both flags require positive integers. When omitted, Polis
preserves Pi's configured values or defaults. Compaction entries are stored in
the durable Pi session on the agent's workspace PVC.

Provider API-key variables may be inherited by the runtime. Alternatively,
`POLIS_PI_AUTH_FILE` may point to an existing Pi `auth.json`; the runtime copies
it into private temporary storage so Pi can refresh OAuth credentials without
mutating the source.

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
the same workspace and most recent Pi session, then waits for a message. A
controller outage does not interrupt existing agents or messaging. A mailbox
outage is retried by a running Pi runtime; if it lasts past the lease deadline,
the worker fences the runtime and reacquires after the mailbox returns.

Completion is journaled before mailbox acknowledgement. If the lease is lost
between those operations, the message remains available for retry. During a
mailbox handoff, a replacement waits for the old process to release the bbolt
file lock rather than entering a crash loop.

## Security boundaries

Polis currently uses three deliberately small bearer-token boundaries:

| Credential | Held by | Capability |
| --- | --- | --- |
| Operator token | `polisctl` and mailbox | Fleet administration and event inspection. |
| Worker token | Mailbox and dedicated workers | Acquire the worker's declared agent lease. |
| Incarnation token | One worker and its runtime | Act only as the leased agent while that lease is valid. |

The worker can consume its credential from a temporary file before starting
its agent runtime. Operator, worker, and supervisor identity variables are
removed from the child environment and replaced with the leased incarnation's
values. The infrastructure image contains `polisctl`; the worker image contains
the agent-facing `polis` but deliberately omits `polisctl`.

`/v1/agents` and `/v1/events` require the operator token.
`/v1/worker/acquire` requires the worker token. Heartbeats, exits, and
`/v1/self` requests require a valid incarnation token. `/healthz` is
unauthenticated.

## Build and development

Polis is developed and packaged as a Nix flake. The dev shell includes the
pinned Kubebuilder CLI, Kubernetes tools, Go, and Node:

```console
nix develop
make manifests
make generate
make test
npm --prefix runtime/pi test
nix flake check
nix build .#container
nix build .#pi-container
```

The main flake outputs are:

| Output | Contents |
| --- | --- |
| `.#polis` | Agent capability CLI. |
| `.#polisctl` | Operator CLI. |
| `.#controller` | Controller process. |
| `.#mailbox` | Mailbox process. |
| `.#worker` | Worker process. |
| `.#pi-runtime` | Persistent Pi SDK runtime. |
| `.#manifests` | Rendered Kubebuilder/Kustomize installation bundle. |
| `.#container` | Infrastructure image containing `polis-controller`, `polis-mailbox`, and `polisctl`. |
| `.#pi-container` | Production `polis-pi` worker image containing only the Pi runtime. |

`make manifests` regenerates the CRD and RBAC from Kubebuilder markers;
`make generate` regenerates DeepCopy methods. Generated files are committed so
deployment consumers do not need the Go toolchain. The companion local
deployment repository pins this repository as a flake input and provides the
tested k3s overlay and live smoke tests.

Each dedicated worker image must contain the runtime executable configured for
its one agent. The local k3s deployment uses the production Pi image.

## Deliberate limits

- The mailbox is one bbolt writer, not a highly available replicated service.
- Every declared agent has its own pod and private PVC, even while waiting
  without making LLM calls.
- Shared folders use ordinary Kubernetes PVCs and volume mounts. Cross-node
  sharing therefore depends on the cluster's storage semantics.
- There is no Polis scheduler. Agents can use Bash to arrange future invocations
  of `polis send` when they need them.

These constraints keep the lifecycle and failure model obvious while real
usage establishes what needs to scale next.
