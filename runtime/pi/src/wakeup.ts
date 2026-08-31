const maximumMailboxWaitSeconds = 30;

export function nextWakeupAt(nowMs: number, wakeupSeconds: number | undefined): number | undefined {
  return wakeupSeconds === undefined ? undefined : nowMs + wakeupSeconds * 1000;
}

export function mailboxWaitSeconds(nowMs: number, wakeupAtMs: number | undefined): number {
  if (wakeupAtMs === undefined) {
    return maximumMailboxWaitSeconds;
  }
  const remainingMs = wakeupAtMs - nowMs;
  if (remainingMs <= 0) {
    return 0;
  }
  return Math.min(maximumMailboxWaitSeconds, Math.ceil(remainingMs / 1000));
}

export function wakeupIsDue(nowMs: number, wakeupAtMs: number | undefined): boolean {
  return wakeupAtMs !== undefined && nowMs >= wakeupAtMs;
}
