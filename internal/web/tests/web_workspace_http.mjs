import assert from "node:assert/strict";
import { request } from "../assets/client.mjs";
import { launch } from "./web_app_harness.mjs";

// Real HTTP + actual app events, with a DOM substitute. Browser evidence is
// recorded separately. No credential values are accepted by this driver.
const endpoint = process.env.TEAMX_WEB_ENDPOINT;
const api = { request: (path, payload) => request(path, payload, (url, options) => fetch(endpoint + url, options)) };
const ui = await launch(api);
async function ready() {
  for (let i=0; i<1200; i++) {
    await ui.refresh();
    let list;
    try { list = await api.request("/v1/rooms"); } catch (error) { if (error.code !== "busy") throw error; }
    if (!list) { await new Promise(resolve => setTimeout(resolve,100)); continue; }
    if (!list.busy && list.rooms.some(room => room.service) && !ui.el("new-room").disabled) return list;
    await new Promise(resolve => setTimeout(resolve,100));
  }
  throw Error("HTTP manager did not settle");
}
if (process.env.TEAMX_WORKSPACE_UI_PHASE === "create") {
  await ui.create("Workspace-A"); await ready();
  await ui.draft("Read the new message using the TeamX CLI. Remain silent publicly. Use bash to write the current absolute working directory into plain-cwd.txt. Do not access GitLab or any repository. Reply privately with PLAIN-READY.");
  await ui.el("compose").emit("submit");
} else {
  const catalogBefore = await ready();
  const origin = process.env.TEAMX_WORKSPACE_GITLAB_URL;
  const connection={id:process.env.TEAMX_WORKSPACE_CONNECTION_ID};
  await ui.el("open-workspace").emit("click");
  ui.el("workspace-repository").checked=true;
  await ui.el("workspace-repository").emit("change");
  ui.el("workspace-repo-url").value=process.env.TEAMX_WORKSPACE_REPOSITORY;
  ui.el("workspace-gitlab").checked=true;
  await ui.el("workspace-gitlab").emit("change");
  ui.el("workspace-connection").value=connection.id;
  await ui.el("workspace-connection").emit("change");
  await ui.el("workspace-form").emit("submit");
  const catalog=await ready();
  const room=catalog.rooms.find(room=>room.name==="Workspace-A");
  const q=new URLSearchParams({ service_epoch:catalog.service_epoch,caller_id:catalog.operator,room_id:room.room_id,room_instance:room.room_instance });
  const ws=await api.request(`/v1/workspace?${q}`);
  assert.equal(ws.operation?.status,"completed", JSON.stringify(ws.operation));
  assert.equal(ws.workspace.repository.url,`${process.env.TEAMX_WORKSPACE_GITLAB_URL}/${process.env.TEAMX_WORKSPACE_PROJECT}.git`);
  assert.equal(ws.workspace.binding.url,process.env.TEAMX_WORKSPACE_GITLAB_URL);
  assert.equal(ws.workspace.binding.project,process.env.TEAMX_WORKSPACE_PROJECT);
  assert.equal(ws.workspace.binding.platform_id,connection.id);
  assert.ok(ws.workspace.binding.project_id > 0);
}
console.log(JSON.stringify({status:"passed",phase:process.env.TEAMX_WORKSPACE_UI_PHASE,surface:"actual app events + real HTTP; DOM substitute"}));
