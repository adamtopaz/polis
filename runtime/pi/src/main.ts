#!/usr/bin/env node

import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import {
  createAgentSession,
  DefaultResourceLoader,
  ModelRuntime,
  resolveCliModel,
  SessionManager,
} from "@earendil-works/pi-coding-agent";
import { loadConfig } from "./config.js";
import { PolisApiError, PolisClient } from "./polis.js";
import { polisSystemPrompt, polisTurnPrompt } from "./prompt.js";

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
  const config = loadConfig();
  await Promise.all([
    mkdir(config.agentDir, { recursive: true }),
    mkdir(config.sessionDir, { recursive: true }),
  ]);

  const polis = new PolisClient(config.polisUrl, config.agentToken);
  const [charter, agent] = await Promise.all([
    readFile(config.charterPath, "utf8"),
    polis.self(),
  ]);
  const modelRuntime = await ModelRuntime.create({
    ...(config.authFile === undefined ? {} : { authPath: config.authFile }),
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

  const resourceLoader = new DefaultResourceLoader({
    cwd: config.workspace,
    agentDir: config.agentDir,
    appendSystemPromptOverride: (base) => [...base, polisSystemPrompt(agent, charter)],
  });
  await resourceLoader.reload();

  const sessionManager = SessionManager.continueRecent(config.workspace, config.sessionDir);
  const { session } = await createAgentSession({
    cwd: config.workspace,
    agentDir: config.agentDir,
    modelRuntime,
    sessionManager,
    resourceLoader,
    tools: ["read", "bash", "edit", "write", "grep", "find", "ls"],
    ...(selected?.model === undefined ? {} : { model: selected.model }),
    ...(selected?.thinkingLevel === undefined ? {} : { thinkingLevel: selected.thinkingLevel }),
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
    });
    log("runtime.waiting", { agent: agent.id });
    while (!stopping) {
      const messages = await polis.messages(30, shutdown.signal);
      if (messages.length === 0) {
        continue;
      }
      const messageIds = messages.map((message) => message.id);
      await polis.journal("pi.turn.started", {
        session_id: session.sessionId,
        message_ids: messageIds,
      }, shutdown.signal);
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
      if (lastMessage !== undefined) {
        await polis.acknowledge(lastMessage, shutdown.signal);
      }
      await polis.journal("pi.turn.completed", {
        session_id: session.sessionId,
        session_file: session.sessionFile,
        model_provider: session.model?.provider,
        model_id: session.model?.id,
        message_ids: messageIds,
        messages_acknowledged: messages.length,
      }, shutdown.signal);
      log("turn.complete", { agent: agent.id, messages_acknowledged: messages.length });
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

main().catch((error: unknown) => {
  const message = error instanceof Error ? error.message : String(error);
  process.stderr.write(`${JSON.stringify({ time: new Date().toISOString(), event: "runtime.error", message })}\n`);
  process.exitCode = 1;
});
