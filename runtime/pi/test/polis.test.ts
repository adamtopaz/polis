import assert from "node:assert/strict";
import test from "node:test";
import { PolisApiError, PolisClient } from "../src/polis.js";

test("PolisClient sends bearer authentication and persistent runtime requests", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  const fetcher: typeof fetch = async (input, init) => {
    requests.push({ url: String(input), ...(init === undefined ? {} : { init }) });
    return Response.json({ id: "agent-1" });
  };
  const polis = new PolisClient("http://polis.test", "lease-token", fetcher);

  await polis.acknowledge(42);
  await polis.journal("work.completed", { result: "ok" });
  await polis.messages(30);

  assert.equal(requests.length, 3);
  assert.equal(new Headers(requests[0]?.init?.headers).get("Authorization"), "Bearer lease-token");
  assert.equal(requests[0]?.url, "http://polis.test/v1/self/messages/ack");
  assert.equal(requests[0]?.init?.body, JSON.stringify({ through: 42 }));
  assert.equal(requests[1]?.init?.body, JSON.stringify({ kind: "work.completed", data: { result: "ok" } }));
  assert.equal(requests[2]?.url, "http://polis.test/v1/self/messages?wait_seconds=30");
});

test("PolisClient handles empty success responses", async () => {
  const polis = new PolisClient(
    "http://polis.test",
    "lease-token",
    async () => new Response(null, { status: 204 }),
  );
  await polis.acknowledge(1);
});

test("PolisClient exposes mailbox errors without leaking the token", async () => {
  const polis = new PolisClient(
    "http://polis.test",
    "secret-token",
    async () => Response.json({ error: "lease expired" }, { status: 401 }),
  );

  await assert.rejects(
    polis.self(),
    (error: unknown) => error instanceof PolisApiError
      && error.status === 401
      && error.message === "lease expired"
      && !error.message.includes("secret-token"),
  );
});
