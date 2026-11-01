// Browser checks using synthetic map/status data; no robot commands are sent.
const http=require('node:http');
const fs=require('node:fs');
const path=require('node:path');
const assert=require('node:assert/strict');
const {chromium}=require(process.env.CODEX_PRIMARY_RUNTIME_NODE_MODULES?process.env.CODEX_PRIMARY_RUNTIME_NODE_MODULES+'/playwright':'playwright');
const root=path.resolve(__dirname,'../agent/ui'),token='a'.repeat(40),commands=[],commandReplies=new Map();
let revision='test-map-1',regionCode=200,applyRegion=true,guardEnabled=false,dropNextRead=false,dropNextMutation=false;
const values={'2/1':6,'2/2':0,'3/1':95,'4/1':6,'4/4':1,'4/5':2,'4/25':0,'7/1':50,'4/23':(2<<16)|(20<<8),'4/27':0,'28/1':10,'4/50':JSON.stringify([{k:'AutoDry',v:1},{k:'SmartCharge',v:1}]),'4/22':JSON.stringify({obstacle_detect_switch:true,human_detect_switch:false,untouched:42}),'5/4':JSON.stringify([{id:9,en:true,st:'22:00',et:'08:00',wk:127,ss:0,extra:'keep'},{id:10,en:false,st:'12:00',et:'13:00',wk:1,ss:0}])};
let passable=[[-700,-700,-400,-700]],impassable=[[100,-700,100,-400]];
const grid=Buffer.alloc(40*30);
for(let y=2;y<28;y++)for(let x=2;x<38;x++)grid[y*40+x]=(x===2||x===37||y===2||y===27)?255:x<20?1:2;
const server=http.createServer((req,res)=>{
 if(req.method==='GET'){
  const names={'/':'index.html','/app.js':'app.js','/style.css':'style.css','/settings.js':'settings.js'},name=names[req.url.split('?')[0]];
  if(!name){res.writeHead(404);res.end();return;}
  const type=name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html';
  res.setHeader('Content-Type',type+'; charset=utf-8');
  res.setHeader('Content-Security-Policy',"default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'none'");
  res.end(fs.readFileSync(path.join(root,name)));return;
 }
 let body='';req.on('data',b=>body+=b);req.on('end',()=>{
  if(req.headers.authorization!=='Bearer '+token){res.writeHead(401);res.end(JSON.stringify({error:'unauthorized'}));return;}
  const q=JSON.parse(body),requestID=req.headers['x-dreame-request-id'];if(requestID&&commandReplies.has(requestID)){res.setHeader('Content-Type','application/json');res.end(commandReplies.get(requestID));return;}let result;
  if(q.method==='miIO.info')result={model:'dreame.vacuum.r2505',fw_ver:'4.3.9_2021'};
  else if(q.method==='get_properties'){
   if(dropNextRead){dropNextRead=false;res.destroy();return;}
   values['4/19']=regionCode===200?'matched':'mismatched';values['14/3']=regionCode;
   result=q.params.map(p=>({...p,code:0,value:values[p.siid+'/'+p.piid]}));
  }else if(q.method==='local.region_lock'){commands.push(q);if(applyRegion)regionCode=q.params.code;result={dispatched:true,confirmed:false};}
  else if(q.method==='local.wifi_status')result={ap_mode:false,addresses:['192.168.2.11'],ssid:'Home WiFi',signal_dbm:-65,signal_percent:70,state:'idle',error:'',portal_http:false,portal_dns:false,portal_error:''};
  else if(q.method==='local.region_guard_status')result={enabled:guardEnabled,hook_loaded:true,blocked_since_boot:false,restart_required:false};
  else if(q.method==='local.region_guard'){commands.push(q);guardEnabled=q.params.enabled;result={enabled:guardEnabled,hook_loaded:true};}
  else if(q.method==='local.wifi_setup'){commands.push(q);result={accepted:true,state:'connecting',ap_will_disconnect:true};}
  else if(q.method==='local.setting'){commands.push(q);if(JSON.stringify(q.params.expected)!==JSON.stringify(values[q.params.siid+'/'+q.params.piid])){res.writeHead(502);res.end(JSON.stringify({error:'setting changed'}));return;}values[q.params.siid+'/'+q.params.piid]=q.params.value;result=[{code:0}];}
  else if(q.method==='local.thresholds'){commands.push(q);if(q.params.revision!==revision){res.writeHead(502);res.end(JSON.stringify({error:'map changed'}));return;}passable=q.params.passable;impassable=q.params.impassable;revision='edited-'+revision;result={code:0};}
  else if(q.method==='local.reboot'){commands.push(q);result={accepted:true,rebooting:true};}
  else if(q.method==='local.map_view')result={empty:false,map_id:2,width:40,height:30,grid_size:50,origin:[-1000,-1000],pixels:grid.toString('base64'),rooms:[{id:1,name:'Кухня',x:10,y:15},{id:2,name:'Гостиная',x:28,y:15}],revision,source:'local_saved_file',live:false,thresholds_editable:true,passable_thresholds:passable,impassable_thresholds:impassable};
  else{commands.push(q);result=q.method==='set_properties'?q.params.map(p=>({...p,code:0})):{code:0,out:[]};if(dropNextMutation){dropNextMutation=false;if(requestID)commandReplies.set(requestID,JSON.stringify({result}));res.destroy();return;}}
  res.setHeader('Content-Type','application/json');if(requestID)commandReplies.set(requestID,JSON.stringify({result}));res.end(JSON.stringify({result}));
 });
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const url='http://127.0.0.1:'+server.address().port;
 const browser=await chromium.launch({headless:true,executablePath:process.env.DREAME_TEST_CHROMIUM||undefined,args:['--no-sandbox','--disable-dev-shm-usage','--no-zygote']});
 const page=await browser.newPage({viewport:{width:1280,height:1000}}),errors=[],external=[];
 page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{if(!r.url().startsWith(url))external.push(r.url());});
 try{
  await page.goto(url);assert(await page.locator('#login').isVisible());
  await page.locator('#token').fill(token);await page.locator('#login-form button').click();await page.locator('#dashboard').waitFor({state:'visible'});await page.getByRole('button',{name:'Кухня · 1',exact:true}).waitFor();
  assert.equal(await page.locator('#battery').innerText(),'95%');
  const xy=await page.evaluate(async()=>{const m=await import('/app.js');return m.pixelToRobot({origin:[-1000,-1000],grid_size:50},4,8);});assert.deepEqual(xy,[-800,-600]);
  await page.locator('[data-command=start]').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);assert.deepEqual(commands.at(-1).params,{siid:2,aiid:1,in:[]});
  await page.getByRole('button',{name:'Кухня · 1',exact:true}).click();await page.locator('#clean-rooms').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);
  assert.deepEqual(JSON.parse(commands.at(-1).params.in[1].value),{selects:[[1,1,1,2,1]]});
  await page.getByRole('button',{name:'Гостиная · 2',exact:true}).click();await page.getByText('Разделение и объединение комнат',{exact:true}).click();await page.locator('#prepare-map-edit').click();await page.locator('#apply-map-edit').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);
  assert.deepEqual(JSON.parse(commands.at(-1).params.in[0].value),{msr:[1,2],mapid:2});
  await page.locator('#map-operation').selectOption('split');await page.getByRole('button',{name:'Кухня · 1',exact:true}).click();
  await page.locator('#map').click({position:{x:60,y:60}});await page.locator('#map').click({position:{x:100,y:90}});await page.locator('#prepare-map-edit').click();
  const count=commands.length;revision='test-map-2';await page.locator('#apply-map-edit').click();await page.waitForFunction(()=>document.querySelector('#toast').textContent.includes('Карта изменилась'));assert.equal(commands.length,count);
  await page.locator('#refresh-map').click();await page.locator('#clear-selection').click();await page.getByRole('button',{name:'Кухня · 1',exact:true}).click();await page.locator('#map').click({position:{x:60,y:60}});await page.locator('#map').click({position:{x:100,y:90}});await page.locator('#prepare-map-edit').click();await page.locator('#apply-map-edit').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);
  const split=JSON.parse(commands.at(-1).params.in[0].value);assert.equal(split.mapid,2);assert.equal(split.dsrid.length,5);assert.equal(split.dsrid[4],1);
  const before=commands.length;await page.locator('#fan').selectOption('2');assert.equal(commands.length,before);await page.locator('#save-fan').click();await page.waitForFunction(()=>!document.querySelector('#save-fan').disabled);assert.deepEqual(commands.at(-1).params,[{siid:4,piid:4,value:2}]);
  assert.equal(await page.locator('#region-status').innerText(),'Блокировка снята');
  await page.locator('#region-code').selectOption('401');await page.locator('#save-region').click();await page.waitForFunction(()=>!document.querySelector('#save-region').disabled);
  assert.deepEqual(commands.at(-1),{method:'local.region_lock',params:{code:401}});assert.equal(await page.locator('#region-status').innerText(),'Региональная блокировка активна');assert.match(await page.locator('#region-result').innerText(),/подтверждено/);
  await page.locator('#region-code').selectOption('200');await page.locator('#save-region').click();await page.waitForFunction(()=>!document.querySelector('#save-region').disabled);assert.equal(await page.locator('#region-status').innerText(),'Блокировка снята');
  applyRegion=false;await page.locator('#region-code').selectOption('401');await page.locator('#save-region').click();await page.waitForFunction(()=>!document.querySelector('#save-region').disabled);assert.match(await page.locator('#region-result').innerText(),/пока не подтверждён/);assert.equal(await page.locator('#region-status').innerText(),'Блокировка снята');
  await page.locator('#region-guard').check();await page.locator('#save-guard').click();await page.waitForFunction(()=>!document.querySelector('#save-guard').disabled);assert.deepEqual(commands.at(-1),{method:'local.region_guard',params:{enabled:true}});await page.waitForFunction(()=>document.querySelector('#guard-status').textContent.includes('Защита включена'));
  assert.equal(await page.locator('#wifi-details').getAttribute('open'),null);assert.match(await page.locator('#wifi-summary').innerText(),/Home WiFi.*70%/);assert(await page.evaluate(()=>document.querySelector('#wifi-panel')===document.querySelector('#dashboard').lastElementChild));assert.equal(await page.locator('footer').count(),0);
  await page.locator('#wifi-details summary').click();
  await page.locator('#wifi-ssid').fill('Home');await page.locator('#wifi-password').fill('testpass123');await page.locator('#save-wifi').click();await page.waitForFunction(()=>!document.querySelector('#save-wifi').disabled);assert.deepEqual(commands.at(-1),{method:'local.wifi_setup',params:{ssid:'Home',password:'testpass123',open:false}});assert.equal(await page.locator('#wifi-password').inputValue(),'');assert.match(await page.locator('#wifi-result').innerText(),/Настройки приняты/);assert(!JSON.stringify(await page.evaluate(()=>({...localStorage,...sessionStorage}))).includes('testpass123'));
  await page.locator('#map-operation').selectOption('merge');await page.locator('#clear-selection').click();await page.getByRole('button',{name:'Кухня · 1',exact:true}).click();await page.getByRole('button',{name:'Гостиная · 2',exact:true}).click();await page.locator('#room-repeats').selectOption('2');await page.locator('#room-fan').selectOption('3');await page.locator('#room-water').selectOption('1');await page.locator('#clean-rooms').click();await page.waitForFunction(()=>!document.querySelector('#clean-rooms').disabled);assert.deepEqual(JSON.parse(commands.at(-1).params.in[1].value),{selects:[[1,2,3,1,1],[2,2,3,1,1]]});
  await page.locator('#threshold-details summary').click();assert.equal(await page.locator('#threshold-choice option').count(),3);await page.locator('#threshold-choice').selectOption('impassable:0');await page.locator('#threshold-kind').selectOption('passable');await page.locator('#save-threshold').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);assert.equal(commands.at(-1).method,'local.thresholds');assert.equal(commands.at(-1).params.passable.length,2);assert.equal(commands.at(-1).params.impassable.length,0);
  await page.locator('#refresh-map').click();await page.waitForFunction(()=>document.querySelectorAll('#threshold-choice option').length===3);await page.locator('#threshold-choice').selectOption('');await page.locator('#threshold-kind').selectOption('impassable');await page.locator('#draw-threshold').click();await page.locator('#map').click({position:{x:30,y:30}});await page.locator('#map').click({position:{x:90,y:60}});await page.locator('#save-threshold').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);assert.equal(commands.at(-1).params.impassable.length,1);
  await page.locator('#refresh-map').click();await page.locator('#threshold-choice').selectOption('impassable:0');await page.locator('#delete-threshold').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);assert.equal(commands.at(-1).params.impassable.length,0);assert.equal(commands.at(-1).params.passable.length,2);
  await page.locator('#settings-details summary').click();await page.waitForFunction(()=>document.querySelector('#settings-list').children.length>5);await page.locator('#setting-child_lock').check();await page.locator('[data-setting=child_lock] button').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);assert.equal(commands.at(-1).method,'local.setting');assert.equal(commands.at(-1).params.value,1);
  await page.locator('#setting-cleaning_mode').selectOption('0');await page.locator('[data-setting=cleaning_mode] button').click();await page.waitForFunction(()=>!document.querySelector('[data-command=start]').disabled);assert.equal(commands.at(-1).params.value,((2<<16)|(20<<8)|2));
  const preserved=await page.evaluate(async()=>{const m=await import('/app.js');return {bits:m.settingValue({bit:2},false,0x183),json:JSON.parse(m.settingValue({json_key:'human_detect_switch'},true,JSON.stringify({human_detect_switch:false,keep:17}))),packed:m.settingValue({packed:'humidity'},3,(2<<16)|(20<<8)|2)};});assert.equal(preserved.bits,0x181);assert.equal(preserved.json.keep,17);assert.equal(preserved.json.human_detect_switch,true);assert.equal(preserved.packed,(3<<16)|(20<<8)|2);
  await page.locator('#settings-search').fill('блокировка');assert(await page.locator('[data-setting=child_lock]').isVisible());assert(!await page.locator('[data-setting=cleaning_mode]').isVisible());await page.locator('#settings-search').fill('');
  const beforeVisibility=commands.length;await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:true});document.dispatchEvent(new Event('visibilitychange'));});await page.evaluate(()=>{Object.defineProperty(document,'hidden',{configurable:true,value:false});document.dispatchEvent(new Event('visibilitychange'));});await page.waitForTimeout(800);assert.equal(commands.length,beforeVisibility);assert(await page.locator('#dashboard').isVisible());
  const mutationCount=commands.length;dropNextRead=true;await page.evaluate(()=>window.dispatchEvent(new Event('online')));await page.waitForFunction(()=>document.querySelector('#connection').textContent==='Подключён');await page.waitForTimeout(700);assert.equal(commands.length,mutationCount);assert(await page.locator('#dashboard').isVisible());assert.equal(await page.evaluate(()=>sessionStorage.getItem('dreame-token')),token);
  dropNextMutation=true;await page.locator('[data-command=locate]').click();await page.waitForFunction(()=>!document.querySelector('[data-command=locate]').disabled);assert.equal(commands.length,mutationCount+1);assert.match(await page.locator('#toast').innerText(),/Команда принята|Команда могла выполниться/);
  await page.evaluate(()=>{window._originalFetch=window.fetch;window._failedWrites=0;window.fetch=(url,options)=>{const q=JSON.parse(options.body);if(q.method==='action'&&q.params.siid===7){window._failedWrites++;return Promise.reject(new TypeError('Failed to fetch'));}return window._originalFetch(url,options);};});await page.locator('[data-command=locate]').click();await page.waitForFunction(()=>!document.querySelector('[data-command=locate]').disabled);assert.equal(await page.evaluate(()=>window._failedWrites),1);assert.match(await page.locator('#toast').innerText(),/Команда могла выполниться/);await page.evaluate(()=>window.fetch=window._originalFetch);
  const rebootCount=commands.filter(x=>x.method==='local.reboot').length;page.once('dialog',d=>d.dismiss());await page.locator('[data-command=reboot]').click();assert.equal(commands.filter(x=>x.method==='local.reboot').length,rebootCount);page.once('dialog',d=>d.accept());await page.locator('[data-command=reboot]').click();await page.waitForFunction(()=>!document.querySelector('[data-command=reboot]').disabled);assert.equal(commands.filter(x=>x.method==='local.reboot').length,rebootCount+1);
  await page.screenshot({path:'/tmp/dreame-native-web-desktop.png',fullPage:true});
  await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));await page.screenshot({path:'/tmp/dreame-native-web-mobile.png',fullPage:true});
  await page.locator('#logout').click();assert(await page.locator('#login').isVisible());assert.equal(await page.evaluate(()=>sessionStorage.getItem('dreame-token')),null);
  await page.goto(url+'/?setup=wifi');await page.locator('#wifi-ssid').waitFor({state:'visible',timeout:5000});assert(await page.locator('#wifi-ssid').isVisible());assert(await page.locator('#login').isVisible());await page.locator('#token').fill(token);await page.locator('#wifi-ssid').fill('Setup');await page.locator('#wifi-open').check();await page.locator('#save-wifi').click();await page.waitForFunction(()=>document.querySelector('#wifi-result').textContent.includes('Настройки приняты'));assert.deepEqual(commands.at(-1),{method:'local.wifi_setup',params:{ssid:'Setup',password:'',open:true}});
  assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
  console.log('Browser checks passed: reboot confirmation, collapsed final Wi-Fi panel and signal, repeated multi-room cleaning, threshold add/edit/delete, packed settings preservation, reconnection without repeating commands,  login, status, coordinates, start, room cleaning, merge, split, stale-map blocking, settings, region lock/unlock and unconfirmed outcome, guard mode, Wi-Fi submission without password storage, captive setup form before login, mobile layout, logout, no external requests');
 }finally{await browser.close();server.close();}
})().catch(e=>{console.error(e);server.close();process.exitCode=1;});
