"""A deterministic executor used to validate the Polis control loop."""

import json
import sys


def main() -> None:
    lease = json.load(sys.stdin)
    task = lease["task"]
    agent = lease["agent"]
    memory = agent.get("memory") or {}
    completed = int(memory.get("completed", 0)) + 1

    json.dump(
        {
            "message": f"{agent['name']} completed {task['title']}",
            "received": task["input"],
            "memory": {**memory, "completed": completed},
        },
        sys.stdout,
    )


if __name__ == "__main__":
    main()
