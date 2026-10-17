// Run: PLAYWRIGHT_MODULE=/absolute/path/to/playwright node capture.cjs
// Optional CHROMIUM_EXECUTABLE selects an already installed Chromium.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const { pathToFileURL } = require('node:url');
const path = require('node:path');
const fs = require('node:fs');
const assert = require('node:assert/strict');
(async () => {
 const browser = await chromium.launch({headless:true, ...(process.env.CHROMIUM_EXECUTABLE ? {executablePath:process.env.CHROMIUM_EXECUTABLE}: {})});
 const page = await browser.newPage({viewport:{width:1440,height:1080},deviceScaleFactor:1});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const root=pathToFileURL(path.join(__dirname,'index.html')).href;
 const out=path.join(__dirname,'screenshots');fs.mkdirSync(out,{recursive:true});
 const counts={create:7,join:7,add:2,revoke:5,reset:2,recovery:13};
 for(const theme of ['light','dark']) {
  for(const [flow,count] of Object.entries(counts)) for(let step=0;step<count;step++) {
   await page.goto(`${root}?flow=${flow}&step=${step}&theme=${theme}`);
   assert.equal(await page.locator('#surfaces .surface').count(),2);
   assert.equal(await page.evaluate(()=>getComputedStyle(document.body).backgroundColor),theme==='light'?'rgb(255, 255, 255)':'rgb(31, 31, 31)');
   assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`${flow}/${step} overflow`);
  }
  for(const [name,flow,step] of [['create','create',1],['handoff','create',2],['authorize','create',3],['join-check','join',4],['add','add',0],['revoke','revoke',1],['reset','reset',0],['recovery','recovery',4]]) {
   await page.goto(`${root}?flow=${flow}&step=${step}&theme=${theme}`);
   await page.screenshot({path:path.join(out,`${name}-${theme}.png`),fullPage:true});
  }
 }
 await page.goto(`${root}?flow=reset`);
 const reset=page.getByRole('button',{name:'Reset fleet',exact:true});
 assert.equal(await reset.isDisabled(),true);
 await page.getByLabel('Type reset to confirm').fill('RESET');assert.equal(await reset.isDisabled(),true);
 await page.getByLabel('Type reset to confirm').fill('reset');await reset.click();
 await page.getByText('Fleet reset here',{exact:true}).waitFor();
 await page.goto(`${root}?flow=create`);
 await page.getByLabel('Machine name',{exact:true}).fill('<img src=x onerror=alert(1)>');
 await page.getByRole('button',{name:'Continue',exact:true}).click();
 assert.equal(await page.locator('#surfaces img').count(),0);
 assert.equal(await page.getByText('<img src=x onerror=alert(1)>',{exact:true}).count(),2);
 await page.getByRole('button',{name:'Create passkey',exact:true}).click();
 await page.getByRole('heading',{name:'Passkey created. One more step.'}).waitFor();
 assert.equal(await page.locator('#browser').getByRole('button',{name:/retry|authorize|create passkey/i}).count(),0);
 await page.goto(`${root}?flow=create&step=3`);
 await page.getByRole('button',{name:'Authorize machine',exact:true}).click();
 await page.locator('#browser').getByRole('heading',{name:'Approval sent.'}).waitFor();
 assert.equal(await page.locator('#desktop').getByRole('heading',{name:'Fleet created'}).count(),0);
 await page.getByRole('button',{name:'Keep working',exact:true}).click();
 await page.getByRole('button',{name:'Resume setup',exact:true}).click();
 assert.equal(await page.getByRole('dialog').count(),1);
 for(const theme of ['light','dark']) {
  await page.setViewportSize({width:390,height:844});
  await page.goto(root.replace('index.html','browser.html')+`?flow=create&step=3&theme=${theme}`);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'mobile overflow');
  await page.screenshot({path:path.join(out,`browser-mobile-${theme}.png`),fullPage:true});
 }
 assert.deepEqual(errors,[]);await browser.close();
 console.log('72 themed states rendered; interaction checks passed; 18 screenshots captured.');
})().catch(e=>{console.error(e);process.exitCode=1;});
