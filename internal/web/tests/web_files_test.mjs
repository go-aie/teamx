import {test} from "node:test";
import assert from "node:assert/strict";
import {launch,settle} from "./web_app_harness.mjs";
import {fixture} from "./web_member_platform_fixture.mjs";

const roots = () => ["a","b"].map(id=>({membership_id:`member-${id}`,root_id:`wr1_${id.repeat(64)}`,shared_key:id,label:`助理 ${id}`,available:true,directory_source:"managed"}));
function fileFixture() {
 const api=fixture();api.sources=roots();let delayed=null;
 api.delay=fn=>delayed=fn;
 api.hook=(path)=>{
  if(!path.startsWith('/v1/workspace/files'))return;
  const url=new URL(path,"http://fixture"),q=url.searchParams;
  if(url.pathname.endsWith('/sources'))return {sources:structuredClone(api.sources),limits:{text_bytes:1048576}};
  const override=delayed?.(url);if(override!==undefined)return override;
  const source=q.has('message_seq') ? api.sources[0] : api.sources.find(source=>source.membership_id===q.get('membership_id')&&source.root_id===q.get('root_id'));
  if(!source)throw Object.assign(Error('changed'),{code:'workspace_source_changed'});
  if(url.pathname.endsWith('/preview'))return {source,path:q.get('path'),name:'note.md',preview_kind:'markdown',text:'# 当前文件',size:16,mtime:'2026-11-21T00:00:00Z'};
  return {source,path:q.get('path'),entries:[{name:'note.md',path:'note.md',kind:'file',size:16}],truncated:false};
 };
 return api;
}
async function open(ui){await ui.el('room-menu-label').emit('click');await ui.el('conversation-open-files').emit('click');await settle();}
async function preview(ui){await ui.el('files-body').children[0].emit('click');await settle();}

test('workspace files own the rail, preserve drafts and settings, and keep text out of storage',async()=>{
 const api=fileFixture(),ui=await launch(api);await ui.draft('未发送草稿');ui.el('timeline').scrollTop=37;
 await ui.el('members').children[0].querySelector('.member-summary').emit('click');await ui.el('agent-tab-activity').emit('click');await settle();
 const before=api.calls.filter(call=>call.path==='/v1/member/activity').length;
 await open(ui);assert.equal(ui.el('files-page').hidden,false);assert.equal(ui.el('activity-dialog').hidden,true);assert.equal(ui.el('workspace').classList.contains('files-open'),true);
 await ui.refresh();assert.equal(api.calls.filter(call=>call.path==='/v1/member/activity').length,before);
 ui.el('files-filter').value='note';await ui.el('files-filter').emit('input');ui.el('files-body').scrollTop=78;
 await preview(ui);assert.equal(ui.el('files-list-tools').hidden,true);
 await ui.el('files-source-toggle').emit('click');assert.equal(ui.el('files-body').children[0].textContent,'# 当前文件');
 await ui.el('files-refresh').emit('click');assert.equal(ui.el('files-source-toggle').getAttribute('aria-pressed'),'true');
 await ui.el('files-back').emit('click');assert.equal(ui.el('files-filter').value,'note');assert.equal(ui.el('files-body').scrollTop,78);
 await ui.el('files-expand').emit('click');assert.equal(ui.el('conversation').inert,true);
 await ui.el('files-expand').emit('click');assert.equal(ui.el('files-expand').getAttribute('aria-expanded'),'false');
 await ui.el('files-close').emit('click');assert.equal(ui.el('activity-dialog').hidden,false);assert.equal(ui.el('activity-dialog').open,true);
 assert.equal(ui.el('timeline').scrollTop,37);assert.equal(ui.el('message-input').value,'未发送草稿');
 assert.ok([...ui.saved.values()].every(value=>!value.includes('当前文件')));
});

