from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


class Client:
    def __init__(self, url: str):
        self.url = url.rstrip("/")

    def request(self, method: str, path: str, body: Any | None = None) -> Any:
        request = urllib.request.Request(
            self.url + path,
            data=json.dumps(body).encode() if body is not None else None,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                data = response.read()
        except urllib.error.HTTPError as error:
            detail = error.read().decode(errors="replace")
            raise SystemExit(f"Polis returned HTTP {error.code}: {detail}") from error
        except urllib.error.URLError as error:
            raise SystemExit(
                f"Cannot reach Polis at {self.url}: {error.reason}"
            ) from error
        return json.loads(data) if data else None


def _value(raw: str) -> Any:
    try:
        return json.loads(raw)
    except json.JSONDecodeError as error:
        raise argparse.ArgumentTypeError(f"invalid JSON: {error}") from error


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(prog="polis", description="Control a Polis fleet")
    root.add_argument("--url", default=os.getenv("POLIS_URL", "http://localhost:8080"))
    commands = root.add_subparsers(dest="command", required=True)

    commands.add_parser("status", help="show fleet counts")

    project = commands.add_parser("create-project", help="create a project")
    project.add_argument("name")
    project.add_argument("--id")
    project.add_argument("--description", default="")

    agent = commands.add_parser(
        "create-agent", help="create a persistent agent identity"
    )
    agent.add_argument("name")
    agent.add_argument("--id")
    agent.add_argument("--instructions", required=True)
    agent.add_argument("--memory", type=_value, default={})

    task = commands.add_parser("create-task", help="queue work for an agent")
    task.add_argument("title")
    task.add_argument("--id")
    task.add_argument("--project", required=True)
    task.add_argument("--agent", required=True)
    task.add_argument("--input", type=_value, required=True)
    task.add_argument("--priority", type=int, default=0)
    task.add_argument("--max-attempts", type=int, default=3)

    tasks = commands.add_parser("tasks", help="list tasks")
    tasks.add_argument("--status")
    tasks.add_argument("--project")
    tasks.add_argument("--agent")
    tasks.add_argument("--limit", type=int, default=100)

    events = commands.add_parser("events", help="read the ordered event stream")
    events.add_argument("--after", type=int, default=0)
    events.add_argument("--limit", type=int, default=100)

    pause_project = commands.add_parser("set-project-status")
    pause_project.add_argument("project_id")
    pause_project.add_argument("status", choices=("active", "paused", "archived"))

    pause_agent = commands.add_parser("set-agent-status")
    pause_agent.add_argument("agent_id")
    pause_agent.add_argument("status", choices=("active", "paused", "retired"))

    cancel = commands.add_parser("cancel-task")
    cancel.add_argument("task_id")
    return root


def run(args: argparse.Namespace, client: Client) -> Any:
    if args.command == "status":
        return client.request("GET", "/v1/status")
    if args.command == "create-project":
        body = {"name": args.name, "description": args.description}
        if args.id:
            body["id"] = args.id
        return client.request("POST", "/v1/projects", body)
    if args.command == "create-agent":
        body = {
            "name": args.name,
            "instructions": args.instructions,
            "memory": args.memory,
        }
        if args.id:
            body["id"] = args.id
        return client.request("POST", "/v1/agents", body)
    if args.command == "create-task":
        body = {
            "title": args.title,
            "project_id": args.project,
            "agent_id": args.agent,
            "input": args.input,
            "priority": args.priority,
            "max_attempts": args.max_attempts,
        }
        if args.id:
            body["id"] = args.id
        return client.request("POST", "/v1/tasks", body)
    if args.command == "tasks":
        values = {
            "status": args.status,
            "project_id": args.project,
            "agent_id": args.agent,
            "limit": args.limit,
        }
        query = urllib.parse.urlencode(
            {key: value for key, value in values.items() if value is not None}
        )
        return client.request("GET", f"/v1/tasks?{query}")
    if args.command == "events":
        return client.request(
            "GET", f"/v1/events?after={args.after}&limit={args.limit}"
        )
    if args.command == "set-project-status":
        return client.request(
            "PATCH", f"/v1/projects/{args.project_id}", {"status": args.status}
        )
    if args.command == "set-agent-status":
        return client.request(
            "PATCH", f"/v1/agents/{args.agent_id}", {"status": args.status}
        )
    if args.command == "cancel-task":
        return client.request("POST", f"/v1/tasks/{args.task_id}/cancel", {})
    raise AssertionError(f"unhandled command: {args.command}")


def main() -> None:
    args = parser().parse_args()
    result = run(args, Client(args.url))
    json.dump(result, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
