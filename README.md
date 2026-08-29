# Polis

Polis is an experimental coordination kernel for fleets of persistent autonomous agents.
It deliberately starts small: durable state, logical agent identities, projects, queued work,
and a lease protocol for disposable workers.

Polis is not an agent framework. An executor can be Codex, another model-driven program, or
an ordinary command. Polis keeps that choice outside the coordination layer.

## The model

```text
operator / automation
         |
         v
  Polis controller ---- SQLite state + ordered event log
         ^
         | lease / heartbeat / finish
         |
  many runner pods ---- one configured executor command per task
```

- A **project** groups related work and can be paused independently.
- An **agent** is a durable identity: instructions and JSON memory survive runner and node
  restarts. One agent executes at most one task at a time.
- A **task** is a durable unit of work for one project and one agent.
- A **runner** is disposable compute. It leases a task, keeps the lease alive, invokes an
  executor, and reports the outcome.
- An **event** is an ordered record of each meaningful state transition.

The controller is a single process using SQLite in WAL mode. This is intentional for the first
experiment: it gives us one deployable binary package, no service dependencies, and real crash
recovery. Runner capacity scales horizontally, and thousands of idle logical agents consume no
pods. The controller is neither highly available nor intended for unbounded write throughput;
PostgreSQL is the obvious storage replacement if measurement proves it necessary.

## Development on NixOS

Enter the development environment and run the checks:

```console
nix develop
make test
nix flake check
```

Run a controller with local durable state:

```console
POLIS_DB_PATH=./polis.db nix run .#controller
```

In another shell, create a project, an agent, and work:

```console
nix run .#cli -- create-project research --id research
nix run .#cli -- create-agent scout --id scout \
  --instructions 'Investigate assigned questions and retain useful context.'
nix run .#cli -- create-task first-question --id first-question \
  --project research --agent scout --input '{"question":"Why is the sky blue?"}'
```

Start a development runner. Its default executor is a deterministic echo program that exercises
the full lifecycle and persistent-memory path:

```console
nix run .#runner
nix run .#cli -- tasks --project research
nix run .#cli -- events
```

Build the OCI-compatible image archive with:

```console
nix build .#container
```

Pushes to `main` run the same checks and publish the image as both
`ghcr.io/adamtopaz/polis:main` and `ghcr.io/adamtopaz/polis:<commit-sha>`. Deployments can use
the moving `main` tag for experimentation or pin the immutable commit tag.

## Executor protocol

`POLIS_EXECUTOR_COMMAND` is either a JSON array or a shell-style command string. A runner starts
the command once per task and writes one JSON object to its standard input:

```json
{
  "project": {"id": "...", "name": "..."},
  "agent": {"id": "...", "instructions": "...", "memory": {}},
  "task": {"id": "...", "title": "...", "input": {}, "attempt": 1}
}
```

On exit status zero, standard output is parsed as JSON and becomes the task output. If that JSON
contains a top-level `memory` value, it atomically replaces the agent's durable memory when the
task completes. A non-zero exit schedules a retry up to `max_attempts`; stderr is recorded as the
failure reason. The runner heartbeats while the command is active.

Delivery is **at least once**. A worker can perform an external side effect and crash before it
reports completion, so real executors must make externally visible actions idempotent.

## Control API

The JSON HTTP API currently exposes:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz`, `/v1/status` | Health and fleet counts |
| `POST`, `GET` | `/v1/projects` | Create and list projects |
| `PATCH` | `/v1/projects/{id}` | Set `active`, `paused`, or `archived` |
| `POST`, `GET` | `/v1/agents` | Create and list durable agents |
| `PATCH` | `/v1/agents/{id}` | Set `active`, `paused`, or `retired` |
| `POST`, `GET` | `/v1/tasks` | Queue and list tasks |
| `POST` | `/v1/tasks/{id}/cancel` | Cancel queued or leased work |
| `GET` | `/v1/events` | Read the ordered event stream |
| `POST` | `/v1/leases` | Atomically acquire runnable work |
| `POST` | `/v1/tasks/{id}/{heartbeat,complete,fail}` | Drive a lease lifecycle |

There is currently no authentication. Bind the controller only inside a trusted network.

## What is intentionally absent

- no workflow DSL or DAG engine;
- no one-pod-per-agent model;
- no built-in model provider or prompt abstraction;
- no message broker, cache, operator, CRDs, or service mesh;
- no multi-controller high availability yet;
- no UI, RBAC, quotas, or per-project isolation yet.

The next useful increment should come from running a real executor and observing the pressure it
puts on this kernel, rather than predicting a large platform in advance. See
[`docs/design.md`](docs/design.md) for invariants and extension seams.