function messageFixture({legacy=false}={}) {
 const api=fileFixture(),hook=api.hook;
 api.message={seq:1,client_message_id:'delivery',author:{kind:'agent',id:'a'},author_membership_id:'member-a',
  ...(legacy ? {} : {workspace_root_id:roots()[0].root_id}),text:'[交付](note.md)',created_at:'2026-11-21T00:00:00Z'};
 api.cleared=0;
 api.hook=(path,...args)=>path==='/v1/read' ? {messages:api.cleared ? [] : [api.message],snapshot_seq:1,cleared_through:api.cleared,has_more:false} : hook(path,...args);
 return api;
}
async function openMessage(ui) {
 const body=ui.document.getElementById('message-1').querySelector('.message-text');
 await body.markdownOptions.openWorkspaceFile('note.md',body);await settle();
}

test('message entry uses one preview read, returns to chat and explicitly switches to current directory browsing',async()=>{
 const api=messageFixture(),ui=await launch(api);await ui.draft('保留草稿');
 const before=api.calls.length;await openMessage(ui);
 const reads=api.calls.slice(before).filter(call=>call.path.startsWith('/v1/workspace/files'));
 assert.equal(reads.length,1);const q=new URL(reads[0].path,'http://fixture').searchParams;
 assert.equal(q.get('message_seq'),'1');assert.equal(q.has('membership_id'),false);
 assert.equal(ui.el('files-directory').hidden,false);
 await ui.el('files-back').emit('click');assert.equal(ui.el('files-page').hidden,true);assert.equal(ui.el('message-input').value,'保留草稿');
 await openMessage(ui);await ui.el('files-directory').emit('click');await settle();
 assert.equal(ui.el('files-list-tools').hidden,false);
 const last=api.calls.filter(call=>call.path.startsWith('/v1/workspace/files?')).at(-1);
 assert.equal(new URL(last.path,'http://fixture').searchParams.get('membership_id'),'member-a');
 assert.equal(new URL(last.path,'http://fixture').searchParams.has('message_seq'),false);
});

test('legacy message links explain the missing source without guessing or reading a current directory',async()=>{
 const api=messageFixture({legacy:true}),ui=await launch(api),before=api.calls.length;
 await openMessage(ui);assert.match(ui.el('files-error').textContent,/旧消息未记录文件来源/);
 assert.equal(api.calls.slice(before).filter(call=>call.path.startsWith('/v1/workspace/files')).length,0);
 await ui.el('files-refresh').emit('click');assert.match(ui.el('files-error').textContent,/旧消息未记录/);
 await ui.el('files-directory').emit('click');await settle();assert.equal(ui.el('files-list-tools').hidden,false);
});

test('clearing the original message discards its pending preview and keeps its source selector on refresh',async()=>{
 const api=messageFixture(),ui=await launch(api);let release;
 api.delay(url=>url.pathname.endsWith('/preview') ? new Promise(resolve=>release=resolve) : undefined);
 const pending=openMessage(ui);await settle();api.cleared=1;await ui.refresh();
 assert.match(ui.el('files-error').textContent,/原消息已清空/);
 release({source:roots()[0],path:'note.md',name:'note.md',preview_kind:'text',text:'OLD',size:3,mtime:'2026-11-21T00:00:00Z'});
 await pending;assert.equal(ui.el('files-body').children.length,0);
 api.delay(url=>{assert.equal(url.searchParams.get('message_seq'),'1');throw Object.assign(Error('gone'),{code:'reference_unavailable'});});
 await ui.el('files-refresh').emit('click');assert.match(ui.el('files-error').textContent,/原消息已清空/);
});

test('closing and changing service contexts discard delayed file responses even when cancellation is ignored',async()=>{
 const api=fileFixture(),ui=await launch(api);await open(ui);
 let release;api.delay(url=>url.pathname.endsWith('/preview')?new Promise(resolve=>release=resolve):undefined);
 const pending=ui.el('files-body').children[0].emit('click');await settle();await ui.el('files-close').emit('click');
 release({path:'note.md',name:'OLD',preview_kind:'text',text:'OLD',size:3,mtime:'2026-11-21T00:00:00Z'});await pending;await settle();
 assert.equal(ui.el('files-page').hidden,true);assert.equal(ui.el('files-body').children.length,0);
 api.delay(null);await open(ui);
 api.delay(url=>url.pathname.endsWith('/preview')?new Promise(resolve=>release=resolve):undefined);
 const second=ui.el('files-body').children[0].emit('click');await settle();api.epoch='new-manager';await ui.refresh();
 release({path:'note.md',name:'OLD',preview_kind:'text',text:'OLD',size:3,mtime:'2026-11-21T00:00:00Z'});await second;await settle();
 assert.equal(ui.el('files-page').hidden,true);assert.equal(ui.el('files-body').children.length,0);
});

