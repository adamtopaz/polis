from __future__ import annotations

import argparse
import json
import logging
import os
import re
import signal
import sqlite3
import threading
import time
import types
import uuid
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import parse_qs, urlparse

LOG = logging.getLogger("polis.controller")
MAX_BODY_BYTES = 1_048_576


class PolisError(Exception):
    status = HTTPStatus.BAD_REQUEST
    code = "bad_request"


class NotFound(PolisError):
    status = HTTPStatus.NOT_FOUND
    code = "not_found"


class Conflict(PolisError):
    status = HTTPStatus.CONFLICT
    code = "conflict"


class Invalid(PolisError):
    status = HTTPStatus.UNPROCESSABLE_ENTITY
    code = "invalid"


class ClosingConnection(sqlite3.Connection):
    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        traceback: types.TracebackType | None,
    ) -> bool | None:
        try:
            return super().__exit__(exc_type, exc_value, traceback)
        finally:
            self.close()


def _json(value: Any) -> str:
    return json.dumps(value, separators=(",", ":"), sort_keys=True)


def _decode(value: str | None) -> Any:
    return None if value is None else json.loads(value)


def _row(row: sqlite3.Row | None) -> dict[str, Any] | None:
    if row is None:
        return None
    result = dict(row)
    for key in ("input_json", "output_json", "memory_json", "payload_json"):
        if key in result:
            result[key.removesuffix("_json")] = _decode(result.pop(key))
    return result


def _require_text(body: dict[str, Any], name: str) -> str:
    value = body.get(name)
    if not isinstance(value, str) or not value.strip():
        raise Invalid(f"{name} must be a non-empty string")
    return value.strip()


