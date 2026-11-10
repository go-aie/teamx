// Optional real Chromium driver; requires the operator's Playwright installation.
// node internal/room/testdata/member_notifications_browser.cjs ABS_OUTPUT
const {chromium}=require('playwright');
const path=require('node:path');
(async()=>{
  const output=process.argv[2];
  if(!output || !path.isAbsolute(output))throw Error('absolute fresh acceptance output required');
  const {verifyMemberNotificationsBrowser}=await import('./member_notifications_browser.mjs');
  const browser=await chromium.launch({headless:true});
  const page=await browser.newPage({viewport:{width:1440,height:1000}});
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  try{
    const report=await verifyMemberNotificationsBrowser({output,driver:'playwright-chromium',errors,
      tab:{playwright:page,goto:url=>page.goto(url),screenshot:options=>page.screenshot(options)},
      browser:{capabilities:{get:async id=>{
        if(id!=='viewport')throw Error('unsupported capability');
        return {set:size=>page.setViewportSize(size),reset:()=>page.setViewportSize({width:1440,height:1000})};
      }}}});
    if(errors.length)throw Error('browser JavaScript error; inspect retained evidence');
    console.log(JSON.stringify(report));
  }catch(error){
    await page.screenshot({path:path.join(output,'browser-failed.png'),fullPage:true});
    throw error;
  }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
