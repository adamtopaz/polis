import type { SettingsManager } from "@earendil-works/pi-coding-agent";

export interface CompactionOverrides {
  compactionReserveTokens?: number;
  compactionKeepRecentTokens?: number;
}

export function applyCompactionOverrides(
  settingsManager: SettingsManager,
  overrides: CompactionOverrides,
): ReturnType<SettingsManager["getCompactionSettings"]> {
  settingsManager.applyOverrides({
    compaction: {
      ...(overrides.compactionReserveTokens === undefined
        ? {}
        : { reserveTokens: overrides.compactionReserveTokens }),
      ...(overrides.compactionKeepRecentTokens === undefined
        ? {}
        : { keepRecentTokens: overrides.compactionKeepRecentTokens }),
    },
  });
  return settingsManager.getCompactionSettings();
}
