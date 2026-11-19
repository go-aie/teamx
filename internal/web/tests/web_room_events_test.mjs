import { test } from "node:test";
import assert from "node:assert/strict";
import { roomEventDisplayText } from "../assets/room-events.mjs";
import { launch } from "./web_app_harness.mjs";
import { fixture } from "./web_member_platform_fixture.mjs";

const title = "给本人飞书私聊发一条消息（罗彭）";
const summary = `定时任务到期：${title}`;
const due = {
  seq: 1, type: "event", author: { kind: "system", id: "calendar" }, created_at: "2026-11-19T08:53:00Z",
  text: `${summary}。run_id=run_due；请先阅读 teamx calendar --help，核对最新人类意图后用 calendar claim 领取，再办理并以 calendar finish 发布报告。`,
  event: { event_type: "calendar_due", operation_id: "run_due", result_ref: "calendar/run/run_due", target_memberships: ["a"] },
};

test("the conversation renders saved calendar events as readable summaries and retains execution data", async () => {
  const api = fixture(), saved = structuredClone(due);
  const reply = { seq: 2, reply_to: 1, author: { kind: "agent", id: "a" }, text: "已完成", created_at: due.created_at };
  api.hook = path => path === "/v1/read" ? { messages: [due, reply], snapshot_seq: 2, cleared_through: 0, has_more: false } : undefined;
  const ui = await launch(api);
  const node = ui.el("messages").children.find(node => node.id === "message-1");
  assert.ok(node);
  assert.equal(node.className, "room-event");
  assert.equal(node.textContent, summary);
  assert.ok(node.title);
  const reference = ui.el("messages").children.find(node => node.id === "message-2").querySelector(".message-reference").textContent;
  assert.ok(reference.endsWith(summary));
  assert.ok(!reference.includes("run_id"));
  assert.deepEqual(due, saved);
});

test("only the known calendar protocol suffix is removed; titles and unrelated text stay intact", () => {
  for (const message of [
    { ...due, type: "message" },
    { ...due, event: { ...due.event, event_type: "user_selection" } },
    { ...due, event: { ...due.event, operation_id: "another_run" } },
    { ...due, event: undefined },
    { ...due, text: summary },
    { ...due, text: `${summary}。run_id=用户写的内容` },
    { ...due, text: "已完成选择" },
  ]) assert.equal(roomEventDisplayText(message), message.text);

  const detailedTitle = "第一项。第二项；run_id=标题内容";
  const message = { ...due, text: due.text.replace(title, detailedTitle) };
  assert.equal(roomEventDisplayText(message), `定时任务到期：${detailedTitle}`);
});
