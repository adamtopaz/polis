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
}

export function loadConfig(environment: NodeJS.ProcessEnv = process.env): Config {
  const polisUrl = required(environment, "POLIS_URL").replace(/\/+$/, "");
  const agentToken = required(environment, "POLIS_AGENT_TOKEN");
  const workspace = path.resolve(required(environment, "POLIS_WORKSPACE"));
  const charterPath = path.resolve(required(environment, "POLIS_CHARTER_PATH"));
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
