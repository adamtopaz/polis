#!/usr/bin/env node

import { chmod, copyFile, mkdir, readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import {
  createAgentSession,
  DefaultResourceLoader,
  ModelRuntime,
  resolveCliModel,
  SessionManager,
  SettingsManager,
} from "@earendil-works/pi-coding-agent";
import { loadConfig } from "./config.js";
import { PolisApiError, PolisClient } from "./polis.js";
import { polisSystemPrompt, polisTurnPrompt, polisWakeupPrompt } from "./prompt.js";
import { withMailboxRetry } from "./retry.js";
import { applyCompactionOverrides } from "./settings.js";
import { mailboxWaitSeconds, nextWakeupAt, wakeupIsDue } from "./wakeup.js";

let stopping = false;
let abortActiveSession: (() => Promise<void>) | undefined;
const shutdown = new AbortController();

for (const signal of ["SIGINT", "SIGTERM"] as const) {
  process.once(signal, () => {
    stopping = true;
    shutdown.abort();
    void abortActiveSession?.();
  });
}

async function main(): Promise<void> {
  const config = loadConfig(process.env, process.argv.slice(2));
  await Promise.all([
    mkdir(config.agentDir, { recursive: true }),
    mkdir(config.sessionDir, { recursive: true }),
  ]);

  const polis = new PolisClient(config.polisUrl, config.agentToken);
  const callPolis = <T>(operation: () => Promise<T>): Promise<T> => withMailboxRetry(operation, {
    signal: shutdown.signal,
    onUnavailable: (error) => log("mailbox.unavailable", { error: errorMessage(error) }),
    onAvailable: (unavailableMs) => log("mailbox.available", { unavailable_ms: unavailableMs }),
  });
  const [charter, additionalInstructions, agent] = await Promise.all([
    readFile(config.charterPath, "utf8"),
    config.additionalInstructionsPath === undefined
      ? Promise.resolve(undefined)
      : readFile(config.additionalInstructionsPath, "utf8"),
    callPolis(() => polis.self(shutdown.signal)),
  ]);
  const authPath = config.authFile === undefined
    ? undefined
    : path.join(os.tmpdir(), "polis-pi-auth.json");
  if (authPath !== undefined && config.authFile !== undefined) {
    await copyFile(config.authFile, authPath);
    await chmod(authPath, 0o600);
  }
  const modelRuntime = await ModelRuntime.create({
    ...(authPath === undefined ? {} : { authPath }),
    modelsPath: path.join(config.agentDir, "models.json"),
    modelsStorePath: path.join(config.agentDir, "models-store.json"),
  });

  const selected = config.model === undefined
    ? undefined
    : resolveCliModel({ cliModel: config.model, modelRuntime });
  if (selected?.error !== undefined) {
    throw new Error(selected.error);
  }
  if (selected?.warning !== undefined) {
    log("model.warning", { message: selected.warning });
  }
  const thinkingLevel = config.thinking ?? selected?.thinkingLevel;
  const settingsManager = SettingsManager.create(config.workspace, config.agentDir);

  const resourceLoader = new DefaultResourceLoader({
    cwd: config.workspace,
    agentDir: config.agentDir,
    settingsManager,
    appendSystemPromptOverride: (base) => [
      ...base,
      polisSystemPrompt(agent, charter, additionalInstructions),
    ],
  });
  await resourceLoader.reload();
  // Resource reload refreshes file-backed settings; runtime arguments are the final overrides.
  const compaction = applyCompactionOverrides(settingsManager, config);

  const sessionManager = SessionManager.continueRecent(config.workspace, config.sessionDir);
  const { session } = await createAgentSession({
    cwd: config.workspace,
    agentDir: config.agentDir,
    modelRuntime,
    sessionManager,
    settingsManager,
    resourceLoader,
    tools: ["read", "bash", "edit", "write", "grep", "find", "ls"],
    ...(selected?.model === undefined ? {} : { model: selected.model }),
    ...(thinkingLevel === undefined ? {} : { thinkingLevel }),
  });
  abortActiveSession = () => session.abort();

  const unsubscribe = session.subscribe((event) => {
    if (event.type === "tool_execution_start") {
      log("tool.start", { tool: event.toolName, call: event.toolCallId });
    } else if (event.type === "tool_execution_end") {
      log("tool.end", { tool: event.toolName, call: event.toolCallId, error: event.isError });
    }
  });

  try {
    log("runtime.ready", {
      agent: agent.id,
      session: session.sessionId,
      model: `${session.model?.provider ?? "unknown"}/${session.model?.id ?? "unknown"}`,
      thinking: session.thinkingLevel,
      compaction_enabled: compaction.enabled,
      compaction_reserve_tokens: compaction.reserveTokens,
      compaction_keep_recent_tokens: compaction.keepRecentTokens,
      wakeup_seconds: config.wakeupSeconds,
    });
    log("runtime.waiting", { agent: agent.id });
    let wakeupAt = nextWakeupAt(Date.now(), config.wakeupSeconds);
    while (!stopping) {
      const waitSeconds = mailboxWaitSeconds(Date.now(), wakeupAt);
      const messages = await callPolis(() => polis.messages(waitSeconds, shutdown.signal));
      if (messages.length === 0) {
        if (!wakeupIsDue(Date.now(), wakeupAt)) {
          continue;
        }
        await callPolis(() => polis.journal("pi.wakeup.started", {
          session_id: session.sessionId,
          idle_seconds: config.wakeupSeconds,
        }, shutdown.signal));
        log("wakeup.start", {
          agent: agent.id,
          session: session.sessionId,
          idle_seconds: config.wakeupSeconds,
        });
        await session.prompt(polisWakeupPrompt());
        if (stopping) {
          return;
        }
        if (session.agent.state.errorMessage !== undefined) {
          throw new Error(session.agent.state.errorMessage);
        }
        await callPolis(() => polis.journal("pi.wakeup.completed", {
          session_id: session.sessionId,
          session_file: session.sessionFile,
          model_provider: session.model?.provider,
          model_id: session.model?.id,
          idle_seconds: config.wakeupSeconds,
        }, shutdown.signal));
        log("wakeup.complete", { agent: agent.id });
        wakeupAt = nextWakeupAt(Date.now(), config.wakeupSeconds);
        log("runtime.waiting", { agent: agent.id });
        continue;
      }
      const messageIds = messages.map((message) => message.id);
      await callPolis(() => polis.journal("pi.turn.started", {
        session_id: session.sessionId,
        message_ids: messageIds,
      }, shutdown.signal));
      log("turn.start", {
        agent: agent.id,
        session: session.sessionId,
        unread_messages: messages.length,
      });
      await session.prompt(polisTurnPrompt(messages));
      if (stopping) {
        return;
      }
      if (session.agent.state.errorMessage !== undefined) {
        throw new Error(session.agent.state.errorMessage);
      }

      const lastMessage = messages.at(-1)?.id;
      await callPolis(() => polis.journal("pi.turn.completed", {
        session_id: session.sessionId,
        session_file: session.sessionFile,
        model_provider: session.model?.provider,
        model_id: session.model?.id,
        message_ids: messageIds,
        messages_completed: messages.length,
      }, shutdown.signal));
      if (lastMessage !== undefined) {
        await callPolis(() => polis.acknowledge(lastMessage, shutdown.signal));
      }
      log("turn.complete", { agent: agent.id, messages_acknowledged: messages.length });
      wakeupAt = nextWakeupAt(Date.now(), config.wakeupSeconds);
      log("runtime.waiting", { agent: agent.id });
    }
  } catch (error) {
    if (stopping) {
      log("runtime.stopped", { agent: agent.id });
      return;
    }
    if (error instanceof PolisApiError && error.status === 401) {
      log("runtime.revoked", { agent: agent.id });
      return;
    }
    throw error;
  } finally {
    unsubscribe();
    abortActiveSession = undefined;
    session.dispose();
  }
}

function log(event: string, fields: Record<string, unknown>): void {
  process.stdout.write(`${JSON.stringify({ time: new Date().toISOString(), event, ...fields })}\n`);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

main().catch((error: unknown) => {
  process.stderr.write(`${JSON.stringify({ time: new Date().toISOString(), event: "runtime.error", message: errorMessage(error) })}\n`);
  process.exitCode = 1;
});
