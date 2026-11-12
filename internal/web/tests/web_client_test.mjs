import { test } from "node:test";
import assert from "node:assert/strict";
import { request, readPages, createActivityReader, teamOpeningDraft, mentionRanges, memberMentionRanges, unseenMentions } from "../assets/client.mjs";

test("member mentions recognize complete known names and prefer longer identities", () => {
  const text="@开发助理 @reviewer，@开发助理甲 @reviewer2 mail@reviewer.com @@reviewer @first.last";
  const names=["开发", "开发助理", "reviewer", "first.last", "reviewer"];
  assert.deepEqual(memberMentionRanges(text,names).map(at=>text.slice(at.start,at.end)),["@开发助理","@reviewer","@first.last"]);
  assert.deepEqual(memberMentionRanges("@开发助理",["开发"]),[]);
  assert.deepEqual(memberMentionRanges("@unknown",names),[]);
});

test("human mentions match exact names with punctuation without matching emails or longer names", () => {
  const text = "请 @ann，确认一下；(@ann). 再问@ann：可以吗？";
  assert.deepEqual(mentionRanges(text, "ann").map(({ start, end }) => text.slice(start, end)), ["@ann", "@ann", "@ann"]);
  for (const text of ["mail@ann.com", "@anna", "@ann2", "@ann_smith", "@ann-smith", "@ann.smith", "@@ann", "@Ann"]) {
    assert.deepEqual(mentionRanges(text, "ann"), [], text);
  }
  assert.deepEqual(mentionRanges("@anyone", ""), []);
  for (const name of ["russellluo", "first.last", "a+b", "店长"]) {
    const text = `请 @${name} 确认 <script>alert('x')</script> @${name}。`;
    assert.equal(mentionRanges(text, name).length, 2, name);
  }
});

test("mention reminders follow viewing progress independently of replies and agent cursors", () => {
  const message = (seq, id, text, kind = "agent") => ({ seq, author: { id, kind }, text });
  const messages = [message(8, "alan", "@boss 还有一个问题"), message(3, "alan", "@boss 请确认"),
    message(5, "boss", "@boss 自己的消息", "human"), message(6, "qixi", "@other 请确认"),
    message(9, "colleague", "@boss 请看这里", "human"), message(10, "boss", "无关的新消息", "human")];
  assert.deepEqual(unseenMentions(messages.values(), "boss", new Set()).map(m => m.seq), [3, 8, 9]);
  assert.deepEqual(unseenMentions(messages, "boss", new Set([8])).map(m => m.seq), [3, 9]);
  assert.deepEqual(unseenMentions(messages, "boss", new Set([3, 8, 9])), []);
  assert.deepEqual(unseenMentions(messages, "boss", new Set([9]), 3).map(m => m.seq), [8]);
  assert.deepEqual(unseenMentions(messages.filter(m => m.seq > 8), "boss", new Set()).map(m => m.seq), [9]);
  assert.deepEqual(unseenMentions(messages, "other", new Set()).map(m => m.seq), [6]);
});

test("operator aliases highlight names while self-message exclusion uses the fixed identity", () => {
  const owner = "owner_" + "a".repeat(32), names = [owner,"russellluo","russellluo"];
  const text = `@russellluo，@${owner} @russellluo2 mail@russellluo.com`;
  assert.deepEqual(mentionRanges(text,names).map(at=>text.slice(at.start,at.end)),["@russellluo",`@${owner}`]);
  const messages = [{seq:1,text,author:{kind:"agent",id:"assistant"}}, {seq:2,text,author:{kind:"human",id:owner}}];
  assert.deepEqual(unseenMentions(messages,owner,new Set(),0,names).map(m=>m.seq),[1]);
  assert.deepEqual(unseenMentions(messages,owner,new Set([1]),0,names),[]);
});

test("seeding a team opening preserves existing drafts and uncertain sends", () => {
  assert.equal(teamOpeningDraft("", null, "opening"), "opening");
  assert.equal(teamOpeningDraft("   ", null, "opening"), "opening");
  assert.equal(teamOpeningDraft("my draft", null, "opening"), "my draft");
  assert.equal(teamOpeningDraft("", { client_message_id: "uncertain" }, "opening"), "");
  assert.equal(teamOpeningDraft("pending text", { client_message_id: "uncertain" }, "opening"), "pending text");
});

