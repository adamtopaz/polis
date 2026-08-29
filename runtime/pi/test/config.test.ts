import assert from "node:assert/strict";
import test from "node:test";
import path from "node:path";
import { loadConfig } from "../src/config.js";

test("loadConfig derives durable Pi paths", () => {
  const config = loadConfig({
    POLIS_URL: "http://polis.test/",
    POLIS_AGENT_TOKEN: "lease-token",
    POLIS_WORKSPACE: "/work/agent-1",
    POLIS_CHARTER_PATH: "/work/agent-1/.polis/charter.md",
    POLIS_PI_MODEL: "anthropic/claude-sonnet-4-5:high",
    POLIS_PI_AUTH_FILE: "./auth.json",
  });

  assert.equal(config.polisUrl, "http://polis.test");
  assert.equal(config.agentDir, "/work/agent-1/.polis/pi-agent");
  assert.equal(config.sessionDir, "/work/agent-1/.polis/pi-sessions");
  assert.equal(config.model, "anthropic/claude-sonnet-4-5:high");
  assert.equal(config.authFile, path.resolve("./auth.json"));
});

test("loadConfig requires lease environment", () => {
  assert.throws(() => loadConfig({}), /POLIS_URL is required/);
});
