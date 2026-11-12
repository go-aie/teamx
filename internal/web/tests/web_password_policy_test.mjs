import {test} from "node:test";
import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import {assessPassword} from "../assets/password-policy.mjs";

const policy=JSON.parse(await readFile(new URL("../../room/access_password_policy.json",import.meta.url),"utf8"));
const cases=JSON.parse(await readFile(new URL("../../room/testdata/access_password_cases.json",import.meta.url),"utf8"));

test("browser uses the server creation-policy cases",()=>{
  for (const [index,input] of cases.entries()) assert.equal(assessPassword(input.value,policy).issue,input.issue,`case ${index}`);
});

test("strength guidance allows a lowercase phrase without mandatory character classes",()=>{
  assert.equal(assessPassword("orchidtrails",policy).issue,"");
  assert.equal(assessPassword("orchidtrails",policy).level,"fair");
  assert.equal(assessPassword("several uncommon orchard words",policy).level,"strong");
  assert.equal(assessPassword("aaaaaaaaaaab",policy).level,"weak");
});
