import path from "node:path";

export interface Config {
  polisUrl: string;
  agentToken: string;
  workspace: string;
  charterPath: string;
  agentDir: string;
  sessionDir: string;
  model?: string;
  authFile?: string;
  idleSeconds: number;
}

export function loadConfig(environment: NodeJS.ProcessEnv = process.env): Config {
  const polisUrl = required(environment, "POLIS_URL").replace(/\/+$/, "");
  const agentToken = required(environment, "POLIS_AGENT_TOKEN");
  const workspace = path.resolve(required(environment, "POLIS_WORKSPACE"));
  const charterPath = path.resolve(required(environment, "POLIS_CHARTER_PATH"));
  const idleSeconds = positiveInteger(environment.POLIS_PI_IDLE_SECONDS ?? "300", "POLIS_PI_IDLE_SECONDS");
  const model = optional(environment.POLIS_PI_MODEL);
  const authFile = optional(environment.POLIS_PI_AUTH_FILE);

  return {
    polisUrl,
    agentToken,
    workspace,
    charterPath,
    agentDir: path.join(workspace, ".polis", "pi-agent"),
    sessionDir: path.join(workspace, ".polis", "pi-sessions"),
    ...(model === undefined ? {} : { model }),
    ...(authFile === undefined ? {} : { authFile: path.resolve(authFile) }),
    idleSeconds,
  };
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

function positiveInteger(value: string, name: string): number {
  if (!/^\d+$/.test(value)) {
    throw new Error(`${name} must be a positive integer`);
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isSafeInteger(parsed) || parsed < 1) {
    throw new Error(`${name} must be a positive integer`);
  }
  return parsed;
}
