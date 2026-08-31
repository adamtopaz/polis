import { setTimeout as sleep } from "node:timers/promises";
import { PolisApiError } from "./polis.js";

export interface MailboxRetryOptions {
  signal: AbortSignal;
  delayMs?: number;
  now?: () => number;
  wait?: (delayMs: number, signal: AbortSignal) => Promise<void>;
  onUnavailable?: (error: unknown) => void;
  onAvailable?: (unavailableMs: number) => void;
}

export async function withMailboxRetry<T>(
  operation: () => Promise<T>,
  options: MailboxRetryOptions,
): Promise<T> {
  const delayMs = options.delayMs ?? 1_000;
  const now = options.now ?? Date.now;
  const wait = options.wait ?? waitForRetry;
  let unavailableAt: number | undefined;

  while (true) {
    try {
      const result = await operation();
      if (unavailableAt !== undefined) {
        options.onAvailable?.(now() - unavailableAt);
      }
      return result;
    } catch (error) {
      if (options.signal.aborted || !isRetryableMailboxError(error)) {
        throw error;
      }
      if (unavailableAt === undefined) {
        unavailableAt = now();
        options.onUnavailable?.(error);
      }
      await wait(delayMs, options.signal);
    }
  }
}

export function isRetryableMailboxError(error: unknown): boolean {
  if (!(error instanceof PolisApiError)) {
    return error instanceof TypeError;
  }
  return error.status === 408 || error.status === 425 || error.status === 429 || error.status >= 500;
}

async function waitForRetry(delayMs: number, signal: AbortSignal): Promise<void> {
  await sleep(delayMs, undefined, { signal });
}
