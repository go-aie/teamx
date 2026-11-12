import {test} from "node:test";
import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";

const html = await readFile(new URL("../assets/access.html",import.meta.url),"utf8");

test("password-manager fields cannot fall back to putting credentials in the URL",async()=>{
  assert.match(html,/<form id="login-form"[^>]*method="post"[^>]*action="\/v1\/access\/login"/);
  assert.match(html,/<input id="token"[^>]*autocomplete="current-password"/);
  const app = await readFile(new URL("../assets/index.html",import.meta.url),"utf8");
  for (const id of ["access-settings-form","access-confirm-form"]) {
    assert.match(app,new RegExp(`<form id="${id}"[^>]*method="post"[^>]*action="/v1/access/settings"`));
  }
});
