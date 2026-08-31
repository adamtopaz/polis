import assert from "node:assert/strict";
import test from "node:test";
import { PolisApiError } from "../src/polis.js";
import { isRetryableMailboxError, withMailboxRetry } from "../src/retry.js";

test("mailbox operations recover from transient network failures", async () => {
  let attempts = 0;
  let unavailable = 0;
  let availableAfter = -1;
  const clock = [100, 125];
  const result = await withMailboxRetry(async () => {
    attempts += 1;
    if (attempts < 3) {
      throw new TypeError("fetch failed");
    }
    return "ok";
  }, {
    signal: new AbortController().signal,
    now: () => clock.shift() ?? 125,
    wait: async () => {},
    onUnavailable: () => { unavailable += 1; },
    onAvailable: (duration) => { availableAfter = duration; },
  });

  assert.equal(result, "ok");
  assert.equal(attempts, 3);
  assert.equal(unavailable, 1);
  assert.equal(availableAfter, 25);
});

test("mailbox operations retry server failures but not rejected leases", async () => {
  assert.equal(isRetryableMailboxError(new PolisApiError(503, "unavailable")), true);
  assert.equal(isRetryableMailboxError(new PolisApiError(429, "busy")), true);
  assert.equal(isRetryableMailboxError(new PolisApiError(401, "expired")), false);
  assert.equal(isRetryableMailboxError(new PolisApiError(400, "invalid")), false);
  assert.equal(isRetryableMailboxError(new TypeError("fetch failed")), true);
  assert.equal(isRetryableMailboxError(new SyntaxError("bad JSON")), false);

  let attempts = 0;
  await assert.rejects(
    withMailboxRetry(async () => {
      attempts += 1;
      throw new PolisApiError(401, "expired");
    }, {
      signal: new AbortController().signal,
      wait: async () => {},
    }),
    /expired/,
  );
  assert.equal(attempts, 1);
});

test("mailbox retry stops when shutdown is requested", async () => {
  const shutdown = new AbortController();
  let attempts = 0;
  await assert.rejects(
    withMailboxRetry(async () => {
      attempts += 1;
      throw new TypeError("fetch failed");
    }, {
      signal: shutdown.signal,
      wait: async () => { shutdown.abort(); },
    }),
    (error: unknown) => error instanceof TypeError && error.message === "fetch failed",
  );
  assert.equal(attempts, 2);
});
