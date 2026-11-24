import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

test("Space document routes and storage stay fixed across other tabs and location changes", async () => {
  const source = await readFile(new URL("../assets/space-context.mjs", import.meta.url), "utf8");
  const previous = globalThis.location, previousFetch = globalThis.fetch;
  try {
    const a = "a".repeat(32), b = "b".repeat(32);
    globalThis.location = {pathname:`/s/${a}/`};
    const A = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}#a`);
    globalThis.location = {pathname:`/s/${b}/`};
    const B = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}#b`);
    assert.equal(A.spaceURL("/v1/rooms"), `/s/${a}/v1/rooms`);
    assert.equal(B.spaceURL("/v1/updates?service_epoch=e"), `/s/${b}/v1/updates?service_epoch=e`);
    assert.equal(A.spaceURL("/access"), `/s/${a}/access`);
    for (const path of ["/ui/app.js", "https://example.com/v1/rooms", "//example.com/image", `/s/${a}/v1/rooms`]) assert.equal(A.spaceURL(path), path);
    const values = new Map(), storage = {getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)};
    const storageA = A.spaceStorage(storage), storageB = B.spaceStorage(storage);
    storageA.setItem("draft", "A"); storageB.setItem("draft", "B");
    assert.equal(storageA.getItem("draft"), "A"); assert.equal(storageB.getItem("draft"), "B");
    storageA.removeItem("draft"); assert.equal(storageB.getItem("draft"), "B");
    storage.setItem("legacy", "default draft");
    globalThis.fetch = async () => ({ok:true,json:async()=>({can_create:false,default_space_id:a})});
    await A.mountSpacePicker(); await B.mountSpacePicker();
    assert.equal(storageA.getItem("legacy"), "default draft");
    assert.equal(storageB.getItem("legacy"), undefined);
    storageA.removeItem("legacy"); assert.equal(storageA.getItem("legacy"), undefined);
  } finally { globalThis.fetch = previousFetch; if (previous === undefined) delete globalThis.location; else globalThis.location = previous; }
});
