import path from "node:path";

export interface Config {
  polisUrl: string;
  agentToken: string;
  workspace: string;
  charterPath: string;
  additionalInstructionsPath?: string;
  wakeupSeconds?: number;
  agentDir: string;
  sessionDir: string;
  model?: string;
  thinking?: ThinkingLevel;
  compactionReserveTokens?: number;
  compactionKeepRecentTokens?: number;
  authFile?: string;
}

export const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
export type ThinkingLevel = typeof thinkingLevels[number];

interface RuntimeArgs {
  model?: string;
  thinking?: ThinkingLevel;
  compactionReserveTokens?: number;
  compactionKeepRecentTokens?: number;
}

export function loadConfig(
  environment: NodeJS.ProcessEnv = process.env,
  args: readonly string[] = [],
): Config {
  const runtime = parseRuntimeArgs(args);
  const polisUrl = required(environment, "POLIS_URL").replace(/\/+$/, "");
  const agentToken = required(environment, "POLIS_AGENT_TOKEN");
  const workspace = path.resolve(required(environment, "POLIS_WORKSPACE"));
  const charterPath = path.resolve(required(environment, "POLIS_CHARTER_PATH"));
  const additionalInstructionsPath = optional(environment.POLIS_ADDITIONAL_INSTRUCTIONS_PATH);
  const wakeup = optional(environment.POLIS_WAKEUP_SECONDS);
  const model = runtime.model ?? optional(environment.POLIS_PI_MODEL);
  const authFile = optional(environment.POLIS_PI_AUTH_FILE);

  return {
    polisUrl,
    agentToken,
    workspace,
    charterPath,
    ...(additionalInstructionsPath === undefined
      ? {}
      : { additionalInstructionsPath: path.resolve(additionalInstructionsPath) }),
    ...(wakeup === undefined
      ? {}
      : { wakeupSeconds: parsePositiveInteger("POLIS_WAKEUP_SECONDS", wakeup) }),
    agentDir: path.join(workspace, ".polis", "pi-agent"),
    sessionDir: path.join(workspace, ".polis", "pi-sessions"),
    ...(model === undefined ? {} : { model }),
    ...(runtime.thinking === undefined ? {} : { thinking: runtime.thinking }),
    ...(runtime.compactionReserveTokens === undefined
      ? {}
      : { compactionReserveTokens: runtime.compactionReserveTokens }),
    ...(runtime.compactionKeepRecentTokens === undefined
      ? {}
      : { compactionKeepRecentTokens: runtime.compactionKeepRecentTokens }),
    ...(authFile === undefined ? {} : { authFile: path.resolve(authFile) }),
  };
}

function parseRuntimeArgs(args: readonly string[]): RuntimeArgs {
  let model: string | undefined;
  let thinking: ThinkingLevel | undefined;
  let compactionReserveTokens: number | undefined;
  let compactionKeepRecentTokens: number | undefined;

  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === "--model") {
      model = flagValue("--model", args[index + 1]);
      index += 1;
    } else if (argument?.startsWith("--model=")) {
      model = flagValue("--model", argument.slice("--model=".length));
    } else if (argument === "--thinking") {
      thinking = parseThinking(flagValue("--thinking", args[index + 1]));
      index += 1;
    } else if (argument?.startsWith("--thinking=")) {
      thinking = parseThinking(flagValue("--thinking", argument.slice("--thinking=".length)));
    } else if (argument === "--compaction-reserve-tokens") {
      compactionReserveTokens = parsePositiveInteger(
        "--compaction-reserve-tokens",
        flagValue("--compaction-reserve-tokens", args[index + 1]),
      );
      index += 1;
    } else if (argument?.startsWith("--compaction-reserve-tokens=")) {
      compactionReserveTokens = parsePositiveInteger(
        "--compaction-reserve-tokens",
        flagValue("--compaction-reserve-tokens", argument.slice("--compaction-reserve-tokens=".length)),
      );
    } else if (argument === "--compaction-keep-recent-tokens") {
      compactionKeepRecentTokens = parsePositiveInteger(
        "--compaction-keep-recent-tokens",
        flagValue("--compaction-keep-recent-tokens", args[index + 1]),
      );
      index += 1;
    } else if (argument?.startsWith("--compaction-keep-recent-tokens=")) {
      compactionKeepRecentTokens = parsePositiveInteger(
        "--compaction-keep-recent-tokens",
        flagValue("--compaction-keep-recent-tokens", argument.slice("--compaction-keep-recent-tokens=".length)),
      );
    } else {
      throw new Error(`unknown argument ${JSON.stringify(argument)}`);
    }
  }

  return {
    ...(model === undefined ? {} : { model }),
    ...(thinking === undefined ? {} : { thinking }),
    ...(compactionReserveTokens === undefined ? {} : { compactionReserveTokens }),
    ...(compactionKeepRecentTokens === undefined ? {} : { compactionKeepRecentTokens }),
  };
}

function flagValue(flag: string, value: string | undefined): string {
  const normalized = optional(value);
  if (normalized === undefined || normalized.startsWith("--")) {
    throw new Error(`${flag} requires a value`);
  }
  return normalized;
}

function parseThinking(value: string): ThinkingLevel {
  if ((thinkingLevels as readonly string[]).includes(value)) {
    return value as ThinkingLevel;
  }
  throw new Error(`invalid --thinking level ${JSON.stringify(value)}; expected ${thinkingLevels.join(", ")}`);
}

function parsePositiveInteger(flag: string, value: string): number {
  const parsed = Number(value);
  if (Number.isSafeInteger(parsed) && parsed > 0) {
    return parsed;
  }
  throw new Error(`${flag} must be a positive integer`);
}

function required(environment: NodeJS.ProcessEnv, name: string): string {
  const value = optional(environment[name]);
  if (value === undefined) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function optional(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed === "" ? undefined : trimmed;
}
