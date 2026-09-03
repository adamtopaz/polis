import { execFile } from "node:child_process";
import { constants, type Stats } from "node:fs";
import {
  access,
  lstat,
  mkdir,
  readlink,
  realpath,
  rename,
  symlink,
  unlink,
} from "node:fs/promises";
import path from "node:path";
import { randomUUID } from "node:crypto";
import { promisify } from "node:util";

const PI_AGENT_DIR_ENV = "PI_CODING_AGENT_DIR";
const initializationKey = Symbol.for("@polis/pi-runtime/initialization");
const execFileAsync = promisify(execFile);

type PiCodingAgent = typeof import("@earendil-works/pi-coding-agent");

interface PiInitialization {
  agentDir: string;
  modulePromise?: Promise<PiCodingAgent>;
}

function getInitialization(): PiInitialization | undefined {
  return (process as unknown as Record<PropertyKey, unknown>)[initializationKey] as
    PiInitialization | undefined;
}

function setInitialization(state: PiInitialization): void {
  (process as unknown as Record<PropertyKey, unknown>)[initializationKey] = state;
}

/**
 * Configure Pi's process-global agent directory before importing Pi.
 *
 * Pi 0.84.4 captures its managed-tools directory when the module loads, so a
 * process cannot safely switch this value after initialization.
 */
export function configurePiAgentDir(agentDir: string): string {
  const configured = path.resolve(agentDir);
  const state = getInitialization();
  const environmentValue = process.env[PI_AGENT_DIR_ENV];

  if (state !== undefined && state.agentDir !== configured) {
    throw new Error(
      `Pi is already initialized for ${JSON.stringify(state.agentDir)}; refusing ${JSON.stringify(configured)}`,
    );
  }
  if (
    environmentValue !== undefined
    && environmentValue !== ""
    && path.resolve(environmentValue) !== configured
  ) {
    throw new Error(
      `${PI_AGENT_DIR_ENV} is already configured for ${JSON.stringify(environmentValue)}; refusing ${JSON.stringify(configured)}`,
    );
  }
  if (state !== undefined && environmentValue === undefined) {
    throw new Error(`${PI_AGENT_DIR_ENV} was removed after Pi runtime initialization`);
  }

  process.env[PI_AGENT_DIR_ENV] = configured;
  if (state === undefined) {
    setInitialization({ agentDir: configured });
  }
  return configured;
}

/** Import Pi only after its process-global directory has been configured. */
export function importPiCodingAgent(agentDir: string): Promise<PiCodingAgent> {
  const configured = configurePiAgentDir(agentDir);
  const state = getInitialization();
  if (state === undefined || state.agentDir !== configured) {
    throw new Error("Pi runtime initialization state is inconsistent");
  }
  state.modulePromise ??= import("@earendil-works/pi-coding-agent");
  return state.modulePromise;
}

/**
 * Validate the immutable, wrapper-provided Nix fd and atomically make Pi's
 * owned managed entry point at it. Existing workspace entries are inspected
 * with lstat and are never executed or followed.
 */
export async function reconcileManagedFd(
  workspace: string,
  agentDir: string,
  packagedFd: string,
): Promise<string> {
  const validatedFd = await validatePackagedFd(packagedFd);
  const resolvedWorkspace = path.resolve(workspace);
  const resolvedAgentDir = path.resolve(agentDir);
  const expectedAgentDir = path.join(resolvedWorkspace, ".polis", "pi-agent");
  if (resolvedAgentDir !== expectedAgentDir) {
    throw new Error(
      `refusing managed fd outside configured agent directory ${JSON.stringify(expectedAgentDir)}`,
    );
  }

  await assertSafeDirectory(resolvedWorkspace, "workspace", false);
  const polisDir = await ensureOwnedChildDirectory(resolvedWorkspace, ".polis");
  const safeAgentDir = await ensureOwnedChildDirectory(polisDir, "pi-agent");
  const binDir = await ensureOwnedChildDirectory(safeAgentDir, "bin");
  const managedFd = path.join(binDir, "fd");

  const existing = await lstatIfExists(managedFd);
  if (existing !== undefined && !existing.isFile() && !existing.isSymbolicLink()) {
    throw new Error("managed fd must be a regular file or symbolic link; refusing replacement");
  }

  const temporaryFd = path.join(binDir, `.fd.polis-${process.pid}-${randomUUID()}.tmp`);
  let temporaryCreated = false;
  let installed = false;
  try {
    await symlink(validatedFd, temporaryFd, "file");
    temporaryCreated = true;
    await assertExactSymlink(temporaryFd, validatedFd, "temporary managed fd");
    await rename(temporaryFd, managedFd);
    temporaryCreated = false;
    installed = true;
    await assertExactSymlink(managedFd, validatedFd, "managed fd");
    return managedFd;
  } catch (error) {
    const cleanupErrors: unknown[] = [];
    if (temporaryCreated) {
      try {
        await unlink(temporaryFd);
      } catch (cleanupError) {
        if (!isErrno(cleanupError, "ENOENT")) {
          cleanupErrors.push(cleanupError);
        }
      }
    }
    if (installed) {
      try {
        if (await isExactSymlink(managedFd, validatedFd)) {
          await unlink(managedFd);
        }
      } catch (cleanupError) {
        cleanupErrors.push(cleanupError);
      }
    }
    if (cleanupErrors.length > 0) {
      throw new AggregateError([error, ...cleanupErrors], "managed fd reconciliation and cleanup failed");
    }
    throw error;
  }
}

