import {test} from "node:test";
import assert from "node:assert/strict";
import {createAttachmentDraft, clipboardImages, attachmentURL} from "../assets/attachment-drafts.mjs";
import {createRoomDrafts} from "../assets/client.mjs";
import {messageDisplayText} from "../assets/agent-names.mjs";
import {launch, settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";

const info = {room_id:"R", room_instance:"instance-R", service_epoch:"runtime"};
const limits = {max_images:4, image_bytes:2*1048576, total_bytes:4*1048576, mime_types:["image/png","image/jpeg","image/webp"]};
const file = (name = "screen.png", size = 20, type = "image/png") => new File([new Uint8Array(size)], name, {type});
const descriptor = (item, image = file()) => ({id:item.id, kind:"image", name:image.name, size:image.size, mime_type:image.type, width:20, height:10});
function controller(options = {}) {
  const draft = options.draft || {attachments:[]}, revoked = [], requests = [];
  const upload = async (identity, operator, item, image, signal) => {
    requests.push({identity, operator, item, image, signal}); return {attachment:descriptor(item,image)};
  };
  const model = createAttachmentDraft({draft, info, operator:"human", upload, states:async () => ({attachments:[]}),
    changed:() => {}, urls:{createObjectURL:()=>`blob:${requests.length}`,revokeObjectURL:url=>revoked.push(url)}, ...options});
  return {draft, model, revoked, requests};
}

test("upload completion order never changes image order; retries reuse identity and removal releases URLs", async () => {
  const waits = [], calls = [];
  const c = controller({upload:(identity, operator, item, image, signal) => {
    calls.push({identity, id:item.id, signal}); return new Promise((resolve,reject) => waits.push({resolve:()=>resolve({attachment:descriptor(item,image)}),reject}));
  }});
  c.model.addFiles([file("first.png"),file("second.png")],limits);
  const ids = c.draft.attachments.map(item=>item.id);
  assert.match(c.model.blockReason(),/上传/);
  waits[1].resolve(); waits[0].reject(Error("offline")); await settle();
  assert.deepEqual(c.draft.attachments.map(item=>item.id),ids);
  assert.match(c.model.blockReason(),/重试/);
  const retry = c.model.retry(ids[0]); waits[2].resolve(); await retry;
  assert.equal(calls[2].id,ids[0]); assert.deepEqual(calls[2].identity,info);
  assert.equal(c.model.blockReason(),"");
  c.model.remove(ids[0]); assert.equal(c.revoked.length,1);
  c.model.dispose(); assert.equal(c.revoked.length,2);
});

test("removed or disposed uploads cannot return to a draft after late completion", async () => {
  let finish;
  const c = controller({upload:(_,__,item,image,signal)=>new Promise(resolve=>{
    finish = () => { assert.equal(signal.aborted,true); resolve({attachment:descriptor(item,image)}); };
  })});
  c.model.addFiles([file()],limits); c.model.remove(c.draft.attachments[0].id); finish(); await settle();
  assert.deepEqual(c.draft.attachments,[]);
  assert.equal(c.revoked.length,1);
});

test("saved drafts contain metadata only; reload checks uploaded images with current epoch and keeps pending original", async () => {
  const saved = new Map(), storage = {getItem:key=>saved.get(key),setItem:(key,value)=>saved.set(key,value)};
  const drafts = createRoomDrafts(storage,()=>"installation"), draft = drafts.get(info,"human");
  const c = controller({draft}); c.model.addFiles([file()],limits); await settle();
  const id = draft.attachments[0].id;
  Object.assign(draft.attachments[0],{url:"blob:private",file:file(),controller:new AbortController()});
  draft.pending = {info,operator:"human",text:"",client_message_id:"original",attachments:[{id}]};
  drafts.save(info,"human",draft);
  const raw = [...saved.values()][0];
  assert.doesNotMatch(raw,/blob:|controller|"file"/);
  const next = {...info,service_epoch:"new"};
  const restored = createRoomDrafts(storage,()=>"installation").get(next,"human");
  let queried;
  const check = controller({draft:restored,info:next,states:async identity=>{
    queried=identity;return {attachments:[{id,status:"available",attachment:descriptor({id})}]};
  }});
  await check.model.restore(); assert.deepEqual(queried,next);
  assert.equal(check.model.items()[0].status,"ready");
  assert.deepEqual(restored.pending.info,info);
  check.model.remove(id); assert.equal(restored.attachments.length,1);
  c.model.dispose(); check.model.dispose();
});

test("reload distinguishes unavailable and memory-only images, retries state, and expiry blocks sending", async () => {
  const ids = ["img_"+"a".repeat(32),"img_"+"b".repeat(32)];
  let expired = false;
  const c = controller({draft:{attachments:[{...descriptor({id:ids[0]}),uploaded:true},{...descriptor({id:ids[1]}),uploaded:false}]},
    now:()=>100, states:async () => ({attachments:[{id:ids[0],status:expired ? "expired" : "available",attachment:descriptor({id:ids[0]})}]})});
  assert.equal(c.model.items()[1].status,"reselect");
  await c.model.restore(); assert.equal(c.model.items()[0].status,"ready");
  expired = true; await c.model.restore(true);
  assert.match(c.model.items()[0].error,/过期/); assert.ok(c.model.blockReason());
  c.model.remove(ids[0]); c.model.remove(ids[1]); assert.equal(c.model.blockReason(),""); c.model.dispose();
});

test("client limits reject unsupported, excessive and oversized selections without silently sending them", async () => {
  const c = controller();
  assert.match(c.model.addFiles([file("photo.heic",10,"image/heic")],limits)[0],/HEIC/);
  assert.match(c.model.addFiles([file("large.png",21)],{...limits,image_bytes:20})[0],/单张/);
  assert.match(c.model.addFiles([file(),file()],{...limits,total_bytes:30})[0],/总大小/);
  await settle(); assert.equal(c.draft.attachments.length,1);
  c.model.addFiles([file(),file(),file(),file()],limits); await settle();
  assert.equal(c.draft.attachments.length,4); assert.match(c.model.blockReason({...limits,max_images:2}),/当前限制/);
  c.model.dispose();
});

test("clipboard supports items and files without consuming text-only or remote HTML images", () => {
  const image = file(), item = {kind:"file",getAsFile:()=>image};
  assert.deepEqual(clipboardImages({items:[item],files:[image]}),[image]);
  assert.deepEqual(clipboardImages({files:[image]}),[image]);
  assert.deepEqual(clipboardImages({items:[{kind:"string"}],files:[]}),[]);
  assert.equal(messageDisplayText({text:"",attachments:[{},{}]},()=>""),"[图片 ×2]");
  const url = new URL(attachmentURL(info,"a+b", "img_"+"a".repeat(32),4),"http://test");
  assert.equal(url.searchParams.get("caller_id"),"a+b"); assert.equal(url.searchParams.get("seq"),"4");
});

test("clipboard deduplicates mirrored Files with different timestamps while retaining separate entries", () => {
  const one = {name:"image.png",size:120,type:"image/png",lastModified:1};
  const mirrored = {...one,lastModified:2};
  const item = {kind:"file",getAsFile:()=>one};
  assert.deepEqual(clipboardImages({items:[item],files:[mirrored]}),[one]);
  assert.deepEqual(clipboardImages({items:[item,item],files:[mirrored,{...mirrored,lastModified:3}]}),[one,one]);
  assert.deepEqual(clipboardImages({files:[one,mirrored]}),[one,mirrored]);
});

async function imageApp({failUpload=false,failSend=false,saved=new Map(),mode="managed"} = {}) {
  const api = fixture(), uploads = [], reads = [], original = api.request.bind(api);
  api.request = async (path, body) => {
    if (path === "/v1/ui" && mode === "single") return {...info,operator:"tester",profiles:["p"]};
    if (path === "/v1/rooms" && mode === "default-direct") {
      const catalog = await original(path,body);
      return {...catalog,default_assistant:{...info,status:"ready"},rooms:catalog.rooms.map(room=>({...room,is_direct:true,members:[{id:"a",name:"个人助理"}]}))};
    }
    if (path.startsWith("/v1/info?")) return {...info,attachments:limits};
    if (path === "/v1/attachments/state") { reads.push(body); return {attachments:body.ids.map(id=>({id,status:"available",attachment:descriptor({id})}))}; }
    if (path === "/v1/send") { api.calls.push({path,body:structuredClone(body)}); if(failSend)throw Error("lost receipt");return {seq:1}; }
    return original(path,body);
  };
  const ui = await launch(api,saved,{fetch:async (path,options)=>{
    if (!path.startsWith("/v1/attachments/upload?")) return {ok:true,json:async()=>({})};
    uploads.push({path,options});const id=new URL(path,"http://test").searchParams.get("attachment_id");
    return {ok:!failUpload,json:async()=>failUpload ? {message:"offline"} : {attachment:descriptor({id},options.body.get("file"))}};
  }});
  // The content-only harness has no native chooser. Browser tests exercise that gesture separately.
  ui.el("attachment-input").click=()=>{};
  ui.el("attachment-input").files=[file()];
  await ui.el("add-images").emit("click"); await ui.el("attachment-input").emit("change"); await settle();
  return {api,ui,uploads,reads};
}

test("app sends pure images, freezes attachment IDs and preserves uncertain receipt across reload", async () => {
  const {api,ui,uploads} = await imageApp({failSend:true});
  assert.equal(ui.el("send").disabled,false);
  assert.equal(ui.el("compose-more").disabled,false);
  await ui.el("compose").emit("submit");
  const pending = api.calls.find(call=>call.path==="/v1/send").body;
  assert.equal(pending.text,""); assert.equal(pending.attachments.length,1);
  assert.deepEqual(Object.keys(pending.attachments[0]),["id"]);
  await ui.el("compose").emit("submit");
  assert.deepEqual(api.calls.filter(call=>call.path==="/v1/send").at(-1).body,pending);
  assert.equal(uploads.length,1); assert.equal(ui.el("message-input").readOnly,true);
  const reopened = await launch(api,ui.saved); await settle();
  assert.equal(reopened.el("send").getAttribute("aria-label"),"重试发送");
  await reopened.el("compose").emit("submit");
  assert.deepEqual(api.calls.filter(call=>call.path==="/v1/send").at(-1).body,pending);
  await reopened.el("discard-send").emit("click");
  assert.match(reopened.el("send-error").textContent,/可能已提交/);
  await reopened.el("compose").emit("submit");
  assert.notEqual(api.calls.filter(call=>call.path==="/v1/send").at(-1).body.client_message_id,pending.client_message_id);
});

test("failed app uploads block text too, removal allows sending and never submits a failed image", async () => {
  const {api,ui} = await imageApp({failUpload:true});
  await ui.draft("text"); assert.equal(ui.el("send").disabled,true);
  await ui.el("compose").emit("submit"); assert.equal(api.calls.some(call=>call.path==="/v1/send"),false);
  const tile = ui.el("attachment-drafts").children[0]; await tile.children.at(-1).emit("click");
  assert.equal(ui.el("send").disabled,false); await ui.el("compose").emit("submit");
  assert.equal(api.calls.find(call=>call.path==="/v1/send").body.attachments,undefined);
});

test("app paste inserts accompanying text at cursor and leaves ordinary text to the browser", async () => {
  const {ui,uploads} = await imageApp();
  await ui.draft("before-after"); const input=ui.el("message-input");input.selectionStart=7;input.selectionEnd=7;
  let prevented = false;
  await input.emit("paste",{preventDefault:()=>prevented=true,clipboardData:{files:[],getData:()=>"plain"}});
  assert.equal(prevented,false); assert.equal(input.value,"before-after");
  await input.emit("paste",{preventDefault:()=>prevented=true,clipboardData:{files:[file("pasted.png")],getData:()=>"pasted"}});
  await settle();assert.equal(prevented,true);assert.equal(input.value,"before-pastedafter"); assert.equal(uploads.length,2);
});

test("epoch changes recheck ordinary drafts and discard a late state result from the previous service", async () => {
  const id="img_"+"c".repeat(32), calls=[];let release;
  const c=controller({draft:{attachments:[{...descriptor({id}),uploaded:true}]},states:identity=>{
    calls.push(identity);
    if(calls.length===1)return new Promise(resolve=>release=()=>resolve({attachments:[{id,status:"expired"}]}));
    return Promise.resolve({attachments:[{id,status:"available",attachment:descriptor({id})}]});
  }});
  const first=c.model.restore();c.model.activate({...info,service_epoch:"new"});release();await first;await settle();
  assert.deepEqual(calls.map(identity=>identity.service_epoch),["runtime","new"]);
  assert.equal(c.model.items()[0].status,"ready");c.model.dispose();
});

test("expiry refresh runs once even when a published reference retains an old upload timestamp", async () => {
  let now=100, next=0, queries=0;const timers=new Map();
  const c=controller({now:()=>now,timer:(fn,delay)=>{const id=++next;timers.set(id,{fn,delay});return id;},clearTimer:id=>timers.delete(id),
    upload:async(_,__,item,image)=>({attachment:descriptor(item,image),expires_at:new Date(200).toISOString()}),
    states:async(_,__,ids)=>{queries++;return {attachments:ids.map(id=>({id,status:"available",attachment:descriptor({id}),expires_at:new Date(200).toISOString()}))};}
  });
  c.model.addFiles([file()],limits);await settle();
  assert.equal([...timers.values()][0].delay,100);
  now=201;const timer=[...timers.values()][0];timers.clear();timer.fn();await settle();
  assert.equal(queries,1);assert.equal(c.model.items()[0].status,"ready");assert.equal(timers.size,0);c.model.dispose();
});

test("OS picker return retains selected files even before foreground network reconciliation", async () => {
  const {ui,uploads}=await imageApp();
  await ui.el("add-images").emit("click");
  ui.window.navigator={onLine:false};await ui.window.emit("offline");
  ui.el("attachment-input").files=[file("returned.png")];await ui.el("attachment-input").emit("change");await settle();
  assert.equal(ui.el("attachment-drafts").children.length,2);assert.equal(uploads.length,2);
  assert.equal(ui.el("send").disabled,true);
});

for (const mode of ["single", "default-direct"]) test(`${mode} composer uploads and sends an image through the shared contract`, async () => {
  const {ui,api,uploads}=await imageApp({mode});
  assert.equal(ui.el("compose-more").disabled,false);assert.equal(ui.el("send").disabled,false);
  await ui.el("compose").emit("submit");
  const sent=api.calls.find(call=>call.path==="/v1/send").body;
  assert.equal(sent.attachments.length,1);assert.equal(uploads.length,1);
  assert.equal(sent.room_instance,info.room_instance);assert.equal(ui.el("attachment-drafts").hidden,true);
});