test('source changes require explicit reselection and never switch to another member silently',async()=>{
 const api=fileFixture(),ui=await launch(api);await open(ui);await preview(ui);
 api.sources[0].root_id='wr1_'+'c'.repeat(64);
 await ui.el('files-refresh').emit('click');await settle();
 assert.match(ui.el('files-error').textContent,/来源已变化/);assert.equal(ui.el('files-source').value,'');assert.equal(ui.el('files-body').children.length,0);
 ui.el('files-source').value='member-a';await ui.el('files-source').emit('change');await settle();
 const last=api.calls.filter(call=>call.path.startsWith('/v1/workspace/files?')).at(-1);
 assert.ok(last.path.includes('root_id=wr1_'+ 'c'.repeat(64)));
});

test('a late preview cannot replace the directory selected from another source',async()=>{
 const api=fileFixture(),ui=await launch(api);await open(ui);
 let release;api.delay(url=>url.pathname.endsWith('/preview')?new Promise(resolve=>release=resolve):undefined);
 const pending=ui.el('files-body').children[0].emit('click');await settle();
 // Sources are changed from the list, after returning out of the pending file.
 await ui.el('files-back').emit('click');ui.el('files-source').value='member-b';await ui.el('files-source').emit('change');await settle();
 release({name:'OLD',preview_kind:'text',text:'OLD'});await pending;await settle();
 assert.equal(ui.el('files-source').value,'member-b');assert.equal(ui.el('files-list-tools').hidden,false);assert.equal(ui.el('files-body').children[0].children[1].textContent,'note.md');
});

test('expanding a file preserves its pending read and the current reading position',async()=>{
 const api=fileFixture(),ui=await launch(api);await open(ui);
 let release;api.delay(url=>url.pathname.endsWith('/preview')?new Promise(resolve=>release=resolve):undefined);
 const pending=ui.el('files-body').children[0].emit('click');await settle();
 await ui.el('files-expand').emit('click');await ui.el('files-expand').emit('click');
 release({path:'note.md',name:'note.md',preview_kind:'text',text:'完整内容',size:12,mtime:'2026-11-21T00:00:00Z'});await pending;await settle();
 assert.equal(ui.el('files-body').children[0].textContent,'完整内容');
 ui.el('files-body').scrollTop=120;await ui.el('files-expand').emit('click');assert.equal(ui.el('files-body').scrollTop,120);
 await ui.el('files-expand').emit('click');assert.equal(ui.el('files-body').scrollTop,120);
});

function directoryFixture() {
 const api=fileFixture(),hook=api.hook;
 const entry=(name,kind='file',mtime='2026-11-20T00:00:00Z')=>({name,path:name,kind,size:2048,mtime});
 api.directories={
  '':[entry('file10.md'),entry('docs10','directory'),entry('file2.md','file','2026-11-21T00:00:00Z'),entry('docs2','directory')],
  docs2:[entry('note.md'),entry('nested','directory')],
  'docs2/nested':[entry('note.md')],
  'one/two/three/four':[entry('note.md')],
  one:[entry('two','directory')],
 };
 api.hook=(path,...args)=>{
  const url=new URL(path,'http://fixture');
  if(url.pathname==='/v1/workspace/files') {
   const directory=url.searchParams.get('path');
   if(!(directory in api.directories))throw Object.assign(Error('missing'),{code:'file_not_found'});
   return {path:directory,entries:api.directories[directory].map(item=>({...item,path:[directory,item.name].filter(Boolean).join('/')})),truncated:false};
  }
  return hook(path,...args);
 };
 return api;
}
const entries=ui=>ui.el('files-body').children.filter(item=>item.dataset.path);
const paths=ui=>entries(ui).map(item=>item.dataset.path);
const clickEntry=async(ui,path)=>{await entries(ui).find(item=>item.dataset.path===path).emit('click');await settle();};

