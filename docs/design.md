# Design notes

## Invariants

These rules are the small core of Polis:

1. Durable intent is separate from disposable compute. Projects, agents, memory, tasks, leases,
   and events live in the controller database; runner pods hold no authoritative state.
2. One logical agent has at most one live lease. That keeps its memory updates sequential without
   distributed locks in executors.
3. Every lease has a random fencing token and an expiry. Only its owner can heartbeat or finish
   it, and a stale worker cannot overwrite a task after the lease expires.
4. State transitions and their events commit in the same SQLite transaction.
5. Execution is at least once. Leases recover abandoned work, but Polis does not claim exactly-once
   external side effects.
6. Paused projects and agents receive no new leases. Already leased work keeps running unless its
   task is explicitly cancelled.

## Why logical agents are not pods

An agent is identity and continuity, not a process. Binding each agent to a long-lived pod would
make thousands of mostly idle agents consume cluster scheduling and memory resources, while still
requiring external persistence for node failures. Polis instead schedules durable tasks onto a
shared runner pool. A runner crash only loses a lease; the agent remains.

## Current transaction boundary

SQLite uses WAL mode. Acquiring work briefly takes an immediate write transaction, reclaims expired
leases, selects the highest-priority runnable task, records its fencing token, and commits. This
serializes lease grants and makes the per-agent concurrency invariant straightforward.

This boundary can later move behind a storage interface or be translated to PostgreSQL using
`SELECT ... FOR UPDATE SKIP LOCKED`. It should move only after contention or availability needs
justify the operational cost.

## Executor seam

The runner's subprocess protocol is the main extension seam. A model-specific executor can:

- consume the task, instructions, and memory supplied on stdin;
- use model APIs, repositories, browsers, or other tools;
- enqueue follow-up tasks through the controller API;
- return updated compact memory with its result.

This keeps model SDK churn, credentials, sandboxing, and tool policy out of the scheduler. In a
production deployment, executors that run untrusted generated code should be isolated more strongly
than a subprocess in the runner container.

## Scaling path, in order

1. Scale runner replicas and measure queue latency.
2. Add executor capability labels only when heterogeneous work needs them.
3. Move state to PostgreSQL when the single controller or SQLite write lock becomes a measured
   bottleneck, or when controller high availability becomes necessary.
4. Partition task acquisition only if the PostgreSQL implementation itself becomes a bottleneck.

The system should not introduce a broker, custom Kubernetes operator, or distributed workflow
engine merely because the eventual fleet might be large.
