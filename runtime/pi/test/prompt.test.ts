import assert from "node:assert/strict";
import test from "node:test";
import { polisSystemPrompt, polisTurnPrompt, polisWakeupPrompt } from "../src/prompt.js";
import type { Agent, Message } from "../src/polis.js";

const agent: Agent = {
  id: "researcher",
  charter: "Explore the problem",
  runtime: ["polis-pi-agent"],
  state: "active",
  phase: "running",
};

test("system prompt identifies the agent and appends charter and additional instructions", () => {
  const prompt = polisSystemPrompt(
    agent,
    "Discover useful things.\n",
    "Keep reports concise.\n",
  );
  assert.match(prompt, /You are researcher/);
  assert.match(prompt, /Discover useful things\./);
  assert.match(prompt, /Additional instructions:\n\nKeep reports concise\./);
  assert.match(prompt, /autonomous/);
  assert.match(prompt, /invoke `polis send` at that time/);
  assert.doesNotMatch(prompt, /polis schedule/);
  assert.match(prompt, /Never reveal credentials/);
  assert.ok(prompt.indexOf("Never reveal credentials") < prompt.indexOf("Keep reports concise."));
});

test("system prompt omits the additional instructions section when not configured", () => {
  const prompt = polisSystemPrompt(agent, "Discover useful things.");
  assert.doesNotMatch(prompt, /Additional instructions:/);
});

test("turn prompt serializes unread mailbox bodies", () => {
  const messages: Message[] = [{
    id: 7,
    agent_id: "researcher",
    sender: "operator",
    body: { request: "check status" },
    created_at: "2026-08-28T12:00:00Z",
  }];
  const prompt = polisTurnPrompt(messages);
  assert.match(prompt, /"id":7/);
  assert.match(prompt, /"from":"operator"/);
  assert.match(prompt, /check status/);
});

test("turn prompt handles an empty mailbox", () => {
  assert.match(polisTurnPrompt([]), /no unread messages/i);
});

test("wakeup prompt identifies the idle trigger and asks for autonomous work", () => {
  const prompt = polisWakeupPrompt();
  assert.match(prompt, /automatic wakeup prompt/i);
  assert.match(prompt, /no mailbox message arrived/i);
  assert.match(prompt, /continue pursuing your charter autonomously/i);
});
