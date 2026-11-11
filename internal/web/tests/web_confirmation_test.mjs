import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";

test("confirmation cancels without a write, defaults focus to cancel and preserves the delete target", async () => {
  const api=fixture(); let deleted=false;
  api.hook=(path)=>{
    if(path==="/v1/rooms/delete"){deleted=true;return {};}
    if(path==="/v1/rooms" && deleted)return {service_epoch:api.epoch,operator:"tester",rooms:[],teams:[]};
  };
  const ui=await launch(api,new Map(),{confirmAction:null});
  const remove=()=>ui.el("room-list").children[0].children[1];
  let click=remove().emit("click");await settle();
  assert.equal(ui.el("confirmation-dialog").open,true);
  assert.equal(ui.document.activeElement,ui.el("confirmation-cancel"));
  assert.match(ui.el("confirmation-message").textContent,/工作区文件.*无法撤销/);
  assert.equal(deleted,false);
  await ui.el("confirmation-cancel").emit("click");await click;
  assert.equal(deleted,false);
  click=remove().emit("click");await settle();
  await ui.el("confirmation-accept").emit("click");await click;await settle();
  const writes=api.calls.filter(c=>c.path==="/v1/rooms/delete");
  assert.equal(writes.length,1);assert.equal(writes[0].body.room_instance,"instance-R");
  assert.equal(ui.el("confirmation-dialog").open,false);
});

test("Escape and closing confirmation dismiss it; duplicate requests cannot replace its target", async () => {
  const api=fixture(),ui=await launch(api,new Map(),{confirmAction:null});
  const remove=ui.el("room-list").children[0].children[1];
  let click=remove.emit("click");await settle();
  const title=ui.el("confirmation-title").textContent;
  await remove.emit("click");assert.equal(ui.el("confirmation-title").textContent,title);
  let prevented=false;
  await ui.el("confirmation-dialog").emit("cancel",{preventDefault(){prevented=true;}});await click;
  assert.equal(prevented,true);assert.equal(ui.el("confirmation-dialog").open,false);
  click=remove.emit("click");await settle();
  ui.el("confirmation-dialog").close();await click;
  assert.equal(api.calls.some(c=>c.path==="/v1/rooms/delete"),false);
});

test("confirming after service replacement cannot delete or restart a stale conversation", async () => {
  const api=fixture(),ui=await launch(api,new Map(),{confirmAction:null});
  let click=ui.el("room-list").children[0].children[1].emit("click");await settle();
  api.epoch="replacement";await ui.refresh();
  await ui.el("confirmation-accept").emit("click");await click;
  assert.equal(api.calls.some(c=>c.path==="/v1/rooms/delete"),false);
  click=ui.el("restart-discussion").emit("click");await settle();
  api.runtimeEpoch="replacement-runtime";await ui.refresh();
  await ui.el("confirmation-accept").emit("click");await click;
  assert.equal(api.calls.some(c=>c.path==="/v1/discussion/restart"),false);
});
