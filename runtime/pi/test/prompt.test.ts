import assert from "node:assert/strict";
import test from "node:test";
import { polisSystemPrompt, polisTurnPrompt } from "../src/prompt.js";
import type { Agent, Message } from "../src/polis.js";

const agent: Agent = {
  id: "researcher",
  charter: "Explore the problem",
  runtime: ["polis-pi-agent"],
  state: "active",
  phase: "running",
};

test("system prompt identifies the autonomous agent and preserves its charter", () => {
  const prompt = polisSystemPrompt(agent, "Discover useful things.\n");
  assert.match(prompt, /You are researcher/);
  assert.match(prompt, /Discover useful things\./);
  assert.match(prompt, /autonomous/);
  assert.match(prompt, /Never reveal credentials/);
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