test('directory sorting keeps folders first, uses natural names and never rereads for sorting or filtering',async()=>{
 const api=directoryFixture(),ui=await launch(api);await open(ui);
 assert.deepEqual(paths(ui),['docs2','docs10','file2.md','file10.md']);
 assert.equal(ui.el('files-up').disabled,true);assert.equal(ui.el('files-back').hidden,true);
 const before=api.calls.length;
 ui.window.innerWidth=390;await ui.el('files-options-toggle').emit('click');
 assert.equal(ui.document.activeElement,ui.el('files-sort-mobile'));
 ui.el('files-sort-mobile').value='modified';await ui.el('files-sort-mobile').emit('change');
 assert.equal(ui.el('files-sort').value,'modified');assert.equal(ui.el('files-options').hidden,true);
 assert.equal(api.calls.length,before);
 assert.deepEqual(paths(ui),['docs2','docs10','file2.md','file10.md']);
 // Make the older natural name the newest to exercise the alternate order.
 api.directories[''][0].mtime='2026-11-22T00:00:00Z';await ui.el('files-refresh').emit('click');await settle();
 assert.deepEqual(paths(ui),['docs2','docs10','file10.md','file2.md']);
 const refreshed=api.calls.length;ui.el('files-filter').value='file';await ui.el('files-filter').emit('input');
 assert.deepEqual(paths(ui),['file10.md','file2.md']);assert.equal(ui.el('files-count').textContent,'2 / 4 项');
 assert.equal(api.calls.length,refreshed);assert.ok(refreshed>before);
});

test('up, breadcrumbs and preview return restore directory filters, scroll and focused entries independently of expansion',async()=>{
 const api=directoryFixture(),ui=await launch(api);await open(ui);
 ui.el('files-filter').value='docs';await ui.el('files-filter').emit('input');ui.el('files-body').scrollTop=47;
 await clickEntry(ui,'docs2');assert.equal(ui.el('files-filter').value,'');assert.equal(ui.el('files-up').disabled,false);
 ui.el('files-filter').value='note';await ui.el('files-filter').emit('input');ui.el('files-body').scrollTop=82;
 await clickEntry(ui,'docs2/note.md');await ui.el('files-expand').emit('click');
 await ui.el('files-back').emit('click');
 assert.equal(ui.el('files-expand').getAttribute('aria-expanded'),'true');
 assert.equal(ui.el('files-filter').value,'note');assert.equal(ui.el('files-body').scrollTop,82);
 assert.equal(ui.document.activeElement.dataset.path,'docs2/note.md');
 await ui.el('files-up').emit('click');await settle();
 assert.equal(ui.el('files-filter').value,'docs');assert.equal(ui.el('files-body').scrollTop,47);
 assert.equal(ui.document.activeElement.dataset.path,'docs2');assert.equal(ui.el('files-up').disabled,true);
 await clickEntry(ui,'docs10');
 // A missing child can still return to its parent without losing orientation.
 assert.match(ui.el('files-error').textContent,/文件已不存在/);
 await ui.el('files-path-segments').children[0].emit('click');await settle();assert.deepEqual(paths(ui),['docs2','docs10']);
});

test('path editing cancels locally, locates deep directories, exposes collapsed ancestors and keeps failed input editable',async()=>{
 const api=directoryFixture(),ui=await launch(api);await open(ui);
 const before=api.calls.length;await ui.el('files-path-edit').emit('click');
 assert.equal(ui.el('files-locate').hidden,false);ui.el('files-locate-path').value='ignored';
 await ui.document.emit('keydown',{key:'Escape'});
 assert.equal(ui.el('files-locate').hidden,true);assert.equal(ui.el('files-page').hidden,false);assert.equal(api.calls.length,before);
 await ui.el('files-path-edit').emit('click');ui.el('files-locate-path').value='one/two/three/four';
 await ui.el('files-locate').emit('submit');await settle();
 assert.equal(ui.el('files-locate').hidden,true);assert.deepEqual(paths(ui),['one/two/three/four/note.md']);
 const overflow=ui.el('files-path-segments').children.find(item=>item.tag==='details');
 assert.deepEqual(overflow.children[1].children.map(item=>item.textContent),['one','two']);
 await overflow.children[1].children[0].emit('click');await settle();assert.deepEqual(paths(ui),['one/two']);
 await ui.el('files-path-edit').emit('click');ui.el('files-locate-path').value='missing';await ui.el('files-locate').emit('submit');
 assert.equal(ui.el('files-locate').hidden,false);assert.equal(ui.el('files-locate-path').value,'missing');
 assert.match(ui.el('files-error').textContent,/文件已不存在/);
 ui.el('files-locate-path').value='missing/';await ui.el('files-locate').emit('submit');
 assert.equal(ui.el('files-locate').hidden,false);assert.equal(ui.el('files-locate-path').value,'missing/');
});

