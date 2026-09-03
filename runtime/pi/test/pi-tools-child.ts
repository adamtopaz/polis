import assert from "node:assert/strict";
import { lstat, readlink } from "node:fs/promises";
import path from "node:path";
import {
  configurePiAgentDir,
  importPiCodingAgent,
  reconcileManagedFd,
} from "../src/pi-runtime.js";

const scenario = required("POLIS_TEST_SCENARIO");
const workspace = required("POLIS_TEST_WORKSPACE");
const agentDir = path.join(workspace, ".polis", "pi-agent");
const packagedFd = required("POLIS_PI_FD_PATH");
process.env.PI_OFFLINE = "1";

if (scenario === "old-broken") {
  process.env.PI_CODING_AGENT_DIR = agentDir;
  const { createFindTool } = await import("@earendil-works/pi-coding-agent");
  const find = createFindTool(workspace);
  await assert.rejects(
    find.execute("old-broken", { pattern: "*.txt", path: "." }),
    /Failed to run fd:.*ENOENT/,
  );
  process.stdout.write(`${JSON.stringify({ scenario, oldFailure: "ENOENT" })}\n`);
} else if (scenario === "ready") {
  const configured = configurePiAgentDir(agentDir);
  assert.equal(configured, agentDir);
  const managedFd = await reconcileManagedFd(workspace, configured, packagedFd);
  const {
    createFindTool,
    createGrepTool,
    createLsTool,
    createReadTool,
  } = await importPiCodingAgent(configured);

  const findResult = await createFindTool(workspace).execute(
    "find-smoke",
    { pattern: "*.txt", path: "." },
  );
  const grepResult = await createGrepTool(workspace).execute(
    "grep-smoke",
    { pattern: "unique-polis-find-smoke", path: ".", literal: true },
  );
  const lsResult = await createLsTool(workspace).execute("ls-smoke", { path: "." });
  const readResult = await createReadTool(workspace).execute(
    "read-smoke",
    { path: "needle.txt" },
  );

  const findText = text(findResult);
  const grepText = text(grepResult);
  const lsText = text(lsResult);
  const readText = text(readResult);
  assert.match(findText, /(?:^|\n)needle\.txt(?:\n|$)/);
  assert.match(grepText, /needle\.txt:1:\s*unique-polis-find-smoke/);
  assert.match(lsText, /needle\.txt/);
  assert.match(readText, /unique-polis-find-smoke/);
  assert.equal((await lstat(managedFd)).isSymbolicLink(), true);
  assert.equal(await readlink(managedFd), packagedFd);

  process.stdout.write(`${JSON.stringify({
    scenario,
    configured,
    managedFd,
    tools: ["find", "grep", "ls", "read"],
  })}\n`);
} else if (scenario === "conflict") {
  const alternate = path.join(workspace, ".polis", "other-agent");
  configurePiAgentDir(agentDir);
  await importPiCodingAgent(agentDir);
  assert.throws(
    () => configurePiAgentDir(alternate),
    /Pi is already initialized.*refusing/,
  );
  process.env.PI_CODING_AGENT_DIR = alternate;
  assert.throws(
    () => importPiCodingAgent(agentDir),
    /PI_CODING_AGENT_DIR is already configured.*refusing/,
  );
  process.stdout.write(`${JSON.stringify({ scenario, conflictRejected: true })}\n`);
} else if (scenario === "preconfigured-conflict") {
  const alternate = path.join(workspace, ".polis", "other-agent");
  process.env.PI_CODING_AGENT_DIR = alternate;
  assert.throws(
    () => configurePiAgentDir(agentDir),
    /PI_CODING_AGENT_DIR is already configured.*refusing/,
  );
  assert.equal(process.env.PI_CODING_AGENT_DIR, alternate);
  process.stdout.write(`${JSON.stringify({ scenario, conflictRejected: true })}\n`);
} else if (scenario === "removed-after-import") {
  configurePiAgentDir(agentDir);
  await importPiCodingAgent(agentDir);
  delete process.env.PI_CODING_AGENT_DIR;
  assert.throws(
    () => importPiCodingAgent(agentDir),
    /PI_CODING_AGENT_DIR was removed/,
  );
  process.stdout.write(`${JSON.stringify({ scenario, removalRejected: true })}\n`);
} else {
  throw new Error(`unknown test scenario ${JSON.stringify(scenario)}`);
}

function text(result: { content: Array<{ type: string; text?: string }> }): string {
  return result.content
    .filter((entry) => entry.type === "text")
    .map((entry) => entry.text ?? "")
    .join("\n");
}

function required(name: string): string {
  const value = process.env[name];
  if (value === undefined || value === "") {
    throw new Error(`${name} is required`);
  }
  return value;
}
