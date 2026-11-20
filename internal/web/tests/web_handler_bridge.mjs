import assert from "node:assert/strict";
import { createInterface } from "node:readline";

// This protocol is a Go test adapter. Business UI behavior is checked through
// actual TCP HTTP; keep this script limited to its serialization/error contract.
const pending = new Map();
const lines = createInterface({ input: process.stdin });
lines.on("line", line => {
  const response = JSON.parse(line);
  const receipt = pending.get(response.id);
  assert.ok(receipt, `unexpected response ${response.id}`);
  pending.delete(response.id);
  if (response.status === 200) receipt.resolve(response.body);
  else receipt.reject(Object.assign(new Error(response.body.error), {
    code: response.body.code, status: response.status,
  }));
});
function call(id, path, body) {
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    process.stdout.write(JSON.stringify({ id, path, body }) + "\n");
  });
}
const body = { text: '准确正文\n"引号" \\ 路径', list: [1, null, "中文"], enabled: false };
const [echo, read] = await Promise.all([
  call(7, `/echo?${new URLSearchParams({ q: "中文 & 空格" })}`, body),
  call(19, "/read", null),
]);
assert.deepEqual(echo, body);
assert.deepEqual(read, { ok: true });
await assert.rejects(call(3, "/reject"), error =>
  error.message === "身份已失效" && error.code === "identity_mismatch" && error.status === 403);
assert.equal(pending.size, 0);
lines.close();
process.stdin.destroy();
