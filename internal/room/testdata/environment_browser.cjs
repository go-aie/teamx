const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
(async () => {
  const endpoint = process.argv[2], fixture = JSON.parse(process.argv[3]);
  const browser = await chromium.launch({headless:true});
  try {
    const page = await browser.newPage({viewport:{width:1600,height:1000}}), errors = [];
    page.on('pageerror', error => errors.push(error.message));
    page.setDefaultTimeout(15000);
    await page.goto(endpoint);
    await page.locator(`[data-room-key*='${fixture.room.room_instance}'] .room-choice`).click();
    await page.waitForFunction(() => !document.querySelector('#message-input').disabled);
    const waitForPixels = async () => {
      const viewer = page.frames().find(frame => frame.url().endsWith('/viewer'));
      await viewer.waitForFunction(() => {
        const canvas=document.querySelector('canvas');
        if(!canvas || canvas.width<100 || canvas.height<100) return false;
        const data=canvas.getContext('2d').getImageData(0,0,canvas.width,canvas.height).data, colors=new Set();
        for(let i=0;i<data.length;i+=400) colors.add(`${data[i]},${data[i+1]},${data[i+2]}`);
        return colors.size>5;
      });
    };
    const open = async () => {
      await page.locator('#room-menu-label').click();
      await page.locator('#conversation-open-environment').click();
      await page.locator('#environment-status').filter({hasText:'已连接'}).waitFor();
      const frame = page.frameLocator('#environment-screen iframe');
      await frame.locator('canvas').waitFor();
      await waitForPixels();
      return frame;
    };
    await page.locator('#message-input').fill('观看环境时保留的草稿');
    const frame = await open();
    const pixels = await frame.locator('canvas').evaluate(canvas => {
      const {width,height} = canvas;
      const data = canvas.getContext('2d').getImageData(0,0,width,height).data;
      const colors = new Set();
      for(let i=0;i<data.length;i+=400) colors.add(`${data[i]},${data[i+1]},${data[i+2]}`);
      return {width,height,colors:colors.size};
    });
    assert.ok(pixels.width>100 && pixels.height>100 && pixels.colors>5, JSON.stringify(pixels));
    assert.equal(await page.locator('#conversation').evaluate(el=>el.inert),false);
    assert.equal(await page.locator('#environment-screen iframe').evaluate(el=>el.getBoundingClientRect().height)>300,true);
    const dir = process.env.TEAMX_ENVIRONMENT_ARTIFACTS;
    if(dir) {fs.mkdirSync(dir,{recursive:true});await page.screenshot({path:dir+'/desktop.png'});}
    await page.locator('#environment-expand').click();
    assert.equal(await page.locator('#conversation').evaluate(el=>el.inert),true);
    await page.locator('#environment-expand').click();
    await page.locator('#conversation-calendar').click();
    assert.equal(await page.locator('#environment-screen iframe').count(),0);
    assert.equal(await page.locator('#calendar-page').isVisible(),true);
    await open();
    await page.locator('#conversation-files').click();
    assert.equal(await page.locator('#environment-screen iframe').count(),0);
    await open();
    await page.locator('#environment-refresh').click();
    await page.locator('#environment-status').filter({hasText:'已连接'}).waitFor();
    assert.equal(await page.locator('#environment-screen iframe').count(),1);
    await page.setViewportSize({width:390,height:844});
    assert.equal(await page.locator('#conversation').evaluate(el=>el.inert),true);
    await waitForPixels(); await page.waitForTimeout(250);
    if(dir) await page.screenshot({path:dir+'/phone.png'});
    await page.locator('#environment-close').click();
    await page.waitForFunction(() => !document.querySelector('#conversation').inert);
    assert.equal(await page.locator('#message-input').inputValue(),'观看环境时保留的草稿');
    await page.setViewportSize({width:1600,height:1000});
    await open();
    await page.locator(`[data-room-key*='${fixture.other_room.room_instance}'] .room-choice`).click();
    await page.waitForFunction(() => !document.querySelector('#message-input').disabled);
    assert.equal(await page.locator('#environment-screen iframe').count(),0);
    await page.locator('#conversation-environment').click();
    await page.locator('#environment-status').filter({hasText:'暂未配置'}).waitFor();
    assert.equal(await page.locator('#environment-screen iframe').count(),0);
    assert.deepEqual(errors,[]);
    console.log(JSON.stringify({status:'passed',scope:'real AIO noVNC + TeamX HTTP and Chromium; Agent execution substituted',pixels,checks:['desktop frame pixels','calendar/files switching releases viewer','reconnect','390px overlay and draft','room isolation and empty state'],errors}));
  } finally {await browser.close();}
})().catch(error=>{console.error(error);process.exit(1);});
