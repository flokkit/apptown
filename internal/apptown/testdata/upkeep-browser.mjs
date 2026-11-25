const { chromium } = await import(process.env.APPTOWN_PLAYWRIGHT_MODULE);
import { mkdir, writeFile } from 'node:fs/promises';
import assert from 'node:assert/strict';
const mode = process.env.DESIGN_BASELINE === 'before' ? 'before' : 'after';
const base = process.env.DESIGN_URL, out = process.env.DESIGN_OUT;
await mkdir(out,{recursive:true});
const browser=await chromium.launch({headless:true,args:['--no-proxy-server', ...(process.env.DESIGN_RESOLVE ? [`--host-resolver-rules=MAP ${process.env.DESIGN_RESOLVE}`] : [])]});
const context=await browser.newContext({ignoreHTTPSErrors:true,timezoneId:'UTC',deviceScaleFactor:1});
const page=await context.newPage();
const errors=[];page.on('pageerror',e=>{errors.push(String(e));console.log('pageerror',String(e))});
const signed=await context.request.post(base+'/api/v1/auth/login',{data:{username:'sam',password:'tour-password-not-secret'},headers:{Origin:base}});
assert.equal(signed.status(),200,await signed.text());
const overview=await context.request.get(base+'/api/v1/projects/nextup/overview');
const data=await overview.json();await writeFile(out+'/overview.json',JSON.stringify(data,null,2));
if(mode==='after') {
 assert.equal(data.active.length,2);
 assert.equal(data.history.filter(e=>e.change).length,4);
 assert.equal(data.history.reduce((n,e)=>n+(e.upkeep?.count||0),0),26);
}
const shots=[];
for(const width of [390,1440]) for(const theme of ['light','dark']) for(const name of ['changes','upkeep']) {
 await page.setViewportSize({width,height:width===390?844:900});
 await page.goto(`${base}/projects/nextup/changes${name==='upkeep'?'/upkeep':''}?theme=${theme}`);
 await page.waitForFunction(()=>document.querySelectorAll('.application-overview-card, .active-run').length>0);
 await page.evaluate(()=>document.fonts.ready);
 await page.waitForTimeout(400);
 if(mode==='after' && name==='changes') {
  assert.equal(await page.getByRole('link',{name:/#31 Update node, vitest/}).count(),1,'approval remains individual');
  await page.getByRole('link',{name:/1 upkeep change in progress/}).waitFor();
 }
 const layout=await page.evaluate(()=>({overflow:document.documentElement.scrollWidth>innerWidth,headings:[...document.querySelectorAll('h1')].map(h=>h.textContent),rows:document.querySelectorAll('.application-overview-card').length}));
 assert.equal(layout.overflow,false,`${name} ${width} ${theme} overflows`);
 assert.equal(layout.headings.length,1);
 assert.equal(await page.getByText('AppTown was updated since this page opened.', {exact:false}).count(),0,'served UI matches its build marker');
 await page.screenshot({path:`${out}/${name}-${width}-${theme}.png`});
 await page.screenshot({path:`${out}/${name}-${width}-${theme}-full.png`,fullPage:true});
 shots.push({name,width,theme,...layout});
}
await writeFile(out+'/capture.json',JSON.stringify({mode,shots,errors},null,2));assert.deepEqual(errors,[]);
await browser.close();console.log(JSON.stringify(shots));
