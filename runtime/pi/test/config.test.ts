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

test("runtime flags override the environment model and select thinking", () => {
  const config = loadConfig({
    POLIS_URL: "http://polis.test",
    POLIS_AGENT_TOKEN: "lease-token",
    POLIS_WORKSPACE: "/work/agent-1",
    POLIS_CHARTER_PATH: "/work/agent-1/.polis/charter.md",
    POLIS_PI_MODEL: "anthropic/environment-model:medium",
  }, [
    "--model",
    "openai-codex/gpt-5.5",
    "--thinking=high",
    "--compaction-reserve-tokens",
    "32768",
    "--compaction-keep-recent-tokens=24000",
  ]);

  assert.equal(config.model, "openai-codex/gpt-5.5");
  assert.equal(config.thinking, "high");
  assert.equal(config.compactionReserveTokens, 32768);
  assert.equal(config.compactionKeepRecentTokens, 24000);
});

test("thinking can be selected without overriding the restored model", () => {
  const config = loadConfig({
    POLIS_URL: "http://polis.test",
    POLIS_AGENT_TOKEN: "lease-token",
    POLIS_WORKSPACE: "/work/agent-1",
    POLIS_CHARTER_PATH: "/work/agent-1/.polis/charter.md",
  }, ["--thinking", "low"]);

  assert.equal(config.model, undefined);
  assert.equal(config.thinking, "low");
});

test("runtime flags reject missing, invalid, and unknown values", () => {
  const environment = {
    POLIS_URL: "http://polis.test",
    POLIS_AGENT_TOKEN: "lease-token",
    POLIS_WORKSPACE: "/work/agent-1",
    POLIS_CHARTER_PATH: "/work/agent-1/.polis/charter.md",
  };

  assert.throws(() => loadConfig(environment, ["--model"]), /--model requires a value/);
  assert.throws(() => loadConfig(environment, ["--thinking", "enormous"]), /invalid --thinking level/);
  assert.throws(
    () => loadConfig(environment, ["--compaction-reserve-tokens", "0"]),
    /--compaction-reserve-tokens must be a positive integer/,
  );
  assert.throws(
    () => loadConfig(environment, ["--compaction-keep-recent-tokens=12.5"]),
    /--compaction-keep-recent-tokens must be a positive integer/,
  );
  assert.throws(() => loadConfig(environment, ["--provider", "openai"]), /unknown argument/);
});

test("loadConfig requires lease environment", () => {
  assert.throws(() => loadConfig({}), /POLIS_URL is required/);
});