test("history pagination preserves the snapshot while newer messages arrive", async () => {
  const calls = [];
  const result = await readPages(async (params) => {
    calls.push(params);
    return calls.length === 1
      ? { messages: [{ seq: 40 }], snapshot_seq: 90, next_after: 40, has_more: true }
      : { messages: [{ seq: 90 }], snapshot_seq: 90, next_after: 90, has_more: false };
  }, { mode: "latest", latest: 50 });
  assert.deepEqual(calls[1], { mode: "range", after: 40, snapshot_seq: 90 });
  assert.equal(result.snapshot, 90);
  assert.deepEqual(result.messages.map((m) => m.seq), [40, 90]);
});

test("incomplete or inconsistent pages fail rather than silently truncate history", async () => {
  await assert.rejects(readPages(async () => {
    throw new Error("connection lost");
  }, { mode: "all" }), /connection lost/);
  await assert.rejects(readPages(async () => ({ messages: [], snapshot_seq: 4, next_after: 2, has_more: true }),
    { mode: "range", after: 2, snapshot_seq: 4 }), /没有前进/);
  await assert.rejects(readPages(async () => ({ messages: [], snapshot_seq: 5, has_more: false }),
    { mode: "range", after: 0, snapshot_seq: 4 }), /快照已变化/);
  assert.deepEqual(await readPages(async () => ({ messages: [], snapshot_seq: 0, has_more: false }),
    { mode: "range", after: 0, snapshot_seq: 0 }), { messages: [], snapshot: 0, clearedThrough: 0 });
});

test("clearing during pagination discards pages containing removed messages", async () => {
  let calls = 0;
  await assert.rejects(readPages(async () => ++calls === 1
    ? { messages: [{ seq: 1 }], snapshot_seq: 3, cleared_through: 0, next_after: 1, has_more: true }
    : { messages: [{ seq: 3 }], snapshot_seq: 3, cleared_through: 2, next_after: 3, has_more: false },
  { mode: "all" }), /消息已被清空/);
  const result = await readPages(async () => ({ messages: [], snapshot_seq: 3, cleared_through: 3, has_more: false }),
    { mode: "latest", latest: 50 });
  assert.deepEqual(result, { messages: [], snapshot: 3, clearedThrough: 3 });
});

test("uncertain sends are never automatically retried by the transport", async () => {
  const payload = { text: "hello", client_message_id: "original-id" };
  const calls = [];
  const fetcher = async (path, options) => {
    calls.push(JSON.parse(options.body));
    if (calls.length === 1) throw new Error("reply lost after commit");
    return { ok: true, json: async () => ({ seq: 1 }) };
  };
  await assert.rejects(request("/v1/send", payload, fetcher), /reply lost/);
  assert.equal(calls.length, 1);
  assert.deepEqual(await request("/v1/send", payload, fetcher), { seq: 1 });
  assert.deepEqual(calls, [payload, payload]);
});

test("timeout aborts a request; API errors retain their code", async () => {
  await assert.rejects(request("/v1/send", {}, async (_, { signal }) => {
    return new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("aborted"))));
  }, 5), /aborted/);
  await assert.rejects(request("/v1/send", {}, async () => ({ ok: false,
    json: async () => ({ code: "busy", error: "Room commit pending" }) })),
    (error) => error.code === "busy" && error.message === "Room commit pending");
});

test("picker cancellation aborts transport, including an already cancelled signal", async () => {
  const controller = new AbortController();
  const pending = request("/v1/platform/lark/chats", {}, async (_, { signal }) =>
    new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("cancelled")))), 1000, controller.signal);
  controller.abort();
  await assert.rejects(pending, /cancelled/);
  await assert.rejects(request("/v1/platform/lark/chats", {}, async (_, { signal }) => {
    assert.equal(signal.aborted, true);
    throw new Error("cancelled");
  }, 1000, controller.signal), /cancelled/);
});