test('keyboard directory navigation follows the visible sorted list and returns focus to the child folder',async()=>{
 const ui=await launch(directoryFixture());await open(ui);ui.el('files-body').focus();
 await ui.el('files-body').emit('keydown',{key:'ArrowDown'});assert.equal(ui.document.activeElement.dataset.path,'docs2');
 await ui.el('files-body').emit('keydown',{key:'ArrowRight'});await settle();assert.deepEqual(paths(ui),['docs2/nested','docs2/note.md']);
 await ui.el('files-body').emit('keydown',{key:'End'});assert.equal(ui.document.activeElement.dataset.path,'docs2/note.md');
 await ui.el('files-body').emit('keydown',{key:'ArrowLeft'});await settle();assert.equal(ui.document.activeElement.dataset.path,'docs2');
});

test('message breadcrumbs explicitly enter the original source directory and reject a replaced source',async()=>{
 const api=messageFixture(),ui=await launch(api);await openMessage(ui);
 assert.equal(ui.el('files-back').getAttribute('aria-label'),'返回会话');assert.equal(ui.el('files-path-edit').hidden,true);
 api.sources[0].root_id='wr1_'+'c'.repeat(64);
 await ui.el('files-directory').emit('click');await settle();
 assert.equal(ui.el('files-source').value,'');assert.match(ui.el('files-error').textContent,/来源已变化/);
 assert.equal(api.calls.filter(call=>new URL(call.path,'http://fixture').pathname==='/v1/workspace/files').length,0);
});

test('preview headings show the filename once, expose its containing directory and keep view modes explicit and idempotent',async()=>{
 const api=directoryFixture(),ui=await launch(api);await open(ui);await clickEntry(ui,'docs2');await clickEntry(ui,'docs2/note.md');
 assert.equal(ui.el('files-title').textContent,'note.md');assert.equal(ui.el('files-title').title,'docs2/note.md');
 assert.equal(ui.el('files-directory').textContent,'docs2');assert.equal(ui.el('files-directory').hidden,false);
 assert.ok(ui.el('files-path-segments').children.every(item=>item.textContent!=='note.md'));
 assert.match(ui.el('files-status').textContent,/^16 B · /);assert.match(ui.el('files-status').title,/当前磁盘文件内容/);
 assert.equal(ui.el('files-view-mode').hidden,false);assert.equal(ui.el('files-preview-mode').getAttribute('aria-pressed'),'true');
 const reads=api.calls.length;
 await ui.el('files-source-toggle').emit('click');const content=ui.el('files-body').children[0];ui.el('files-body').scrollTop=64;
 assert.equal(ui.el('files-preview-mode').getAttribute('aria-pressed'),'false');assert.equal(ui.el('files-source-toggle').getAttribute('aria-pressed'),'true');
 await ui.el('files-source-toggle').emit('click');assert.equal(ui.el('files-body').children[0],content);assert.equal(ui.el('files-body').scrollTop,64);
 await ui.el('files-preview-mode').emit('click');assert.equal(ui.el('files-source-toggle').getAttribute('aria-pressed'),'false');
 assert.equal(api.calls.length,reads);
 await ui.el('files-directory').emit('click');await settle();assert.deepEqual(paths(ui),['docs2/nested','docs2/note.md']);
 assert.equal(ui.el('files-view-mode').hidden,true);
});
