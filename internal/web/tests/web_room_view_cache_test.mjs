import { test } from "node:test";
import assert from "node:assert/strict";
import { createRoomViewCache, roomViewCacheLimits } from "../assets/room-view-cache.mjs";

const info = (id, epoch = "runtime") => ({ room_id: id, room_instance: `instance-${id}`, service_epoch: epoch });
const snapshot = (text = "message", count = 1) => ({ messages: Array.from({ length: count }, (_, i) => ({ seq: i + 1, text })),
  members: [{ agent_id: "agent", membership_id: "membership" }], lastSeq: count, clearedThrough: 0, position: { bottom: true } });

test("Room snapshots retain only a bounded recent window, detach mutations, and evict the least recently used Room", () => {
  const cache = createRoomViewCache({ rooms: 2 });
  cache.scope(["installation", "manager", "operator"]);
  const original = snapshot("original", 70); cache.put(info("a"), original); cache.put(info("b"), snapshot());
  original.messages[69].text = "mutated";
  const first = cache.get(info("a"));
  assert.equal(first.messages.length, roomViewCacheLimits.messages);
  assert.equal(first.messages[0].seq, 21); assert.equal(first.messages.at(-1).text, "original");
  first.members[0].agent_id = "mutated";
  cache.put(info("c"), snapshot());
  assert.equal(cache.get(info("b")), undefined);
  assert.equal(cache.get(info("a")).members[0].agent_id, "agent"); assert.equal(cache.size, 2);
});

test("serialized cache bytes stay bounded and a single oversized Room is not retained", () => {
  const sample = snapshot("体积");
  const bytes = new TextEncoder().encode(JSON.stringify(sample)).byteLength;
  const cache = createRoomViewCache({ bytes: bytes * 2 });
  for (let i = 0; i < 20; i++) { cache.put(info(String(i)), sample); assert.ok(cache.bytes <= bytes * 2); }
  assert.equal(cache.size, 2);
  cache.put(info("19"), snapshot("x".repeat(bytes * 3)));
  assert.equal(cache.get(info("19")), undefined); assert.equal(cache.size, 1);
});

test("installation, operator, manager, Room instance and runtime identities isolate display snapshots", () => {
  const cache = createRoomViewCache();
  for (const scope of [["i", "m", "a"], ["i", "m", "b"], ["j", "m", "b"], ["j", "n", "b"]]) {
    cache.scope(scope); assert.equal(cache.size, 0); cache.put(info("a"), snapshot());
    assert.equal(cache.get(info("a", "replacement")), undefined);
    assert.equal(cache.get({ ...info("a"), room_instance: "replacement" }), undefined);
  }
});

test("directory deletion, runtime replacement and known message clears discard previews", () => {
  const cache = createRoomViewCache();
  const room = id => ({ ...info(id), status: "ready", service: info(id) });
  for (const id of ["deleted", "replaced", "cleared", "kept"]) cache.put(info(id), snapshot());
  cache.reconcile([{ ...room("replaced"), service: info("replaced", "new") }, { ...room("cleared"), human_read: { cleared_through: 1 } }, room("kept")]);
  assert.equal(cache.size, 1); assert.ok(cache.get(info("kept")));
  cache.forget(info("kept")); assert.equal(cache.bytes, 0);
  cache.put(info("a"), snapshot()); assert.equal(cache.get(info("a"), 1), undefined);
});
