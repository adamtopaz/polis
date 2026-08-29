import json
import os
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

from polis.controller import PolisServer, Store


class HttpTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        store = Store(os.path.join(self.tempdir.name, "polis.db"))
        try:
            self.server = PolisServer(("127.0.0.1", 0), store)
        except PermissionError:
            self.tempdir.cleanup()
            self.skipTest("network sockets are disabled in this build sandbox")
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        self.tempdir.cleanup()

    def request(self, method: str, path: str, body=None):
        data = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(
            self.url + path,
            data=data,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=2) as response:
            content = response.read()
            return response.status, json.loads(content) if content else None

    def test_end_to_end_http_flow(self) -> None:
        status, health = self.request("GET", "/healthz")
        self.assertEqual(200, status)
        self.assertEqual({"ok": True}, health)

        self.request("POST", "/v1/projects", {"id": "p", "name": "P"})
        self.request(
            "POST",
            "/v1/agents",
            {"id": "a", "name": "A", "instructions": "Do work"},
        )
        self.request(
            "POST",
            "/v1/tasks",
            {"id": "t", "project_id": "p", "agent_id": "a", "title": "T", "input": {}},
        )
        _, lease = self.request(
            "POST", "/v1/leases", {"worker_id": "w", "ttl_seconds": 30}
        )
        _, task = self.request(
            "POST",
            "/v1/tasks/t/complete",
            {
                "worker_id": "w",
                "lease_token": lease["task"]["lease_token"],
                "output": {"done": True},
            },
        )
        self.assertEqual("succeeded", task["status"])

        _, status_body = self.request("GET", "/v1/status")
        self.assertEqual(1, status_body["tasks"]["succeeded"])

    def test_validation_errors_are_json(self) -> None:
        with self.assertRaises(urllib.error.HTTPError) as caught:
            self.request("POST", "/v1/projects", {"name": ""})
        error = caught.exception
        self.assertEqual(422, error.code)
        body = json.loads(error.read())
        self.assertEqual("invalid", body["error"]["code"])


if __name__ == "__main__":
    unittest.main()
