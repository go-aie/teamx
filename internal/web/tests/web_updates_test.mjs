import { test } from "node:test";
import assert from "node:assert/strict";
import { createUpdateScheduler, createSharedRead, updateIntervals } from "../assets/updates.mjs";

const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
function clock() {
  let time = 0, id = 0;
  const timers = new Map();
  return {
    performance: { now: () => time },
    setTimeout(fn, delay) { timers.set(++id, { fn, at: time + delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
    get timers() { return timers.size; },
    async advance(ms) {
      const end = time + ms;
      await flush();
      for (;;) {
        const next = [...timers].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!next) break;
        time = next[1].at; timers.delete(next[0]); next[1].fn(); await flush();
      }
      time = end; await flush();
    },
  };
}

for (const [topic, interval] of Object.entries(updateIntervals)) test(`${topic}: 10 seconds of 10ms notifications obey start intervals and then stop`, async () => {
  const env = clock(), updates = createUpdateScheduler(env), starts = [];
  updates.resource(topic, { read: () => { starts.push(env.performance.now()); } });
  // These are ordinary notifications, including repeated terminal/error frames.
  for (let elapsed = 0; elapsed <= 10000; elapsed += 10) {
    updates.invalidate(topic); await env.advance(10);
    assert.ok(env.timers <= 1);
  }
  await env.advance(interval);
  for (let i = 1; i < starts.length; i++) assert.ok(starts[i] - starts[i - 1] >= interval, starts.join(","));
  // Adjacent starts prove the bound for every window; also enumerate all windows
  // with refresh endpoints, including both ends, rather than a single total.
  for (let i = 0; i < starts.length; i++) for (let j = i; j < starts.length; j++) {
    assert.ok(j - i + 1 <= Math.floor((starts[j] - starts[i]) / interval) + 1);
  }
  const count = starts.length;
  await env.advance(60000);
  assert.equal(starts.length, count); assert.equal(env.timers, 0);
  updates.invalidate(topic); await flush();
  assert.equal(starts.at(-1), env.performance.now(), "first quiet notification has no extra delay");
});

test("slow reads serialize; hundreds of dirty marks retain one rate-limited followup", async () => {
  const env = clock(), updates = createUpdateScheduler(env), starts = [], responses = [];
  const r = updates.resource("activity", { read() {
    starts.push(env.performance.now()); const response = deferred(); responses.push(response); return response.promise;
  } });
  r.invalidate(); await flush();
  for (let i = 0; i < 100; i++) { await env.advance(10); r.invalidate(); }
  assert.deepEqual(starts, [0]); assert.equal(env.timers, 0);
  responses[0].resolve("first"); await flush();
  assert.equal(env.timers, 1);
  await env.advance(490); r.invalidate(); await env.advance(10);
  assert.deepEqual(starts, [0, 1500], "new marks do not postpone the existing wake");
  await env.advance(5000);
  assert.equal(starts.length, 2, "slow transport cannot overlap even after interval");
  responses[1].resolve("last"); await flush();
  await env.advance(60000); assert.equal(starts.length, 2); assert.equal(env.timers, 0);
});

test("failure backoff uses the later deadline and does not block an unrelated resource", async () => {
  const env = clock(), updates = createUpdateScheduler(env), starts = [], errors = [];
  let failing = true, chats = 0;
  updates.resource("catalog", { read() {
    starts.push(env.performance.now()); if (failing) throw Error("offline");
  }, onError: error => errors.push(error.message) });
  updates.resource("messages", { read: () => chats++ });
  updates.invalidate("catalog"); updates.invalidate("messages"); await flush();
  assert.equal(chats, 1);
  await env.advance(1490); updates.invalidate("catalog"); await env.advance(10);
  assert.deepEqual(starts, [0, 1500]);
  await env.advance(1999); assert.equal(starts.length, 2);
  failing = false; await env.advance(1);
  assert.deepEqual(starts, [0, 1500, 3500]); assert.deepEqual(errors, ["offline", "offline"]);
  await env.advance(60000); assert.equal(starts.length, 3); assert.equal(env.timers, 0);
});

test("manual entry/recovery and terminal notifications use one serial entry; exceptions are counted separately", async () => {
  const env = clock(), updates = createUpdateScheduler(env), starts = [], responses = [];
  const r = updates.resource("messages", { read() {
    starts.push(env.performance.now()); const d = deferred(); responses.push(d); return d.promise;
  } });
  const entry = r.refresh(), duplicateEntry = r.refresh();
  assert.equal(entry, duplicateEntry);
  await flush(); await env.advance(10); r.invalidate(); r.invalidate();
  const manual = r.refresh(), recovery = r.refresh();
  assert.equal(starts.length, 1);
  responses[0].resolve(1); await entry; await flush();
  assert.deepEqual(starts, [0, 10], "one explicit followup may bypass the ordinary interval");
  r.invalidate(); responses[1].resolve(2); await Promise.all([manual, recovery]);
  await env.advance(499); assert.equal(starts.length, 2);
  await env.advance(1); assert.deepEqual(starts, [0, 10, 510]);
  responses[2].resolve(3); await flush(); assert.equal(env.timers, 0);
});

for (const change of ["stream", "view", "membership", "write"]) test(`late ${change} reads cannot apply over a new context or receipt`, async () => {
  const env = clock(), updates = createUpdateScheduler(env), responses = [], applied = [];
  let scope = { view: "room-a", membership: "member-a" };
  const r = updates.resource("policy", { scope: () => scope, read() {
    const d = deferred(); responses.push(d); return d.promise;
  }, apply: value => applied.push(value) });
  const oldStream = updates.generation, old = r.refresh(); await flush();
  if (change === "stream") updates.reset();
  else if (change === "write") r.supersede("receipt");
  else scope = { ...scope, [change]: "new" };
  const fresh = r.refresh();
  responses[0].resolve("stale"); await old; await flush();
  assert.equal(applied.includes("stale"), false);
  assert.equal(responses.length, 2, "new read waits for old transport");
  responses[1].resolve("fresh"); await fresh;
  assert.deepEqual(applied, change === "write" ? ["receipt", "fresh"] : ["fresh"]);
  if (change === "stream") { updates.invalidate("policy", oldStream); await env.advance(60000); assert.equal(responses.length, 2); }
});

test("authoritative receipts suppress redundant reads; unknown operations only observe", async () => {
  const env = clock(), updates = createUpdateScheduler(env), applied = [], d = deferred();
  let reads = 0;
  const r = updates.resource("models", { read: () => { reads++; return d.promise; }, apply: value => applied.push(value) });
  const pending = r.refresh(); await flush();
  r.invalidate(); r.supersede({ operation_id: "original", status: "completed" });
  d.resolve({ operation_id: "original", status: "applying" }); await pending;
  await env.advance(60000);
  assert.equal(reads, 1); assert.deepEqual(applied, [{ operation_id: "original", status: "completed" }]);
});

test("hidden resources retain dirty until woken; identity failures stop retries until explicit entry", async () => {
  const env = clock(), updates = createUpdateScheduler(env);
  let visible = false, reads = 0, failing = true;
  const r = updates.resource("inbox", { enabled: () => visible, read() {
    reads++; if (failing) throw Object.assign(Error("forbidden"), { code: "forbidden" });
  } });
  r.invalidate(); await env.advance(10000); assert.equal(reads, 0); assert.equal(env.timers, 0);
  visible = true; r.wake(); await flush(); await env.advance(60000); assert.equal(reads, 1);
  r.invalidate(); await env.advance(60000); assert.equal(reads, 1, "permission errors stay stopped despite more notifications");
  failing = false; await r.refresh(); assert.equal(reads, 2);
  r.invalidate(); updates.pause(); await env.advance(60000); assert.equal(reads, 2);
  updates.resume(); await r.refresh(); assert.equal(reads, 3);
  r.invalidate(); updates.dispose(); await env.advance(60000); assert.equal(reads, 3); assert.equal(env.timers, 0);
});

test("pausing before dispatch cancels a queued read without starting its transport", async () => {
  const env = clock(), updates = createUpdateScheduler(env);
  let reads = 0;
  updates.resource("messages", { read: () => reads++ });
  updates.invalidate("messages"); updates.pause();
  await env.advance(60000); assert.equal(reads, 0); assert.equal(env.timers, 0);
});

test("one paginated message round holds the snapshot and serial slot across all HTTP pages", async () => {
  const env = clock(), updates = createUpdateScheduler(env), rounds = [], http = [], applied = [];
  const { readPages } = await import("../assets/client.mjs");
  const r = updates.resource("messages", { async read() {
    rounds.push(env.performance.now());
    return readPages(async params => {
      http.push(params); await Promise.resolve();
      if (params.after === 0) { r.invalidate(); return { messages: [{ seq: 1 }], snapshot_seq: 3, has_more: true, next_after: 1 }; }
      return { messages: [{ seq: 2 }, { seq: 3 }], snapshot_seq: 3, has_more: false, next_after: 3 };
    }, { after: rounds.length === 1 ? 0 : 3 });
  }, apply: data => applied.push(data.snapshot) });
  r.invalidate(); await flush();
  assert.deepEqual(rounds, [0]); assert.equal(http.length, 2); assert.equal(http[1].snapshot_seq, 3);
  await env.advance(500); assert.deepEqual(rounds, [0, 500]); assert.equal(http.length, 3);
  assert.deepEqual(applied, [3, 3]); assert.equal(env.timers, 0);
});

test("platform components share a request and confirmed result; failed reads retry without a cache", async () => {
  let scope = "epoch-a", calls = 0, d = deferred();
  const shared = createSharedRead({ scope: () => scope, read: () => { calls++; return d.promise; } });
  const workspace = shared.read(), settings = shared.read();
  assert.equal(workspace, settings); assert.equal(calls, 1);
  d.resolve({ platforms: ["a"] }); await workspace;
  assert.deepEqual(await shared.read({ cached: true }), { platforms: ["a"] }); assert.equal(calls, 1);
  d = deferred(); const failure = shared.read();
  d.reject(Error("offline")); await assert.rejects(failure, /offline/);
  assert.equal(shared.value, undefined, "a failed refresh cannot present the old cache as confirmed");
  d = deferred(); const retry = shared.read({ cached: true }); d.resolve({ platforms: ["b"] }); await retry;
  assert.equal(calls, 3);
  scope = "epoch-b"; assert.equal(shared.value, undefined);
});

test("a platform write fences a pending read and coalesces the necessary post-write observation", async () => {
  const responses = [], shared = createSharedRead({ scope: () => "epoch", read: () => {
    const d = deferred(); responses.push(d); return d.promise;
  } });
  const old = shared.read(); shared.invalidate();
  const fresh = shared.read(), duplicate = shared.read();
  responses[0].resolve({ platforms: ["stale"] }); await assert.rejects(old, { code: "stale_read" }); await flush();
  assert.equal(responses.length, 2);
  responses[1].resolve({ platforms: ["fresh"] });
  assert.deepEqual(await fresh, await duplicate); assert.deepEqual(shared.value, { platforms: ["fresh"] });
});

test("combined load keeps fast chat independent of slow details and failed configuration, then becomes idle", async () => {
  const env = clock(), updates = createUpdateScheduler(env), records = new Map();
  for (const topic of Object.keys(updateIntervals)) {
    const record = { starts: [], active: 0, maxActive: 0, applied: [], attempts: 0, version: 0 };
    records.set(topic, record);
    updates.resource(topic, {
      read() {
        record.starts.push(env.performance.now()); record.attempts++;
        record.maxActive = Math.max(record.maxActive, ++record.active);
        const version = record.version, failing = topic === "platforms" && record.attempts <= 2;
        return new Promise((resolve, reject) => env.setTimeout(() => {
          record.active--;
          if (failing) reject(Error("temporary provider failure")); else resolve(version);
        }, topic === "activity" ? 2500 : 20));
      },
      apply(value) { record.applied.push(value); },
    });
  }
  for (let time = 0; time <= 10000; time += 10) {
    for (const [topic, record] of records) { record.version++; updates.invalidate(topic); }
    await env.advance(10);
    assert.ok(env.timers <= records.size * 2, "one read completion and one wake per resource at most");
  }
  await env.advance(20000);
  for (const [topic, record] of records) {
    assert.equal(record.maxActive, 1, topic);
    assert.equal(record.applied.at(-1), record.version, `${topic} eventually applies its last dirty generation`);
    for (let i = 1; i < record.starts.length; i++) assert.ok(record.starts[i] - record.starts[i-1] >= updateIntervals[topic], topic);
    assert.deepEqual(record.applied, [...record.applied].sort((a,b) => a-b), topic);
  }
  assert.ok(records.get("messages").starts.length >= 20, "slow Activity and provider retries cannot starve chat");
  const settled = [...records.values()].map(r => r.starts.length);
  await env.advance(60000);
  assert.deepEqual([...records.values()].map(r => r.starts.length), settled);
  assert.equal(env.timers, 0);
  updates.dispose();
});
