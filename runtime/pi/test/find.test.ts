import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readlink,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { reconcileManagedFd, validatePackagedFd } from "../src/pi-runtime.js";

const packagedFd = process.env.POLIS_PI_FD_PATH;
const unexecutableFd = process.env.POLIS_TEST_UNEXECUTABLE_FD;
const nixRuntimeOnly = packagedFd === undefined
  ? { skip: "POLIS_PI_FD_PATH is supplied by the repository-built Nix runtime" }
  : {};
const unexecutableNixFixtureOnly = unexecutableFd === undefined
  ? { skip: "POLIS_TEST_UNEXECUTABLE_FD is supplied by the repository-built Nix check" }
  : {};
const childPath = fileURLToPath(new URL("./pi-tools-child.js", import.meta.url));

test("packaged fd validation rejects missing and unexecutable artifacts", unexecutableNixFixtureOnly, async () => {
  await assert.rejects(
    validatePackagedFd("/nix/store/00000000000000000000000000000000-fd-missing/bin/fd"),
    /packaged fd is missing or inaccessible/,
  );
  await assert.rejects(
    validatePackagedFd(requiredUnexecutableFd()),
    /packaged fd is not readable and executable/,
  );
});

test("fresh runtime configures Pi before import and smoke-tests read-only tools", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  try {
    const legacyFd = path.join(fixture.legacyHome, ".pi", "agent", "bin", "fd");
    await mkdir(path.dirname(legacyFd), { recursive: true });
    const legacyContent = "#!/definitely/missing\n";
    await writeFile(legacyFd, legacyContent, { mode: 0o755 });

    const result = await runScenario("ready", fixture);
    assert.deepEqual(result.tools, ["find", "grep", "ls", "read"]);
    assert.equal(result.configured, fixture.agentDir);
    assert.equal(await readFile(legacyFd, "utf8"), legacyContent);
    assert.equal((await lstat(legacyFd)).isFile(), true);
    await assertManagedLink(fixture.agentDir);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("old persisted-helper failure is retained and safe reconciliation repairs it", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  try {
    const managedFd = await createManagedEntry(fixture.agentDir, "#!/definitely/missing\n");
    const oldResult = await runScenario("old-broken", fixture);
    assert.equal(oldResult.oldFailure, "ENOENT");

    const executionSentinel = path.join(fixture.root, "workspace-helper-executed");
    await writeFile(
      managedFd,
      `#!/bin/sh\nprintf touched > ${JSON.stringify(executionSentinel)}\n`,
      { mode: 0o755 },
    );
    await chmod(managedFd, 0o755);
    const result = await runScenario("ready", fixture);
    assert.deepEqual(result.tools, ["find", "grep", "ls", "read"]);
    await assert.rejects(lstat(executionSentinel), { code: "ENOENT" });
    await assertManagedLink(fixture.agentDir);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("outside symlink target is untouched while the owned fd entry is replaced", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  try {
    const sentinel = path.join(fixture.root, "outside-sentinel");
    const sentinelContent = "preserve-outside-target\n";
    await writeFile(sentinel, sentinelContent);
    const managedFd = path.join(fixture.agentDir, "bin", "fd");
    await mkdir(path.dirname(managedFd), { recursive: true });
    await symlink(sentinel, managedFd);

    await runScenario("ready", fixture);
    assert.equal(await readFile(sentinel, "utf8"), sentinelContent);
    assert.equal((await lstat(sentinel)).isFile(), true);
    await assertManagedLink(fixture.agentDir);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("directory at managed fd fails closed without replacement", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  try {
    const managedFd = path.join(fixture.agentDir, "bin", "fd");
    await mkdir(managedFd, { recursive: true });
    await assert.rejects(
      reconcileManagedFd(fixture.workspace, fixture.agentDir, requiredPackagedFd()),
      /managed fd must be a regular file or symbolic link; refusing replacement/,
    );
    assert.equal((await lstat(managedFd)).isDirectory(), true);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("unix socket at managed fd fails closed without replacement", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  const managedFd = path.join(fixture.agentDir, "bin", "fd");
  await mkdir(path.dirname(managedFd), { recursive: true });
  const server = createServer();
  try {
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(managedFd, resolve);
    });
    await assert.rejects(
      reconcileManagedFd(fixture.workspace, fixture.agentDir, requiredPackagedFd()),
      /managed fd must be a regular file or symbolic link; refusing replacement/,
    );
    assert.equal((await lstat(managedFd)).isSocket(), true);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("symlinked agent directory is rejected before touching its outside target", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  try {
    const polisDir = path.join(fixture.workspace, ".polis");
    const outsideAgentDir = path.join(fixture.root, "outside-agent");
    const sentinel = path.join(outsideAgentDir, "sentinel");
    await mkdir(polisDir, { recursive: true });
    await mkdir(outsideAgentDir);
    await writeFile(sentinel, "outside-agent-sentinel\n");
    await symlink(outsideAgentDir, fixture.agentDir, "dir");

    await assert.rejects(
      reconcileManagedFd(fixture.workspace, fixture.agentDir, requiredPackagedFd()),
      /pi-agent must be a real directory, not a symlink or special file/,
    );
    assert.equal(await readFile(sentinel, "utf8"), "outside-agent-sentinel\n");
    await assert.rejects(lstat(path.join(outsideAgentDir, "bin")), { code: "ENOENT" });
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

test("conflicting and late Pi process-global initialization fail explicitly", nixRuntimeOnly, async () => {
  const fixture = await createFixture();
  try {
    const conflict = await runScenario("conflict", fixture);
    const preconfigured = await runScenario("preconfigured-conflict", fixture);
    const removed = await runScenario("removed-after-import", fixture);
    assert.equal(conflict.conflictRejected, true);
    assert.equal(preconfigured.conflictRejected, true);
    assert.equal(removed.removalRejected, true);
  } finally {
    await rm(fixture.root, { recursive: true, force: true });
  }
});

interface Fixture {
  root: string;
  workspace: string;
  agentDir: string;
  legacyHome: string;
}

async function createFixture(): Promise<Fixture> {
  const root = await mkdtemp(path.join(os.tmpdir(), "polis-pi-find-"));
  const workspace = path.join(root, "workspace");
  const legacyHome = path.join(root, "legacy-home");
  await mkdir(workspace);
  await mkdir(legacyHome);
  await writeFile(path.join(workspace, "needle.txt"), "unique-polis-find-smoke\n");
  return {
    root,
    workspace,
    agentDir: path.join(workspace, ".polis", "pi-agent"),
    legacyHome,
  };
}

async function createManagedEntry(agentDir: string, content: string): Promise<string> {
  const managedFd = path.join(agentDir, "bin", "fd");
  await mkdir(path.dirname(managedFd), { recursive: true });
  await writeFile(managedFd, content, { mode: 0o755 });
  return managedFd;
}

async function assertManagedLink(agentDir: string): Promise<void> {
  const managedFd = path.join(agentDir, "bin", "fd");
  assert.equal((await lstat(managedFd)).isSymbolicLink(), true);
  assert.equal(await readlink(managedFd), requiredPackagedFd());
}

async function runScenario(scenario: string, fixture: Fixture): Promise<Record<string, unknown>> {
  const environment: NodeJS.ProcessEnv = {
    ...process.env,
    HOME: fixture.legacyHome,
    PI_OFFLINE: "1",
    POLIS_PI_FD_PATH: requiredPackagedFd(),
    POLIS_TEST_SCENARIO: scenario,
    POLIS_TEST_WORKSPACE: fixture.workspace,
  };
  delete environment.PI_CODING_AGENT_DIR;

  const child = spawn(process.execPath, [childPath], {
    env: environment,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => {
    stdout += chunk.toString();
  });
  child.stderr.on("data", (chunk) => {
    stderr += chunk.toString();
  });
  const result = await new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code, signal) => resolve({ code, signal }));
  });
  assert.equal(
    result.code,
    0,
    `child scenario ${scenario} failed (signal=${result.signal ?? "none"})\nstdout:\n${stdout}\nstderr:\n${stderr}`,
  );
  const line = stdout.trim().split("\n").at(-1);
  assert.ok(line, `child scenario ${scenario} did not return JSON`);
  return JSON.parse(line) as Record<string, unknown>;
}

function requiredPackagedFd(): string {
  assert.ok(packagedFd, "POLIS_PI_FD_PATH is required for the Nix runtime smoke");
  return packagedFd;
}

function requiredUnexecutableFd(): string {
  assert.ok(unexecutableFd, "POLIS_TEST_UNEXECUTABLE_FD is required for the Nix runtime check");
  return unexecutableFd;
}
