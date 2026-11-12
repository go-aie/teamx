import {test} from "node:test";
import assert from "node:assert/strict";
import {webcrypto} from "node:crypto";
import {randomUUID} from "../assets/random-id.mjs";

test("LAN HTTP identifiers use secure random bytes when randomUUID is absent", () => {
  let reads = 0;
  const source = {getRandomValues(bytes) {reads++; return webcrypto.getRandomValues(bytes);}};
  const values = new Set(Array.from({length:64}, () => randomUUID(source)));
  assert.equal(reads,64);
  assert.equal(values.size,64);
  for (const id of values) assert.match(id,/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
});

test("HTTPS identifiers use the browser randomUUID implementation", () => {
  assert.equal(randomUUID({randomUUID:()=>"native-id"}),"native-id");
});
