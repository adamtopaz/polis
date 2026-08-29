import os
import tempfile
import unittest

from polis.controller import Conflict, Store


class StoreTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.store = Store(os.path.join(self.tempdir.name, "polis.db"))
        self.project = self.store.create_project(
            {"id": "project-a", "name": "Project A", "description": "test"}
        )
        self.agent = self.store.create_agent(
            {
                "id": "agent-a",
                "name": "Agent A",
                "instructions": "Complete the task.",
                "memory": {"completed": 0},
            }
        )

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def create_task(self, task_id: str, priority: int = 0, max_attempts: int = 3):
        return self.store.create_task(
            {
                "id": task_id,
                "project_id": self.project["id"],
                "agent_id": self.agent["id"],
                "title": task_id,
                "input": {"task": task_id},
                "priority": priority,
                "max_attempts": max_attempts,
            }
        )

    def test_agent_executes_only_one_task_at_a_time(self) -> None:
        self.create_task("low", priority=1)
        self.create_task("high", priority=10)

        first = self.store.acquire({"worker_id": "worker-1", "ttl_seconds": 30})
        self.assertEqual("high", first["task"]["id"])
        self.assertEqual(1, first["task"]["attempt"])

        self.assertIsNone(
            self.store.acquire({"worker_id": "worker-2", "ttl_seconds": 30})
        )

        completed = self.store.complete(
            "high",
            {
                "worker_id": "worker-1",
                "lease_token": first["task"]["lease_token"],
                "output": {"ok": True},
                "memory": {"completed": 1},
            },
        )
        self.assertEqual("succeeded", completed["status"])
        self.assertEqual({"completed": 1}, self.store.get_agent("agent-a")["memory"])

        second = self.store.acquire({"worker_id": "worker-2", "ttl_seconds": 30})
        self.assertEqual("low", second["task"]["id"])

    def test_wrong_worker_cannot_complete_a_lease(self) -> None:
        self.create_task("protected")
        lease = self.store.acquire({"worker_id": "owner", "ttl_seconds": 30})
        with self.assertRaises(Conflict):
            self.store.complete(
                "protected",
                {
                    "worker_id": "stranger",
                    "lease_token": lease["task"]["lease_token"],
                    "output": {},
                },
            )

    def test_failed_task_retries_up_to_max_attempts(self) -> None:
        self.create_task("flaky", max_attempts=2)
        first = self.store.acquire({"worker_id": "worker", "ttl_seconds": 30})
        retried = self.store.fail(
            "flaky",
            {
                "worker_id": "worker",
                "lease_token": first["task"]["lease_token"],
                "error": "temporary",
            },
        )
        self.assertEqual("queued", retried["status"])

        second = self.store.acquire({"worker_id": "worker", "ttl_seconds": 30})
        failed = self.store.fail(
            "flaky",
            {
                "worker_id": "worker",
                "lease_token": second["task"]["lease_token"],
                "error": "permanent",
            },
        )
        self.assertEqual("failed", failed["status"])
        self.assertEqual(2, failed["attempt"])

    def test_expired_lease_is_reclaimed(self) -> None:
        self.create_task("abandoned", max_attempts=2)
        first = self.store.acquire({"worker_id": "gone", "ttl_seconds": 30})
        with self.store._connect() as connection:
            connection.execute(
                "UPDATE tasks SET lease_expires_at = 0 WHERE id = 'abandoned'"
            )

        second = self.store.acquire({"worker_id": "replacement", "ttl_seconds": 30})
        self.assertEqual("abandoned", second["task"]["id"])
        self.assertEqual(2, second["task"]["attempt"])
        self.assertNotEqual(first["task"]["lease_token"], second["task"]["lease_token"])

    def test_events_form_an_ordered_audit_log(self) -> None:
        self.create_task("audited")
        lease = self.store.acquire({"worker_id": "worker", "ttl_seconds": 30})
        self.store.complete(
            "audited",
            {
                "worker_id": "worker",
                "lease_token": lease["task"]["lease_token"],
                "output": {},
            },
        )
        events = self.store.list_events()
        sequences = [event["sequence"] for event in events]
        self.assertEqual(sorted(sequences), sequences)
        self.assertEqual(
            [
                "project.created",
                "agent.created",
                "task.created",
                "task.leased",
                "task.succeeded",
            ],
            [event["type"] for event in events],
        )

    def test_paused_agent_receives_no_new_lease(self) -> None:
        self.create_task("waiting")
        self.store.update_agent("agent-a", {"status": "paused"})
        self.assertIsNone(
            self.store.acquire({"worker_id": "worker", "ttl_seconds": 30})
        )

        self.store.update_agent("agent-a", {"status": "active"})
        lease = self.store.acquire({"worker_id": "worker", "ttl_seconds": 30})
        self.assertEqual("waiting", lease["task"]["id"])

    def test_cancelling_a_leased_task_fences_its_worker(self) -> None:
        self.create_task("cancelled")
        lease = self.store.acquire({"worker_id": "worker", "ttl_seconds": 30})
        cancelled = self.store.cancel_task("cancelled")
        self.assertEqual("cancelled", cancelled["status"])

        with self.assertRaises(Conflict):
            self.store.complete(
                "cancelled",
                {
                    "worker_id": "worker",
                    "lease_token": lease["task"]["lease_token"],
                    "output": {},
                },
            )


if __name__ == "__main__":
    unittest.main()
