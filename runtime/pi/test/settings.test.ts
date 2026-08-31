import assert from "node:assert/strict";
import test from "node:test";
import { SettingsManager } from "@earendil-works/pi-coding-agent";
import { applyCompactionOverrides } from "../src/settings.js";

test("compaction arguments override selected settings and preserve the rest", () => {
  const settingsManager = SettingsManager.inMemory({
    compaction: {
      enabled: false,
      reserveTokens: 16384,
      keepRecentTokens: 20000,
    },
  });

  const settings = applyCompactionOverrides(settingsManager, {
    compactionReserveTokens: 32768,
  });

  assert.deepEqual(settings, {
    enabled: false,
    reserveTokens: 32768,
    keepRecentTokens: 20000,
  });
});

test("omitted compaction arguments preserve Pi defaults", () => {
  const settingsManager = SettingsManager.inMemory();

  const settings = applyCompactionOverrides(settingsManager, {});

  assert.deepEqual(settings, {
    enabled: true,
    reserveTokens: 16384,
    keepRecentTokens: 20000,
  });
});