export async function validatePackagedFd(packagedFd: string): Promise<string> {
  if (!path.isAbsolute(packagedFd) || path.normalize(packagedFd) !== packagedFd) {
    throw new Error("POLIS_PI_FD_PATH must be an absolute normalized path");
  }
  const storeRelative = path.relative("/nix/store", packagedFd);
  const storeParts = storeRelative.split(path.sep);
  if (
    storeRelative.startsWith("..")
    || path.isAbsolute(storeRelative)
    || storeParts.length !== 3
    || !/^[a-z0-9]{32}-fd-[^/]+$/.test(storeParts[0] ?? "")
    || storeParts[1] !== "bin"
    || storeParts[2] !== "fd"
  ) {
    throw new Error("POLIS_PI_FD_PATH must identify the packaged Nix fd executable");
  }

  let stats: Stats;
  try {
    stats = await lstat(packagedFd);
  } catch (error) {
    throw new Error(`packaged fd is missing or inaccessible: ${errorMessage(error)}`, { cause: error });
  }
  if (!stats.isFile() || stats.isSymbolicLink()) {
    throw new Error("packaged fd must be a regular file, not a symlink or special file");
  }
  if ((stats.mode & 0o022) !== 0) {
    throw new Error("packaged fd must not be group- or world-writable");
  }
  const canonical = await realpath(packagedFd);
  if (canonical !== packagedFd) {
    throw new Error("packaged fd path must not traverse symbolic links");
  }
  try {
    await access(packagedFd, constants.R_OK | constants.X_OK);
  } catch (error) {
    throw new Error(`packaged fd is not readable and executable: ${errorMessage(error)}`, { cause: error });
  }

  try {
    const { stdout } = await execFileAsync(packagedFd, ["--version"], {
      encoding: "utf8",
      timeout: 5_000,
      maxBuffer: 64 * 1024,
    });
    if (!/^fd \d+(?:\.\d+)+$/.test(stdout.trim())) {
      throw new Error(`unexpected version output ${JSON.stringify(stdout.trim())}`);
    }
  } catch (error) {
    throw new Error(`packaged fd is not executable: ${errorMessage(error)}`, { cause: error });
  }
  return packagedFd;
}

async function ensureOwnedChildDirectory(parent: string, name: string): Promise<string> {
  const child = path.join(parent, name);
  try {
    await mkdir(child, { mode: 0o700 });
  } catch (error) {
    if (!isErrno(error, "EEXIST")) {
      throw error;
    }
  }
  await assertSafeDirectory(child, name, true);
  return child;
}

async function assertSafeDirectory(
  directory: string,
  label: string,
  requireOwnership: boolean,
): Promise<void> {
  const stats = await lstat(directory);
  if (!stats.isDirectory() || stats.isSymbolicLink()) {
    throw new Error(`${label} must be a real directory, not a symlink or special file`);
  }
  const canonical = await realpath(directory);
  if (canonical !== directory) {
    throw new Error(`${label} path must not traverse symbolic links`);
  }
  const effectiveUser = process.geteuid?.();
  if (requireOwnership && effectiveUser !== undefined && stats.uid !== effectiveUser) {
    throw new Error(`${label} directory must be owned by the runtime user`);
  }
  await access(directory, constants.W_OK | constants.X_OK);
}

async function assertExactSymlink(linkPath: string, target: string, label: string): Promise<void> {
  if (!(await isExactSymlink(linkPath, target))) {
    throw new Error(`${label} was not installed as the exact packaged fd symlink`);
  }
}

async function isExactSymlink(linkPath: string, target: string): Promise<boolean> {
  const stats = await lstatIfExists(linkPath);
  return stats?.isSymbolicLink() === true && await readlink(linkPath) === target;
}

async function lstatIfExists(filePath: string): Promise<Stats | undefined> {
  try {
    return await lstat(filePath);
  } catch (error) {
    if (isErrno(error, "ENOENT")) {
      return undefined;
    }
    throw error;
  }
}

function isErrno(error: unknown, code: string): error is NodeJS.ErrnoException {
  return error instanceof Error && "code" in error && error.code === code;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
