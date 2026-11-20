// Production TeamX APIs and persistence. The only fault is a dropped response
// after the production send handler has committed. No API response is mocked.
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const {createRequire} = require('node:module');
const {chromium} = createRequire(path.resolve('../../packaging/verifier/package.json'))('playwright');

(async () => {
  const endpoint = process.argv[2], cfg = JSON.parse(process.argv[3]);
  const browser = await chromium.launch({headless:true});
  const stateFile = path.join(cfg.out,'browser-storage.json');
  const evidenceFile = path.join(cfg.out,'browser.json');
  const evidence = cfg.phase === 'send' ? {backend:'real TeamX',checks:[]} : JSON.parse(await fs.readFile(evidenceFile,'utf8'));
  let page;
  try {
    const context = await browser.newContext({viewport:{width:1440,height:1000},permissions:['clipboard-read','clipboard-write'],...(cfg.phase==='restored'?{storageState:stateFile}:{})});
    // TeamX intentionally scopes drafts to the tab's sessionStorage. Recreate
    // that same tab session when the Go driver restarts the backend between
    // browser processes; Playwright storageState does not include it.
    if(cfg.phase==='restored') {
      const saved=JSON.parse(await fs.readFile(path.join(cfg.out,'tab-session.json'),'utf8'));
      await context.addInitScript(({origin,saved})=>{
        if(location.origin===origin)for(const [key,value] of Object.entries(saved))sessionStorage.setItem(key,value);
      },{origin:endpoint,saved});
    }
    page = await context.newPage(); page.setDefaultTimeout(20000);
    const errors=[], sends=[], uploads=[];
    page.on('pageerror',e=>errors.push(e.message));
    page.on('request',r=>{
      if(new URL(r.url()).pathname==='/v1/send')sends.push(r.postDataJSON());
      if(new URL(r.url()).pathname==='/v1/attachments/upload')uploads.push(r.url());
    });
    async function enter() {
      await page.goto(endpoint);
      if(cfg.mode!=='single') await page.locator(`#room-list > [data-room-key=${JSON.stringify(JSON.stringify([cfg.room.room_id,cfg.room.room_instance]))}] > button:first-child`).click();
      await page.locator('#message-input:enabled').waitFor();
      if(await page.locator('#activity-dialog').evaluate(el=>el.open))await page.locator('#close-activity').click();
    }
    const ready = n => page.waitForFunction(n=>document.querySelectorAll('.attachment-draft[data-status="ready"]').length===n,n);
    async function add() {
      await page.locator('#compose-more').click();
      const chooser=page.waitForEvent('filechooser');await page.locator('#add-images').click();
      await (await chooser).setFiles(cfg.picture);
    }
    async function preview(seq,n) {
      await page.locator(`#message-${seq} .room-message-attachments img`).first().waitFor();
      await page.waitForFunction(({seq,n})=>{const images=[...document.querySelectorAll(`#message-${seq} .room-message-attachments img`)];return images.length===n&&images.every(i=>i.complete&&i.naturalWidth===960);},{seq,n});
    }
    await enter();
    if(cfg.phase==='send') {
      await add();await ready(1);
      assert(await page.locator('.attachment-draft img').evaluate(i=>i.complete&&i.naturalWidth===960&&i.src.startsWith('blob:')));
      await page.locator('#message-input').fill('browser image acceptance');
      // Actual browser clipboard API, followed by an actual keyboard paste.
      const png=(await fs.readFile(cfg.picture)).toString('base64');
      await page.evaluate(async base64=>{
        const bytes=Uint8Array.from(atob(base64),c=>c.charCodeAt(0));
        await navigator.clipboard.write([new ClipboardItem({'image/png':new Blob([bytes],{type:'image/png'})})]);
      },png);
      await page.locator('#message-input').focus();
      await page.keyboard.press(process.platform==='darwin'?'Meta+V':'Control+V');
      await ready(2);
      assert(await page.locator('#compose-more').isEnabled());
      await page.screenshot({path:path.join(cfg.out,'draft-desktop.png')});
      await page.locator('#send').click();await preview(1,2);
      await page.locator('#message-1 .attachment-preview').first().click();
      await page.locator('.message-image-dialog').waitFor();
      assert.equal(await page.locator('.message-image-download').count(),1);
      await page.keyboard.press('Escape');
      evidence.checks.push('system clipboard paste + file chooser -> production uploads -> text and two images -> preview');

      await add();await ready(1);
      let drop=true;
      await page.route('**/v1/send',async route=>{
        if(!drop)return route.continue();
        drop=false;
        const response=await route.fetch();assert.equal(response.status(),200);
        await route.abort('failed');
      });
      await page.locator('#send').click();await page.locator('#discard-send').waitFor();
      const original=structuredClone(sends.at(-1)),uploaded=uploads.length;
      assert.equal(original.text,'');
      await page.reload();await page.locator('#send[aria-label="重试发送"]').waitFor();
      await page.locator('#send').click();await page.locator('#discard-send').waitFor({state:'hidden'});await preview(2,1);
      assert.deepEqual(sends.at(-1),original);assert.equal(uploads.length,uploaded);
      assert.equal(sends.length,3);
      await page.unroute('**/v1/send');
      evidence.checks.push('lost committed pure-image receipt -> reload -> identical request retry with no upload');
      await page.locator('#message-input').fill('ordinary text regression');await page.locator('#send').click();
      await page.locator('#message-3').waitFor();
      await add();await ready(1);await page.reload();await ready(1);
      assert(await page.locator('.attachment-draft img').evaluate(i=>i.complete&&i.naturalWidth===960&&!i.src.startsWith('blob:')));
      await context.storageState({path:stateFile});
      await fs.writeFile(path.join(cfg.out,'tab-session.json'),JSON.stringify(await page.evaluate(()=>Object.fromEntries(Object.entries(sessionStorage)))),{mode:0o600});
      evidence.checks.push('ordinary text + uploaded draft survives refresh');
      evidence.pending_service_epoch=original.service_epoch;
    } else {
      await preview(1,2);await preview(2,1);await ready(1);
      const epoch=await page.locator('#message-1 img').first().evaluate(i=>new URL(i.src).searchParams.get('service_epoch'));
      assert.notEqual(epoch,evidence.pending_service_epoch);
      await page.locator('#message-1 .attachment-preview').first().click();await page.locator('.message-image-dialog').waitFor();await page.keyboard.press('Escape');
      assert.equal(uploads.length,0);
      evidence.checks.push('service reopened from same data -> new epoch -> saved messages and draft preview without upload');
      await page.locator('.attachment-remove').click();
      assert.equal(sends.length,0);
    }
    for(const width of [1440,390,320]) {
      await page.setViewportSize({width,height:width===320?640:900});
      if(cfg.mode!=='single'&&width<600&&await page.locator('#conversation').evaluate(e=>e.inert))await page.locator('#room-list .room-choice').first().click();
      await page.waitForFunction(()=>document.documentElement.scrollWidth<=innerWidth);
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
      await page.screenshot({path:path.join(cfg.out,`${cfg.phase}-${width}.png`)});
    }
    assert.deepEqual(errors,[]);
    evidence.browser=browser.version();evidence.mode=cfg.mode;evidence.phase=cfg.phase;evidence.page_errors=errors;evidence.phone='not exercised; widths are simulated';
    await fs.writeFile(evidenceFile,JSON.stringify(evidence,null,2)+'\n',{mode:0o600});
    console.log(JSON.stringify(evidence));
  } catch(error) {
    if(page){
      console.error('Layout',await page.evaluate(()=>({width:innerWidth,scroll:document.documentElement.scrollWidth,offenders:[...document.querySelectorAll('body *')].map(e=>({id:e.id,cls:e.className,rect:e.getBoundingClientRect().toJSON()})).filter(e=>e.rect.width>0&&(e.rect.right>innerWidth+1||e.rect.left< -1)).slice(0,20)})).catch(()=>({})));
      await page.screenshot({path:path.join(cfg.out,'failure.png')}).catch(()=>{});await fs.writeFile(path.join(cfg.out,'failure.html'),await page.content()).catch(()=>{});
    }
    throw error;
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
