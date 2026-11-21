const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const {createHash, randomBytes} = require('node:crypto');

// No mocked fetch, fabricated messages or model tools. The Go caller owns the
// isolated real process, its normal restart, and independent file assertions.
(async () => {
  const [endpoint, raw] = process.argv.slice(2), f = JSON.parse(raw);
  const browser = await chromium.launch({headless:true});
  const page = await browser.newPage({viewport:{width:1600,height:1000}});
  const errors = [], reads = [];
  page.setDefaultTimeout(20000);
  page.on('pageerror', e => errors.push(e.message));
  page.on('request', r => {if (r.url().includes('/v1/workspace/files')) reads.push(r.url());});
  const evidence = {phase:f.phase, browser:browser.version(), node:process.version, real_model:true, real_serve_process:true};
  const post = async (path, data) => {
    const response = await page.request.post(endpoint+path, {headers:{Origin:endpoint},data});
    assert.equal(response.status(),200,await response.text());
    return response.json();
  };
  const catalog = () => page.request.get(endpoint+'/v1/rooms').then(r=>r.json());
  const query = async () => {const c=await catalog();return new URLSearchParams({...f.room,service_epoch:c.service_epoch,caller_id:c.operator,message_seq:String(f.seq)});};
  const open = async () => {
    await page.locator(`#message-${f.seq} button.workspace-file-link`).filter({hasText:'预览示例'}).click();
    await page.locator('#files-body h1').filter({hasText:'真实文件预览'}).waitFor();
  };
  const image = () => page.waitForFunction(()=>[...document.querySelectorAll('#files-body img')].some(i=>i.complete&&i.naturalWidth===32&&i.naturalHeight===32));
  const directOpen = async () => {
    await page.locator('#room-menu-label').click();await page.locator('#conversation-open-files').click();
    await page.locator('#files-body .file-entry').first().waitFor();
    await page.locator('#files-path-edit').click();await page.locator('#files-locate-path').fill('docs/preview.md');await page.locator('#files-locate button[type=submit]').click();
    await page.locator('#files-body h1').filter({hasText:'真实文件预览'}).waitFor();
  };
  try {
    await page.goto(endpoint);
    await page.locator(`[data-room-key*='${f.room.room_instance}'] .room-choice`).click();
    await page.waitForFunction(()=>!document.querySelector('#message-input').disabled);
    await page.locator(`#message-${f.seq} button.workspace-file-link`).first().waitFor();
    if (f.phase === 'modify') {
      const before=reads.length;await open();await image();
      const preview=reads.slice(before).filter(u=>u.includes('/preview?'));
      assert.equal(preview.length,1);assert.equal(new URL(preview[0]).searchParams.get('message_seq'),String(f.seq));
      assert.match(await page.locator('#files-body').textContent(),/WORKSPACE_PREVIEW_V1/);
      await page.locator('#files-source-toggle').click();await page.locator('#files-body pre').waitFor();
      assert.match(await page.locator('#files-body pre').textContent(),/!\[示例图片\]\(\.\.\/images\/preview.png\)/);
      await page.locator('#files-preview-mode').click();await image();
      await page.locator('#files-body button.workspace-file-link').filter({hasText:'使用指南'}).click();
      await page.locator('#files-body h2').filter({hasText:'使用'}).waitFor();
      await page.locator('#files-back').click();await image();
      await page.locator('#files-directory').click();await page.locator('#files-body .file-entry').getByText('preview.md',{exact:true}).waitFor();
      await page.locator('#files-body .file-entry').getByText('preview.md',{exact:true}).click();await image();
      await page.locator('#files-expand').click();await page.screenshot({path:`${f.output}/generated-expanded.png`,animations:'disabled'});
      await page.keyboard.press('Escape');await page.locator('#files-close').click();
      const task='请修改刚才的 docs/preview.md：保留标题、表格、代码、图片和使用指南链接，把 WORKSPACE_PREVIEW_V1 替换为 WORKSPACE_PREVIEW_V2，并增加一句“这次修改通过真实模型完成”。不要修改其他文件。验证后仅发一条 Room 消息，包含 [预览修改](docs/preview.md)；不要发送其他 Room 消息。';
      await page.locator('#message-input').fill(task);await page.locator('#send').click();
      // Keep the old document mounted while the real model edits it. Only an
      // explicit refresh reads the changed bytes; no polling reads its body.
      await open();await image();await page.locator('#files-source-toggle').click();
      const readsBefore=reads.filter(u=>u.includes('/preview?')).length;
      await page.locator('#messages button.workspace-file-link').filter({hasText:'预览修改'}).waitFor({state:'attached',timeout:240000});
      assert.equal(reads.filter(u=>u.includes('/preview?')).length,readsBefore);
      assert.match(await page.locator('#files-body pre').textContent(),/WORKSPACE_PREVIEW_V1/);
      await page.locator('#files-refresh').click();
      await page.waitForFunction(()=>document.querySelector('#files-body pre')?.textContent.includes('WORKSPACE_PREVIEW_V2'));
      assert.equal(await page.locator('#files-source-toggle').getAttribute('aria-pressed'),'true');
      await page.locator('#files-preview-mode').click();await image();
      assert.match(await page.locator('#files-body').textContent(),/这次修改通过真实模型完成/);
      await page.screenshot({path:`${f.output}/modified-desktop.png`,animations:'disabled'});
      const downloadEvent=page.waitForEvent('download');await page.locator('#files-download').click();
      const download=await downloadEvent;assert.equal(download.suggestedFilename(),'preview.md');assert.equal(await download.failure(),null);
      await page.locator('#files-close').click();
      Object.assign(evidence,{single_preview_request:true,source_toggle:true,relative_image:true,relative_document:true,directory:true,expanded:true,explicit_refresh:true,no_automatic_body_reads:true,markdown_download:true});
    } else if (f.phase === 'restart') {
      for (const width of [1600,1000,390]) {
        await page.setViewportSize({width,height:850});await open();await image();
        assert.match(await page.locator('#files-body').textContent(),/WORKSPACE_PREVIEW_V2/);
        const box=await page.locator('#files-page').boundingBox();assert.ok(box.x>=0&&box.x+box.width<=width+1);
        await page.screenshot({path:`${f.output}/restart-${width}.png`,animations:'disabled'});
        if(width===390)await page.goBack();else await page.keyboard.press('Escape');
        assert.equal(await page.locator('#files-page').isVisible(),false);
      }
      Object.assign(evidence,{old_link_after_restart:true,sizes:[1600,1000,390],mobile_browser_back:true});
    } else if (f.phase === 'invalidate') {
      await open();await image();await page.locator('#files-close').click();
      const switchDirectory=async external=>{
        const c=await catalog();const q=new URLSearchParams({...f.room,service_epoch:c.service_epoch,caller_id:c.operator});
        const workspace=await page.request.get(endpoint+'/v1/workspace?'+q).then(r=>r.json());
        const target={...workspace.workspace};if(external)target.member_directories={[f.member]:f.other};else delete target.member_directories;
        await post('/v1/workspace/apply',{...f.room,service_epoch:c.service_epoch,caller:{kind:'human',id:c.operator},expected_version:workspace.workspace_version,operation_id:randomBytes(16).toString('hex'),target});
        await page.waitForFunction(async query=>{
          const r=await fetch('/v1/workspace?'+query);if(!r.ok)return false;const v=await r.json();
          if(v.operation?.status==='failed')throw new Error(v.operation.error_code);
          if(v.operation?.status!=='completed'||!v.active)return false;
          const c=await fetch('/v1/rooms').then(r=>r.json()),room=c.rooms.find(r=>r.room_instance===v.room_instance);
          return room?.runtime_status==='ready'&&!room.busy&&room.service?.service_epoch===v.active.service_epoch;
        },q.toString());
        await page.reload();await page.locator(`[data-room-key*='${f.room.room_instance}'] .room-choice`).click();
        await page.waitForFunction(()=>!document.querySelector('#message-input').disabled&&document.querySelector('#connection').textContent==='服务已连接');
      };
      await switchDirectory(true);
      await page.locator(`#message-${f.seq} button.workspace-file-link`).first().click();
      await page.locator('#files-error').filter({hasText:'来源已变化'}).waitFor();
      await page.screenshot({path:`${f.output}/source-changed.png`,animations:'disabled'});
      const changed=await query();changed.set('path','images/preview.png');
      const denied=await page.request.get(endpoint+'/v1/workspace/files/content?'+changed);
      assert.equal(denied.status(),409);assert.equal((await denied.json()).code,'workspace_source_changed');
      await page.locator('#files-close').click();await switchDirectory(false);
      await open();await image();await page.locator('#message-input').fill('清空后仍保留的草稿');
      // Start a real 32 MiB response and pause consumption before clearing.
      // The admitted stream remains bound to its original file handle.
      const q=await query();q.set('path','downloads/preview.bin');q.set('download','true');
      const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),35000);
      try {
        const response=await fetch(endpoint+'/v1/workspace/files/content?'+q,{signal:controller.signal});
        assert.equal(response.status,200);assert.equal(response.headers.get('content-length'),String(32*1024*1024));
        assert.equal(response.headers.get('cache-control'),'private, no-store');assert.equal(response.headers.get('x-content-type-options'),'nosniff');
        const reader=response.body.getReader(),hash=createHash('sha256');
        const first=await reader.read();assert.ok(!first.done&&first.value.length<32*1024*1024);hash.update(first.value);
        const c=await catalog(),room=c.rooms.find(r=>r.room_instance===f.room.room_instance);
        const latest=Math.max(...await page.locator('#messages .message[id^="message-"]').evaluateAll(elements=>elements.map(e=>Number(e.id.slice(8)))));
        assert.ok(latest>=f.seq);
        await post('/v1/messages/clear',{...room.service,caller:{kind:'human',id:c.operator},snapshot_seq:latest});
        await page.locator('#files-error').filter({hasText:'原消息已清空'}).waitFor();
        let bytes=first.value.length;
        while(true){const chunk=await reader.read();if(chunk.done)break;bytes+=chunk.value.length;hash.update(chunk.value);}
        assert.equal(bytes,32*1024*1024);assert.equal(hash.digest('hex'),f.download_sha256);
        evidence.admitted_download={bytes,received_before_clear:first.value.length,completed_after_clear:true,original_file_sha256:true};
      } finally {clearTimeout(timer);controller.abort();}
      for(const [route,path,download] of [['preview','docs/preview.md',false],['content','images/preview.png',false],['content','downloads/preview.bin',true]]){
        const q=await query();q.set('path',path);if(download)q.set('download','true');
        const r=await page.request.get(`${endpoint}/v1/workspace/files/${route}?${q}`);
        assert.equal(r.status(),404);assert.equal((await r.json()).code,'reference_unavailable');
      }
      assert.equal(await page.locator('#files-body img').count(),0);
      await page.locator('#files-close').click();await directOpen();await image();
      assert.match(await page.locator('#files-body').textContent(),/WORKSPACE_PREVIEW_V2/);
      assert.equal(await page.locator('#message-input').inputValue(),'清空后仍保留的草稿');
      await page.screenshot({path:`${f.output}/direct-after-clear.png`,animations:'disabled'});
      Object.assign(evidence,{source_change:true,restored_same_source:true,clear_denies_new_preview_image_download:true,direct_browse_after_clear:true,files_preserved:true,draft_preserved:true});
    } else {throw new Error('unknown phase');}
    assert.deepEqual(errors,[]);console.log(JSON.stringify({...evidence,status:'passed',page_errors:errors}));
  } catch(error) {
    await page.screenshot({path:`${f.output}/failure-${f.phase}.png`,animations:'disabled'}).catch(()=>{});
    console.error(JSON.stringify({phase:f.phase,body:await page.locator('body').innerText(),reads,errors}));throw error;
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
