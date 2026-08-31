import assert from "node:assert/strict";
import test from "node:test";
import { mailboxWaitSeconds, nextWakeupAt, wakeupIsDue } from "../src/wakeup.js";

test("an omitted wakeup keeps using the ordinary mailbox long poll", () => {
  assert.equal(nextWakeupAt(1_000, undefined), undefined);
  assert.equal(mailboxWaitSeconds(1_000, undefined), 30);
  assert.equal(wakeupIsDue(1_000, undefined), false);
});

test("mailbox polling shortens as the wakeup deadline approaches", () => {
  const wakeupAt = nextWakeupAt(1_000, 45);
  assert.equal(wakeupAt, 46_000);
  assert.equal(mailboxWaitSeconds(1_000, wakeupAt), 30);
  assert.equal(mailboxWaitSeconds(41_001, wakeupAt), 5);
  assert.equal(mailboxWaitSeconds(46_000, wakeupAt), 0);
});

test("wakeup becomes due only at its deadline", () => {
  const wakeupAt = nextWakeupAt(10_000, 2);
  assert.equal(wakeupIsDue(11_999, wakeupAt), false);
  assert.equal(wakeupIsDue(12_000, wakeupAt), true);
});
