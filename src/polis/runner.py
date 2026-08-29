from __future__ import annotations

import argparse
import json
import logging
import os
import shlex
import signal
import socket
import subprocess
import threading
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from typing import Any

LOG = logging.getLogger("polis.runner")


class ApiError(Exception):
    pass


class PolisClient:
    def __init__(self, base_url: str, timeout: float = 10):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def post(self, path: str, body: dict[str, Any]) -> tuple[int, Any | None]:
        request = urllib.request.Request(
            self.base_url + path,
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                data = response.read()
                return response.status, json.loads(data) if data else None
        except urllib.error.HTTPError as error:
            data = error.read()
            message = data.decode(errors="replace") if data else str(error)
            raise ApiError(f"POST {path} returned {error.code}: {message}") from error
        except urllib.error.URLError as error:
            raise ApiError(f"POST {path} failed: {error.reason}") from error


@dataclass(frozen=True)
class Config:
    url: str
    worker_id: str
    command: list[str]
    agent_id: str | None
    lease_seconds: int
    poll_seconds: float
    task_timeout_seconds: int
    retry_delay_seconds: int


class Runner:
    def __init__(self, config: Config):
        self.config = config
        self.client = PolisClient(config.url)
        self.stopping = threading.Event()

    def stop(self, _signum: int | None = None, _frame: Any | None = None) -> None:
        self.stopping.set()

    def _heartbeat(self, task_id: str, token: str, stopped: threading.Event) -> None:
        interval = max(1.0, self.config.lease_seconds / 3)
        while not stopped.wait(interval):
            try:
                self.client.post(
                    f"/v1/tasks/{task_id}/heartbeat",
                    {
                        "worker_id": self.config.worker_id,
                        "lease_token": token,
                        "ttl_seconds": self.config.lease_seconds,
                    },
                )
            except ApiError as error:
                LOG.warning("heartbeat failed for task %s: %s", task_id, error)

    def _execute(self, lease: dict[str, Any]) -> tuple[int, Any, str]:
        task_id = lease["task"]["id"]
        token = lease["task"]["lease_token"]
        heartbeat_stop = threading.Event()
        heartbeat = threading.Thread(
            target=self._heartbeat,
            args=(task_id, token, heartbeat_stop),
            daemon=True,
        )
        heartbeat.start()
        try:
            process = subprocess.Popen(
                self.config.command,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                start_new_session=True,
            )
            try:
                stdout, stderr = process.communicate(
                    json.dumps(lease), timeout=self.config.task_timeout_seconds
                )
            except subprocess.TimeoutExpired:
                process.terminate()
                try:
                    stdout, stderr = process.communicate(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    stdout, stderr = process.communicate()
                return 124, {"text": stdout}, f"executor timed out; {stderr}".strip()
        finally:
            heartbeat_stop.set()
            heartbeat.join(timeout=2)

        stdout = stdout.strip()
        try:
            output = json.loads(stdout) if stdout else {}
        except json.JSONDecodeError:
            output = {"text": stdout}
        return process.returncode, output, stderr.strip()

    def run_once(self) -> bool:
        request: dict[str, Any] = {
            "worker_id": self.config.worker_id,
            "ttl_seconds": self.config.lease_seconds,
        }
        if self.config.agent_id:
            request["agent_id"] = self.config.agent_id
        status, lease = self.client.post("/v1/leases", request)
        if status == 204 or lease is None:
            return False

        task = lease["task"]
        task_id = task["id"]
        LOG.info(
            "executing task %s (%s), attempt %s/%s",
            task_id,
            task["title"],
            task["attempt"],
            task["max_attempts"],
        )
        return_code, output, error_output = self._execute(lease)
        common = {
            "worker_id": self.config.worker_id,
            "lease_token": task["lease_token"],
        }
        if return_code == 0:
            body = {**common, "output": output}
            if isinstance(output, dict) and "memory" in output:
                body["memory"] = output["memory"]
            self.client.post(f"/v1/tasks/{task_id}/complete", body)
            LOG.info("completed task %s", task_id)
        else:
            error = error_output or f"executor exited with status {return_code}"
            self.client.post(
                f"/v1/tasks/{task_id}/fail",
                {
                    **common,
                    "error": error[-8192:],
                    "retry": True,
                    "delay_seconds": self.config.retry_delay_seconds,
                },
            )
            LOG.warning("executor failed task %s: %s", task_id, error)
        return True

    def run_forever(self) -> None:
        LOG.info("runner %s starting", self.config.worker_id)
        while not self.stopping.is_set():
            try:
                worked = self.run_once()
            except (ApiError, OSError, KeyError, TypeError, ValueError) as error:
                LOG.warning("runner iteration failed: %s", error)
                worked = False
            if not worked:
                self.stopping.wait(self.config.poll_seconds)
        LOG.info("runner %s stopped", self.config.worker_id)


def _command(value: str) -> list[str]:
    try:
        decoded = json.loads(value)
    except json.JSONDecodeError:
        decoded = None
    if (
        isinstance(decoded, list)
        and decoded
        and all(isinstance(item, str) for item in decoded)
    ):
        return decoded
    parsed = shlex.split(value)
    if not parsed:
        raise ValueError("executor command cannot be empty")
    return parsed


def main() -> None:
    default_id = f"{socket.gethostname()}-{uuid.uuid4().hex[:8]}"
    parser = argparse.ArgumentParser(description="Run a Polis task runner")
    parser.add_argument(
        "--url", default=os.getenv("POLIS_URL", "http://localhost:8080")
    )
    parser.add_argument("--worker-id", default=os.getenv("POLIS_WORKER_ID", default_id))
    parser.add_argument("--agent-id", default=os.getenv("POLIS_AGENT_ID"))
    parser.add_argument(
        "--command",
        default=os.getenv("POLIS_EXECUTOR_COMMAND", '["polis-echo-agent"]'),
    )
    parser.add_argument(
        "--lease-seconds", type=int, default=int(os.getenv("POLIS_LEASE_SECONDS", "60"))
    )
    parser.add_argument(
        "--poll-seconds",
        type=float,
        default=float(os.getenv("POLIS_POLL_SECONDS", "2")),
    )
    parser.add_argument(
        "--task-timeout-seconds",
        type=int,
        default=int(os.getenv("POLIS_TASK_TIMEOUT_SECONDS", "3600")),
    )
    parser.add_argument(
        "--retry-delay-seconds",
        type=int,
        default=int(os.getenv("POLIS_RETRY_DELAY_SECONDS", "10")),
    )
    args = parser.parse_args()
    logging.basicConfig(
        level=os.getenv("POLIS_LOG_LEVEL", "INFO"),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    config = Config(
        url=args.url,
        worker_id=args.worker_id,
        command=_command(args.command),
        agent_id=args.agent_id,
        lease_seconds=args.lease_seconds,
        poll_seconds=args.poll_seconds,
        task_timeout_seconds=args.task_timeout_seconds,
        retry_delay_seconds=args.retry_delay_seconds,
    )
    runner = Runner(config)
    signal.signal(signal.SIGTERM, runner.stop)
    signal.signal(signal.SIGINT, runner.stop)
    runner.run_forever()


if __name__ == "__main__":
    main()