class Store:
    """SQLite-backed state machine. All task transitions happen in transactions."""

    def __init__(self, path: str):
        self.path = path
        parent = os.path.dirname(os.path.abspath(path))
        os.makedirs(parent, exist_ok=True)
        self._initialize()

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(
            self.path,
            timeout=10,
            isolation_level=None,
            check_same_thread=False,
            factory=ClosingConnection,
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys = ON")
        connection.execute("PRAGMA busy_timeout = 10000")
        return connection

    def _initialize(self) -> None:
        with self._connect() as connection:
            connection.executescript(
                """
                PRAGMA journal_mode = WAL;
                PRAGMA synchronous = NORMAL;

                CREATE TABLE IF NOT EXISTS projects (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL UNIQUE,
                    description TEXT NOT NULL DEFAULT '',
                    status TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'paused', 'archived')),
                    created_at REAL NOT NULL,
                    updated_at REAL NOT NULL
                );

                CREATE TABLE IF NOT EXISTS agents (
                    id TEXT PRIMARY KEY,
                    name TEXT NOT NULL UNIQUE,
                    instructions TEXT NOT NULL,
                    status TEXT NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active', 'paused', 'retired')),
                    memory_json TEXT NOT NULL DEFAULT '{}',
                    created_at REAL NOT NULL,
                    updated_at REAL NOT NULL
                );

                CREATE TABLE IF NOT EXISTS tasks (
                    id TEXT PRIMARY KEY,
                    project_id TEXT NOT NULL REFERENCES projects(id),
                    agent_id TEXT NOT NULL REFERENCES agents(id),
                    title TEXT NOT NULL,
                    input_json TEXT NOT NULL,
                    status TEXT NOT NULL DEFAULT 'queued'
                        CHECK (status IN ('queued', 'leased', 'succeeded', 'failed', 'cancelled')),
                    priority INTEGER NOT NULL DEFAULT 0,
                    attempt INTEGER NOT NULL DEFAULT 0,
                    max_attempts INTEGER NOT NULL DEFAULT 3,
                    available_at REAL NOT NULL,
                    lease_owner TEXT,
                    lease_token TEXT,
                    lease_expires_at REAL,
                    output_json TEXT,
                    last_error TEXT,
                    created_at REAL NOT NULL,
                    updated_at REAL NOT NULL,
                    completed_at REAL
                );

                CREATE INDEX IF NOT EXISTS tasks_ready
                    ON tasks(status, available_at, priority DESC, created_at);
                CREATE INDEX IF NOT EXISTS tasks_agent
                    ON tasks(agent_id, status, lease_expires_at);
                CREATE INDEX IF NOT EXISTS tasks_project
                    ON tasks(project_id, created_at);

                CREATE TABLE IF NOT EXISTS events (
                    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
                    occurred_at REAL NOT NULL,
                    type TEXT NOT NULL,
                    entity_kind TEXT NOT NULL,
                    entity_id TEXT NOT NULL,
                    payload_json TEXT NOT NULL DEFAULT '{}'
                );

                CREATE INDEX IF NOT EXISTS events_entity
                    ON events(entity_kind, entity_id, sequence);
                """
            )

    @staticmethod
    def _event(
        connection: sqlite3.Connection,
        event_type: str,
        entity_kind: str,
        entity_id: str,
        payload: Any | None = None,
        now: float | None = None,
    ) -> None:
        connection.execute(
            "INSERT INTO events(occurred_at, type, entity_kind, entity_id, payload_json) "
            "VALUES (?, ?, ?, ?, ?)",
            (
                now or time.time(),
                event_type,
                entity_kind,
                entity_id,
                _json(payload or {}),
            ),
        )

    def healthy(self) -> bool:
        with self._connect() as connection:
            return connection.execute("SELECT 1").fetchone()[0] == 1

    def create_project(self, body: dict[str, Any]) -> dict[str, Any]:
        project_id = str(body.get("id") or uuid.uuid4())
        name = _require_text(body, "name")
        description = body.get("description", "")
        if not isinstance(description, str):
            raise Invalid("description must be a string")
        now = time.time()
        try:
            with self._connect() as connection:
                connection.execute("BEGIN IMMEDIATE")
                connection.execute(
                    "INSERT INTO projects(id, name, description, created_at, updated_at) "
                    "VALUES (?, ?, ?, ?, ?)",
                    (project_id, name, description, now, now),
                )
                self._event(
                    connection, "project.created", "project", project_id, now=now
                )
                connection.commit()
        except sqlite3.IntegrityError as error:
            raise Conflict("a project with that id or name already exists") from error
        return self.get_project(project_id)

    def create_agent(self, body: dict[str, Any]) -> dict[str, Any]:
        agent_id = str(body.get("id") or uuid.uuid4())
        name = _require_text(body, "name")
        instructions = _require_text(body, "instructions")
        memory = body.get("memory", {})
        now = time.time()
        try:
            with self._connect() as connection:
                connection.execute("BEGIN IMMEDIATE")
                connection.execute(
                    "INSERT INTO agents(id, name, instructions, memory_json, created_at, updated_at) "
                    "VALUES (?, ?, ?, ?, ?, ?)",
                    (agent_id, name, instructions, _json(memory), now, now),
                )
                self._event(connection, "agent.created", "agent", agent_id, now=now)
                connection.commit()
        except sqlite3.IntegrityError as error:
            raise Conflict("an agent with that id or name already exists") from error
        return self.get_agent(agent_id)

    def create_task(self, body: dict[str, Any]) -> dict[str, Any]:
        task_id = str(body.get("id") or uuid.uuid4())
        project_id = _require_text(body, "project_id")
        agent_id = _require_text(body, "agent_id")
        title = _require_text(body, "title")
        if "input" not in body:
            raise Invalid("input is required")
        try:
            priority = int(body.get("priority", 0))
            max_attempts = int(body.get("max_attempts", 3))
            available_at = float(body.get("available_at", time.time()))
        except (TypeError, ValueError) as error:
            raise Invalid(
                "priority, max_attempts, and available_at must be numbers"
            ) from error
        if not 1 <= max_attempts <= 100:
            raise Invalid("max_attempts must be between 1 and 100")
        now = time.time()
        try:
            with self._connect() as connection:
                connection.execute("BEGIN IMMEDIATE")
                project = connection.execute(
                    "SELECT status FROM projects WHERE id = ?", (project_id,)
                ).fetchone()
                agent = connection.execute(
                    "SELECT status FROM agents WHERE id = ?", (agent_id,)
                ).fetchone()
                if project is None:
                    raise NotFound("project not found")
                if agent is None:
                    raise NotFound("agent not found")
                if project["status"] != "active":
                    raise Conflict("project is not active")
                if agent["status"] != "active":
                    raise Conflict("agent is not active")
                connection.execute(
                    """
                    INSERT INTO tasks(
                        id, project_id, agent_id, title, input_json, priority,
                        max_attempts, available_at, created_at, updated_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        task_id,
                        project_id,
                        agent_id,
                        title,
                        _json(body["input"]),
                        priority,
                        max_attempts,
                        available_at,
                        now,
                        now,
                    ),
                )
                self._event(
                    connection,
                    "task.created",
                    "task",
                    task_id,
                    {"project_id": project_id, "agent_id": agent_id},
                    now,
                )
                connection.commit()
        except sqlite3.IntegrityError as error:
            raise Conflict("a task with that id already exists") from error
        return self.get_task(task_id)

    def get_project(self, project_id: str) -> dict[str, Any]:
        with self._connect() as connection:
            item = _row(
                connection.execute(
                    "SELECT * FROM projects WHERE id = ?", (project_id,)
                ).fetchone()
            )
        if item is None:
            raise NotFound("project not found")
        return item

    def get_agent(self, agent_id: str) -> dict[str, Any]:
        with self._connect() as connection:
            item = _row(
                connection.execute(
                    "SELECT * FROM agents WHERE id = ?", (agent_id,)
                ).fetchone()
            )
        if item is None:
            raise NotFound("agent not found")
        return item

    def get_task(self, task_id: str) -> dict[str, Any]:
        with self._connect() as connection:
            item = _row(
                connection.execute(
                    "SELECT * FROM tasks WHERE id = ?", (task_id,)
                ).fetchone()
            )
        if item is None:
            raise NotFound("task not found")
        return item

    def update_project(self, project_id: str, body: dict[str, Any]) -> dict[str, Any]:
        status = body.get("status")
        if status not in {"active", "paused", "archived"}:
            raise Invalid("project status must be active, paused, or archived")
        now = time.time()
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            result = connection.execute(
                "UPDATE projects SET status = ?, updated_at = ? WHERE id = ?",
                (status, now, project_id),
            )
            if result.rowcount == 0:
                raise NotFound("project not found")
            self._event(
                connection,
                "project.status_changed",
                "project",
                project_id,
                {"status": status},
                now,
            )
            connection.commit()
        return self.get_project(project_id)

    def update_agent(self, agent_id: str, body: dict[str, Any]) -> dict[str, Any]:
        status = body.get("status")
        if status not in {"active", "paused", "retired"}:
            raise Invalid("agent status must be active, paused, or retired")
        now = time.time()
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            result = connection.execute(
                "UPDATE agents SET status = ?, updated_at = ? WHERE id = ?",
                (status, now, agent_id),
            )
            if result.rowcount == 0:
                raise NotFound("agent not found")
            self._event(
                connection,
                "agent.status_changed",
                "agent",
                agent_id,
                {"status": status},
                now,
            )
            connection.commit()
        return self.get_agent(agent_id)

    def cancel_task(self, task_id: str) -> dict[str, Any]:
        now = time.time()
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            task = connection.execute(
                "SELECT status FROM tasks WHERE id = ?", (task_id,)
            ).fetchone()
            if task is None:
                raise NotFound("task not found")
            if task["status"] in {"succeeded", "failed", "cancelled"}:
                raise Conflict(f"cannot cancel a {task['status']} task")
            connection.execute(
                """
                UPDATE tasks SET status = 'cancelled', lease_owner = NULL,
                    lease_token = NULL, lease_expires_at = NULL,
                    completed_at = ?, updated_at = ? WHERE id = ?
                """,
                (now, now, task_id),
            )
            self._event(connection, "task.cancelled", "task", task_id, now=now)
            connection.commit()
        return self.get_task(task_id)

    def list_projects(self, limit: int = 100) -> list[dict[str, Any]]:
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM projects ORDER BY created_at DESC LIMIT ?", (limit,)
            ).fetchall()
        return [_row(row) for row in rows]

    def list_agents(self, limit: int = 100) -> list[dict[str, Any]]:
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM agents ORDER BY created_at DESC LIMIT ?", (limit,)
            ).fetchall()
        return [_row(row) for row in rows]

    def list_tasks(
        self, filters: dict[str, str], limit: int = 100
    ) -> list[dict[str, Any]]:
        clauses: list[str] = []
        values: list[Any] = []
        for name in ("status", "project_id", "agent_id"):
            value = filters.get(name)
            if value:
                clauses.append(f"{name} = ?")
                values.append(value)
        where = " WHERE " + " AND ".join(clauses) if clauses else ""
        values.append(limit)
        with self._connect() as connection:
            rows = connection.execute(
                f"SELECT * FROM tasks{where} ORDER BY created_at DESC LIMIT ?", values
            ).fetchall()
        return [_row(row) for row in rows]

    def list_events(self, after: int = 0, limit: int = 100) -> list[dict[str, Any]]:
        with self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM events WHERE sequence > ? ORDER BY sequence LIMIT ?",
                (after, limit),
            ).fetchall()
        return [_row(row) for row in rows]

    def _reclaim_expired(self, connection: sqlite3.Connection, now: float) -> None:
        expired = connection.execute(
            "SELECT id, attempt, max_attempts, lease_owner FROM tasks "
            "WHERE status = 'leased' AND lease_expires_at <= ?",
            (now,),
        ).fetchall()
        for task in expired:
            if task["attempt"] < task["max_attempts"]:
                connection.execute(
                    """
                    UPDATE tasks SET status = 'queued', lease_owner = NULL,
                        lease_token = NULL, lease_expires_at = NULL, updated_at = ?
                    WHERE id = ?
                    """,
                    (now, task["id"]),
                )
                event_type = "task.lease_expired"
            else:
                connection.execute(
                    """
                    UPDATE tasks SET status = 'failed', lease_owner = NULL,
                        lease_token = NULL, lease_expires_at = NULL,
                        last_error = 'lease expired after final attempt',
                        completed_at = ?, updated_at = ? WHERE id = ?
                    """,
                    (now, now, task["id"]),
                )
                event_type = "task.failed"
            self._event(
                connection,
                event_type,
                "task",
                task["id"],
                {"reason": "lease_expired", "worker_id": task["lease_owner"]},
                now,
            )

    def acquire(self, body: dict[str, Any]) -> dict[str, Any] | None:
        worker_id = _require_text(body, "worker_id")
        agent_id = body.get("agent_id")
        if agent_id is not None and (
            not isinstance(agent_id, str) or not agent_id.strip()
        ):
            raise Invalid("agent_id must be a non-empty string")
        try:
            ttl_seconds = int(body.get("ttl_seconds", 60))
        except (TypeError, ValueError) as error:
            raise Invalid("ttl_seconds must be an integer") from error
        if not 5 <= ttl_seconds <= 3600:
            raise Invalid("ttl_seconds must be between 5 and 3600")

        now = time.time()
        token = str(uuid.uuid4())
        expires_at = now + ttl_seconds
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            self._reclaim_expired(connection, now)
            query = """
                SELECT t.id
                FROM tasks t
                JOIN projects p ON p.id = t.project_id
                JOIN agents a ON a.id = t.agent_id
                WHERE t.status = 'queued'
                  AND t.available_at <= ?
                  AND p.status = 'active'
                  AND a.status = 'active'
                  AND NOT EXISTS (
                      SELECT 1 FROM tasks active
                      WHERE active.agent_id = t.agent_id
                        AND active.status = 'leased'
                        AND active.lease_expires_at > ?
                  )
            """
            values: list[Any] = [now, now]
            if agent_id:
                query += " AND t.agent_id = ?"
                values.append(agent_id)
            query += " ORDER BY t.priority DESC, t.created_at ASC LIMIT 1"
            candidate = connection.execute(query, values).fetchone()
            if candidate is None:
                connection.commit()
                return None
            task_id = candidate["id"]
            connection.execute(
                """
                UPDATE tasks SET status = 'leased', attempt = attempt + 1,
                    lease_owner = ?, lease_token = ?, lease_expires_at = ?, updated_at = ?
                WHERE id = ?
                """,
                (worker_id, token, expires_at, now, task_id),
            )
            self._event(
                connection,
                "task.leased",
                "task",
                task_id,
                {"worker_id": worker_id, "lease_expires_at": expires_at},
                now,
            )
            task = connection.execute(
                "SELECT * FROM tasks WHERE id = ?", (task_id,)
            ).fetchone()
            project = connection.execute(
                "SELECT * FROM projects WHERE id = ?", (task["project_id"],)
            ).fetchone()
            agent = connection.execute(
                "SELECT * FROM agents WHERE id = ?", (task["agent_id"],)
            ).fetchone()
            connection.commit()
        return {"task": _row(task), "project": _row(project), "agent": _row(agent)}

    @staticmethod
    def _assert_lease(
        task: sqlite3.Row | None, worker_id: str, lease_token: str, now: float
    ) -> None:
        if task is None:
            raise NotFound("task not found")
        if task["status"] != "leased":
            raise Conflict("task is not leased")
        if task["lease_owner"] != worker_id or task["lease_token"] != lease_token:
            raise Conflict("lease is not owned by this worker")
        if task["lease_expires_at"] <= now:
            raise Conflict("lease has expired")

    def heartbeat(self, task_id: str, body: dict[str, Any]) -> dict[str, Any]:
        worker_id = _require_text(body, "worker_id")
        lease_token = _require_text(body, "lease_token")
        try:
            ttl_seconds = int(body.get("ttl_seconds", 60))
        except (TypeError, ValueError) as error:
            raise Invalid("ttl_seconds must be an integer") from error
        if not 5 <= ttl_seconds <= 3600:
            raise Invalid("ttl_seconds must be between 5 and 3600")
        now = time.time()
        expires_at = now + ttl_seconds
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            task = connection.execute(
                "SELECT * FROM tasks WHERE id = ?", (task_id,)
            ).fetchone()
            self._assert_lease(task, worker_id, lease_token, now)
            connection.execute(
                "UPDATE tasks SET lease_expires_at = ?, updated_at = ? WHERE id = ?",
                (expires_at, now, task_id),
            )
            connection.execute(
                "UPDATE agents SET updated_at = ? WHERE id = ?", (now, task["agent_id"])
            )
            connection.commit()
        return self.get_task(task_id)

    def complete(self, task_id: str, body: dict[str, Any]) -> dict[str, Any]:
        worker_id = _require_text(body, "worker_id")
        lease_token = _require_text(body, "lease_token")
        if "output" not in body:
            raise Invalid("output is required")
        now = time.time()
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            task = connection.execute(
                "SELECT * FROM tasks WHERE id = ?", (task_id,)
            ).fetchone()
            self._assert_lease(task, worker_id, lease_token, now)
            connection.execute(
                """
                UPDATE tasks SET status = 'succeeded', output_json = ?,
                    lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL,
                    completed_at = ?, updated_at = ? WHERE id = ?
                """,
                (_json(body["output"]), now, now, task_id),
            )
            if "memory" in body:
                connection.execute(
                    "UPDATE agents SET memory_json = ?, updated_at = ? WHERE id = ?",
                    (_json(body["memory"]), now, task["agent_id"]),
                )
            self._event(
                connection,
                "task.succeeded",
                "task",
                task_id,
                {"worker_id": worker_id, "attempt": task["attempt"]},
                now,
            )
            connection.commit()
        return self.get_task(task_id)

    def fail(self, task_id: str, body: dict[str, Any]) -> dict[str, Any]:
        worker_id = _require_text(body, "worker_id")
        lease_token = _require_text(body, "lease_token")
        error_message = _require_text(body, "error")
        retry = body.get("retry", True)
        if not isinstance(retry, bool):
            raise Invalid("retry must be a boolean")
        try:
            delay_seconds = max(0, int(body.get("delay_seconds", 0)))
        except (TypeError, ValueError) as error:
            raise Invalid("delay_seconds must be an integer") from error
        now = time.time()
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            task = connection.execute(
                "SELECT * FROM tasks WHERE id = ?", (task_id,)
            ).fetchone()
            self._assert_lease(task, worker_id, lease_token, now)
            should_retry = retry and task["attempt"] < task["max_attempts"]
            if should_retry:
                status = "queued"
                completed_at = None
                event_type = "task.retry_scheduled"
            else:
                status = "failed"
                completed_at = now
                event_type = "task.failed"
            connection.execute(
                """
                UPDATE tasks SET status = ?, available_at = ?, last_error = ?,
                    lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL,
                    completed_at = ?, updated_at = ? WHERE id = ?
                """,
                (
                    status,
                    now + delay_seconds,
                    error_message,
                    completed_at,
                    now,
                    task_id,
                ),
            )
            self._event(
                connection,
                event_type,
                "task",
                task_id,
                {
                    "worker_id": worker_id,
                    "attempt": task["attempt"],
                    "error": error_message,
                },
                now,
            )
            connection.commit()
        return self.get_task(task_id)

    def status(self) -> dict[str, Any]:
        with self._connect() as connection:
            projects = connection.execute(
                "SELECT status, COUNT(*) count FROM projects GROUP BY status"
            ).fetchall()
            agents = connection.execute(
                "SELECT status, COUNT(*) count FROM agents GROUP BY status"
            ).fetchall()
            tasks = connection.execute(
                "SELECT status, COUNT(*) count FROM tasks GROUP BY status"
            ).fetchall()
            latest_event = (
                connection.execute("SELECT MAX(sequence) FROM events").fetchone()[0]
                or 0
            )
        return {
            "projects": {row["status"]: row["count"] for row in projects},
            "agents": {row["status"]: row["count"] for row in agents},
            "tasks": {row["status"]: row["count"] for row in tasks},
            "latest_event": latest_event,
        }


class PolisServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address: tuple[str, int], store: Store):
        super().__init__(address, Handler)
        self.store = store


class Handler(BaseHTTPRequestHandler):
    server: PolisServer
    protocol_version = "HTTP/1.1"

    def log_message(self, message: str, *args: Any) -> None:
        LOG.info("%s - %s", self.client_address[0], message % args)

    def _send(self, status: HTTPStatus, body: Any | None = None) -> None:
        if body is None:
            data = b""
        else:
            data = _json(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        self.send_response(status)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _body(self) -> dict[str, Any]:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError as error:
            raise Invalid("invalid Content-Length") from error
        if length <= 0:
            return {}
        if length > MAX_BODY_BYTES:
            raise Invalid(f"request body exceeds {MAX_BODY_BYTES} bytes")
        try:
            value = json.loads(self.rfile.read(length))
        except (json.JSONDecodeError, UnicodeDecodeError) as error:
            raise Invalid("request body must be valid JSON") from error
        if not isinstance(value, dict):
            raise Invalid("request body must be a JSON object")
        return value

    @staticmethod
    def _limit(query: dict[str, list[str]]) -> int:
        try:
            return min(1000, max(1, int(query.get("limit", ["100"])[0])))
        except ValueError as error:
            raise Invalid("limit must be an integer") from error

    def _dispatch(self, method: str) -> tuple[HTTPStatus, Any | None]:
        parsed = urlparse(self.path)
        path = parsed.path.rstrip("/") or "/"
        query = parse_qs(parsed.query)
        store = self.server.store

        if method == "GET" and path == "/healthz":
            return HTTPStatus.OK, {"ok": store.healthy()}
        if method == "GET" and path == "/v1/status":
            return HTTPStatus.OK, store.status()
        if method == "GET" and path == "/v1/projects":
            return HTTPStatus.OK, {"items": store.list_projects(self._limit(query))}
        if method == "GET" and path == "/v1/agents":
            return HTTPStatus.OK, {"items": store.list_agents(self._limit(query))}
        if method == "GET" and path == "/v1/tasks":
            filters = {
                name: query.get(name, [""])[0]
                for name in ("status", "project_id", "agent_id")
            }
            return HTTPStatus.OK, {
                "items": store.list_tasks(filters, self._limit(query))
            }
        if method == "GET" and path == "/v1/events":
            try:
                after = max(0, int(query.get("after", ["0"])[0]))
            except ValueError as error:
                raise Invalid("after must be an integer") from error
            return HTTPStatus.OK, {
                "items": store.list_events(after, self._limit(query))
            }

        if method == "POST" and path == "/v1/projects":
            return HTTPStatus.CREATED, store.create_project(self._body())
        if method == "POST" and path == "/v1/agents":
            return HTTPStatus.CREATED, store.create_agent(self._body())
        if method == "POST" and path == "/v1/tasks":
            return HTTPStatus.CREATED, store.create_task(self._body())
        if method == "POST" and path == "/v1/leases":
            lease = store.acquire(self._body())
            return (HTTPStatus.OK, lease) if lease else (HTTPStatus.NO_CONTENT, None)

        task_match = re.fullmatch(r"/v1/tasks/([^/]+)", path)
        if method == "GET" and task_match:
            return HTTPStatus.OK, store.get_task(task_match.group(1))
        action_match = re.fullmatch(
            r"/v1/tasks/([^/]+)/(heartbeat|complete|fail)", path
        )
        if method == "POST" and action_match:
            task_id, action = action_match.groups()
            operation = getattr(store, action)
            return HTTPStatus.OK, operation(task_id, self._body())
        cancel_match = re.fullmatch(r"/v1/tasks/([^/]+)/cancel", path)
        if method == "POST" and cancel_match:
            return HTTPStatus.OK, store.cancel_task(cancel_match.group(1))

        project_match = re.fullmatch(r"/v1/projects/([^/]+)", path)
        if method == "PATCH" and project_match:
            return HTTPStatus.OK, store.update_project(
                project_match.group(1), self._body()
            )
        agent_match = re.fullmatch(r"/v1/agents/([^/]+)", path)
        if method == "PATCH" and agent_match:
            return HTTPStatus.OK, store.update_agent(agent_match.group(1), self._body())

        raise NotFound("endpoint not found")

    def _handle(self, method: str) -> None:
        try:
            status, body = self._dispatch(method)
            self._send(status, body)
        except PolisError as error:
            self._send(
                error.status, {"error": {"code": error.code, "message": str(error)}}
            )
        except Exception:
            LOG.exception("unhandled request error")
            self._send(
                HTTPStatus.INTERNAL_SERVER_ERROR,
                {"error": {"code": "internal", "message": "internal server error"}},
            )

    def do_GET(self) -> None:
        self._handle("GET")

    def do_POST(self) -> None:
        self._handle("POST")

    def do_PATCH(self) -> None:
        self._handle("PATCH")


def serve(bind: str, port: int, database: str) -> None:
    store = Store(database)
    server = PolisServer((bind, port), store)

    def stop(_signum: int, _frame: Any) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    LOG.info("Polis controller listening on %s:%d using %s", bind, port, database)
    try:
        server.serve_forever(poll_interval=0.5)
    finally:
        server.server_close()


def main() -> None:
    parser = argparse.ArgumentParser(description="Run the Polis controller")
    parser.add_argument("--bind", default=os.getenv("POLIS_BIND", "0.0.0.0"))
    parser.add_argument(
        "--port", type=int, default=int(os.getenv("POLIS_PORT", "8080"))
    )
    parser.add_argument(
        "--database", default=os.getenv("POLIS_DB_PATH", "/data/polis.db")
    )
    args = parser.parse_args()
    logging.basicConfig(
        level=os.getenv("POLIS_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    serve(args.bind, args.port, args.database)


if __name__ == "__main__":
    main()
