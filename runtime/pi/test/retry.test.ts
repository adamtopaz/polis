import assert from "node:assert/strict";
import test from "node:test";
import { PolisApiError } from "../src/polis.js";
import { isRetryableControllerError, withControllerRetry } from "../src/retry.js";

test("controller operations recover from transient network failures", async () => {
  let attempts = 0;
  let unavailable = 0;
  let availableAfter = -1;
  const clock = [100, 125];
  const result = await withControllerRetry(async () => {
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

test("controller operations retry server failures but not rejected leases", async () => {
  assert.equal(isRetryableControllerError(new PolisApiError(503, "unavailable")), true);
  assert.equal(isRetryableControllerError(new PolisApiError(429, "busy")), true);
  assert.equal(isRetryableControllerError(new PolisApiError(401, "expired")), false);
  assert.equal(isRetryableControllerError(new PolisApiError(400, "invalid")), false);
  assert.equal(isRetryableControllerError(new TypeError("fetch failed")), true);
  assert.equal(isRetryableControllerError(new SyntaxError("bad JSON")), false);

  let attempts = 0;
  await assert.rejects(
    withControllerRetry(async () => {
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

test("controller retry stops when shutdown is requested", async () => {
  const shutdown = new AbortController();
  let attempts = 0;
  await assert.rejects(
    withControllerRetry(async () => {
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