test("late private output cannot replace a different member or a closed drawer", async () => {
  const resolvers = [], rendered = [];
  const reader = createActivityReader(() => new Promise(resolve => resolvers.push(resolve)),
    (view, member) => rendered.push({ view, member }));
  const a = { agent_id: "A", membership_id: "old-A" }, b = { agent_id: "B", membership_id: "B" };
  const first = reader.select(a);
  await Promise.resolve();
  const second = reader.select(b);
  await Promise.resolve();
  resolvers[0]({ text: "private-A" });
  await first;
  assert.deepEqual(rendered.at(-1), { view: { loading: true }, member: b });
  resolvers[1]({ text: "private-B" });
  await second;
  assert.equal(rendered.at(-1).view.data.text, "private-B");
  const third = reader.select({ agent_id: "A", membership_id: "new-A" });
  await Promise.resolve();
  const count = rendered.length;
  reader.close();
  resolvers[2]({ text: "new-private-A" });
  await third;
  await reader.refresh();
  assert.equal(rendered.length, count);
  assert.equal(resolvers.length, 3);
});

test("drawer polling shares a request and clears stale data when a read fails", async () => {
  let resolve, calls = 0;
  const rendered = [];
  const reader = createActivityReader(() => {
    calls++;
    if (calls > 1) throw new Error("member removed");
    return new Promise(done => { resolve = done; });
  }, view => rendered.push(view));
  const initial = reader.select({ agent_id: "A", membership_id: "A" });
  await Promise.resolve();
  assert.equal(reader.refresh(), initial);
  resolve({ last_output: { text: "saved" } });
  await initial;
  assert.equal(rendered.at(-1).data.last_output.text, "saved");
  await reader.refresh();
  assert.equal(rendered.at(-1).error.message, "member removed");
  assert.equal(rendered.at(-1).data, undefined);
});

test("room drafts survive reload independently, while uncertain sends keep the original route", async () => {
  const { createRoomDrafts, sameService } = await import("../assets/client.mjs");
  const saved = new Map(), storage = { getItem: key => saved.get(key), setItem: (key, value) => saved.set(key, value) };
  const a = { room_id: "a", room_instance: "A", service_epoch: "old" }, b = { room_id: "b", room_instance: "B", service_epoch: "new" };
  const drafts = createRoomDrafts(storage), da = drafts.get(a, "operator");
  Object.assign(da, { text: "draft A", replyTo: 7, replyLabel: "quote A", replyAuthor:"reviewer", pending: { info: a,
    operator: "operator", text: "draft A", client_message_id: "original" } });
  drafts.save(a, "operator", da); drafts.seed(b, "operator", "opening B");
  const restored = createRoomDrafts(storage), newA = { ...a, service_epoch: "another-activation" };
  const back = restored.get(newA, "operator");
  assert.equal(back.text, "draft A"); assert.equal(back.replyTo, 7); assert.equal(back.replyLabel, "quote A");
  assert.equal(back.replyAuthor,"reviewer");
  assert.deepEqual(back.pending.info, a); assert.equal(sameService(back.pending.info, newA), false);
  assert.equal(restored.get(b, "operator").text, "opening B");
  assert.equal(restored.get(a, "different-human").text, "");
  assert.equal(restored.get({ ...a, room_instance: "replacement-A" }, "operator").text, "");
  const db = restored.get(b, "operator"); db.text = ""; restored.save(b, "operator", db);
  createRoomDrafts(storage).seed(b, "operator", "must not refill after sending");
  assert.equal(createRoomDrafts(storage).get(b, "operator").text, "");
});

test("drafts work without storage and a new-room opening never overwrites an existing draft", async () => {
  const { createRoomDrafts } = await import("../assets/client.mjs");
  const drafts = createRoomDrafts({ getItem() { throw new Error("storage denied"); }, setItem() { throw new Error("full"); } });
  const room = { room_instance: "A" }, draft = drafts.get(room, "operator");
  draft.text = "keep me"; drafts.seed(room, "operator", "opening");
  assert.equal(drafts.get(room, "operator").text, "keep me");
});
