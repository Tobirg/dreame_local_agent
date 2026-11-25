import {settings} from '/settings.js';
import {mountVoice} from '/voice.js';
const $=id=>document.getElementById(id);
const captiveSetup=new URLSearchParams(location.search).get('setup')==='wifi';
const statusNames={0:'Ожидание',1:'Пауза',2:'Уборка',3:'Возвращается',4:'Локальная уборка',5:'Вдоль стен',6:'Заряжается',12:'Ошибка',13:'Ручное управление',14:'Спит',17:'Ожидание',18:'Уборка комнат',19:'Уборка зоны',20:'Точечная уборка',21:'Строит карту',15:'Тестовый режим',1527:'Саморемонт станции',22:'Патрулирование',23:'Едет к точке камеры',1501:'Проверка воды'};
// Station codes observed on r2505; other codes are shown as numbers.
const stationNames={0:'ожидание',1:'мойка салфеток',2:'сушка'};
const palette=['#7fd9c2','#a3bcec','#e5bd8a','#bcaceb','#a7d39a','#e3a5b4','#93cdd9','#d4d093'];
let token=sessionStorage.getItem('dreame-token')||localStorage.getItem('dreame-token')||'';
let timer=null,busy=false,refreshing=false,view=null,selected=new Set(),points=[],pending=null,toastTimer=null,fanDirty=false,volumeDirty=false;
let viewEpoch=0,resumeTimer=null,roomOptionsDirty=false,thresholdDrawing=false,thresholdDraft=null,settingsLoading=false,settingsSnapshot=new Map(),settingsRows=new Map(),rebootingUntil=0;
const activeReads=new Set(),settingsDirty=new Set();
let zoneDrawing=false,zoneSpot=false,zone=[],zoneHover=null,zoneDrag=null,dndDirty=false,careShown='',restrictDraft=null,restrictDrawing=false,restrictPoint=null;
const canvas=$('map'),ctx=canvas.getContext('2d');

export function pixelToRobot(v,x,y){
 if(!Array.isArray(v.origin)||v.origin.length!==2||!Number.isFinite(v.grid_size)||v.grid_size<=0||![x,y,...v.origin].every(Number.isFinite))throw Error('Некорректная калибровка карты');
 return [Math.round(v.origin[0]+x*v.grid_size),Math.round(v.origin[1]+y*v.grid_size)];
}
export function roomCommand(ids,fan,water,repeats=1){
 if(!ids.length||!ids.every(id=>Number.isInteger(id)&&id>0))throw Error('Выбери комнаты');
 if(!Number.isInteger(fan)||fan<0||fan>3||!Number.isInteger(water)||water<1||water>3)throw Error('Параметры уборки ещё не получены от робота');
 if(!Number.isInteger(repeats)||repeats<1||repeats>3)throw Error('Допустимо от 1 до 3 повторов');
 return {name:'clean_rooms',rooms:ids,repeats,suction:fan,water};
}
const command=(name,args={})=>control('/api/v1/command',{name,...args});
function toast(message,error=false){clearTimeout(toastTimer);$('toast').textContent=message;$('toast').classList.toggle('error',error);$('toast').hidden=false;toastTimer=setTimeout(()=>$('toast').hidden=true,7000);}
function isRead(method){return method==='get_properties'||method==='miIO.info'||method==='local.map_view'||method==='local.live_pose'||method==='local.maps'||method.endsWith('_status')||method==='/api/v1/state'||method==='/api/v1/capabilities'||method==='/api/v1/modules'||method==='/api/v1/system/update'||method==='/api/v1/system/firmware'||method==='/api/v1/logs';}
function connectionLost(message){$('connection').textContent=rebootingUntil>Date.now()?'Перезагрузка…':'Восстанавливаю связь…';$('connection').classList.add('connection-offline');$('reconnect').hidden=!token;$('reconnect').title=message;}
async function api(method,params=null){
 const read=isRead(method),requestID=read?null:Array.from(crypto.getRandomValues(new Uint8Array(16)),x=>x.toString(16).padStart(2,'0')).join('');
 for(let attempt=0;attempt<(read?2:1);attempt++){
  const controller=new AbortController(),timeout=setTimeout(()=>controller.abort('timeout'),read?15000:40000);
  if(read)activeReads.add(controller);
  try{
   // "/api/v1/..." methods are agent paths (GET without params); everything else is a /rpc method.
   const v1=method.startsWith('/api/v1/'),post=!v1||params!==null;
   const response=await fetch(v1?method:'/rpc',{method:post?'POST':'GET',headers:{...(post?{'Content-Type':'application/json'}:{}),'Authorization':'Bearer '+token,...(requestID?{'X-Dreame-Request-ID':requestID}:{})},body:post?JSON.stringify(v1?params:{method,params}):undefined,credentials:'omit',cache:'no-store',signal:controller.signal});
   const body=await response.json();if(!response.ok){const error=Error(body.error||'Ошибка HTTP '+response.status);error.status=response.status;throw error;}return body.result;
  }catch(e){
   if(controller.signal.reason==='suspend'||controller.signal.reason==='logout'){const error=Error('Чтение приостановлено');error.suspended=true;throw error;}
   if(e.status)throw e;
   if(read&&attempt===0&&!document.hidden&&token){await new Promise(resolve=>setTimeout(resolve,150));continue;}
   throw Error(read?'Нет ответа от робота. Подключение будет восстановлено автоматически.':'Связь прервалась. Команда могла выполниться: проверь состояние перед повтором.');
  }finally{clearTimeout(timeout);activeReads.delete(controller);}
 }
}

export function assertCommand(result,method,params){
 if(result?.patrol==='started')return; // the agent runs it; no robot reply yet
 if(method==='local.patrol_schedule'||method==='local.patrol_schedule_delete')return; // agent-side list, no robot call
 const rows=Array.isArray(result)?result:[result];
 if(!rows.length||rows.some(r=>!r||r.code!==0))throw Error('Робот отклонил команду: '+JSON.stringify(result));
 // EDIT_COMMAND answers "0" in its out value when the robot took the change.
 if(/_quick_command$/.test(params?.name||'')&&String(result?.out?.[0]?.value)!=='0')throw Error('Робот не принял быструю команду: '+JSON.stringify(result?.out));
 if(method==='local.thresholds'||method==='local.restrictions'||method==='local.room_settings'||method==='local.room_order'||method==='local.map_use'||method==='local.carpets'||method==='local.carpet_types'||method==='local.carpet_clean'||method==='local.furniture'||(method==='action'&&params?.siid===6&&params?.aiid===2)){
  if(Array.isArray(result)||!Array.isArray(result.out))throw Error('Map operation result is missing; outcome unknown.');
  const operation=result.out.filter(row=>row&&row.piid===6);
  if(operation.length!==1||!Number.isSafeInteger(operation[0].value))throw Error('Map operation result is invalid; outcome unknown.');
  if(operation[0].value!==0)throw Error('Map operation rejected (op_ret='+operation[0].value+')');
 }
}
async function control(method,params,before=null){
 if(busy)return;busy=true;disableControls(true);
 try{if(before)await before();const result=await api(method,params);assertCommand(result,method,params);toast('Команда принята роботом');setTimeout(()=>refresh(),900);return result;}
 catch(e){toast(e.message,true);throw e;}
 finally{busy=false;disableControls(false);}
}
function disableControls(disabled){
 document.querySelectorAll('[data-command],#stage-update,#apply-update,#rollback-update,#stage-module,#install-module,#save-fan,#save-volume,#save-wifi,#send-rpc,.setting-row input,.setting-row select,.setting-apply').forEach(b=>b.disabled=disabled);
 $('clean-rooms').disabled=disabled||selected.size===0;
 $('clean-zone').disabled=disabled||zone.length!==(zoneSpot?1:2);
 $('cancel-zone').disabled=!zoneDrawing&&!zone.length;
 $('save-restrict').disabled=disabled||!restrictDraft||!view?.restrictions_editable;
 $('draw-restrict').disabled=disabled||!view?.restrictions_editable;
 $('apply-map-edit').disabled=disabled||!pending;
 $('save-threshold').disabled=disabled||!thresholdDraft||!view?.thresholds_editable;
 $('delete-threshold').disabled=disabled||!$('threshold-choice').value||!view?.thresholds_editable;
 $('draw-threshold').disabled=disabled||!view?.thresholds_editable;
}
function renderNetwork(wifi){
 const network=wifi.ssid||'Не подключён',signal=Number.isFinite(wifi.signal_percent)?' · ~'+wifi.signal_percent+'%':'';
 $('wifi-summary').textContent=network+signal;$('wifi-details').open=$('wifi-details').open||wifi.ap_mode||captiveSetup;
 $('wifi-status').textContent='Сеть: '+network+signal+(Number.isFinite(wifi.signal_dbm)?' ('+wifi.signal_dbm+' dBm)':'')+' · '+(wifi.ap_mode?'Режим точки доступа':'Адреса: '+wifi.addresses.join(', '))+' · '+({idle:'Ожидание',connecting:'Подключение к сети',connected:'Wi-Fi подключён',failed:'Ошибка подключения'}[wifi.state]||wifi.state)+(wifi.error?' · '+wifi.error:'')+(wifi.ap_mode?' · портал: '+(wifi.portal_http&&wifi.portal_dns?'работает':wifi.portal_error||'запускается'):'');
}
// ---- Agent self-update: the agent checks the uploaded binary, a helper swaps it and rolls back if it does not come up.
let stagedUpdate=null;
function describeUpdate(r){
 if(!r)return '';
 return {applying:'идёт обновление',updated:'обновлено '+r.from+' → '+r.to,rolled_back:'версия '+r.to+' не запустилась, возвращена '+r.from,failed:'ошибка'}[r.status]+(r.error?' ('+r.error+')':'');
}
function renderUpdate(u){
 $('update-status').textContent='Установлена версия '+u.current+(u.last?' · последнее обновление: '+describeUpdate(u.last):'');
 $('rollback-update').hidden=!u.previous||u.applying;if(u.previous)$('rollback-update').textContent='Вернуть версию '+u.previous;
}
async function loadUpdate(){renderUpdate(await api('/api/v1/system/update'));await loadFirmware().catch(()=>{});}
function renderFirmware(f){$('firmware-updates').checked=f.enabled;$('firmware-status').textContent=f.enabled?'Обновления прошивки разрешены. Штатное обновление сбросит рут, сервис и защиту региона.':'Обновления прошивки выключены — агент обезвредил загрузчик, установка ничего не затрёт.';}
async function loadFirmware(){renderFirmware(await api('/api/v1/system/firmware'));}
$('firmware-updates').onchange=async()=>{if(busy)return;busy=true;disableControls(true);try{renderFirmware(await api('/api/v1/system/firmware',{enabled:$('firmware-updates').checked}));}catch(e){toast(e.message,true);await loadFirmware().catch(()=>{});}finally{busy=false;disableControls(false);}};
async function runUpdate(source,label){
 if(busy)return;busy=true;disableControls(true);
 try{
  const start=await api('/api/v1/system/update/apply',{source});
  $('update-result').textContent=label+' Агент перезапускается, связь пропадёт на несколько секунд…';
  for(let i=0;i<60;i++){
   await new Promise(resolve=>setTimeout(resolve,2000));
   let u;try{u=await api('/api/v1/system/update');}catch{continue;}
   if(u.applying||u.last?.id!==start.id||u.last.status==='applying')continue;
   renderUpdate(u);$('update-result').textContent='Результат: '+describeUpdate(u.last)+'.';
   // The web UI is embedded in the agent: reload to get the new version's pages.
   if(u.last.status==='updated'){$('update-result').textContent+=' Перезагружаю страницу…';setTimeout(()=>location.reload(),1500);}
   return;
  }
  $('update-result').textContent='Агент не ответил за 2 минуты. Обнови страницу и проверь версию.';
 }catch(e){$('update-result').textContent=e.message;}finally{busy=false;disableControls(false);}
}
$('stage-update').onclick=async()=>{
 const file=$('update-file').files[0];if(!file){toast('Выбери файл агента',true);return;}if(busy)return;
 busy=true;disableControls(true);$('update-review').hidden=true;$('update-result').textContent='Проверяю файл…';
 try{
  const response=await fetch('/api/v1/system/update/stage',{method:'POST',headers:{'Authorization':'Bearer '+token,'Content-Type':'application/octet-stream'},body:file,credentials:'omit',cache:'no-store'});
  const body=await response.json();if(!response.ok)throw Error(body.error||'Ошибка HTTP '+response.status);
  stagedUpdate=body.result;
  $('update-summary').textContent='Версия '+stagedUpdate.current+' → '+stagedUpdate.version+' · '+Math.round(stagedUpdate.size/1024)+' КБ · SHA-256: '+stagedUpdate.sha256;
  $('update-review').hidden=false;$('update-result').textContent='Проверь версию и нажми «Обновить».';
 }catch(e){$('update-result').textContent=e.message;}finally{busy=false;disableControls(false);}
};
$('apply-update').onclick=()=>{if(!stagedUpdate)return;$('update-review').hidden=true;$('update-file').value='';stagedUpdate=null;runUpdate('staged','Обновление начато.');};
$('cancel-update').onclick=()=>{stagedUpdate=null;$('update-review').hidden=true;$('update-result').textContent='';};
$('rollback-update').onclick=()=>{if(window.confirm($('rollback-update').textContent+'?'))runUpdate('previous','Возврат начат.');};
// ---- Scoped tokens: only the main token manages them; the new token is shown once.
const scopeNames={read:'чтение',control:'управление'};
let logs=[];
const logNames={agent:'Агент',update:'Обновление агента'};
function showLog(){const l=logs.find(x=>x.source===$('log-source').value);$('log-text').textContent=l?.text||'Пусто';$('log-text').scrollTop=$('log-text').scrollHeight;}
async function loadLogs(){
 logs=await api('/api/v1/logs');const keep=$('log-source').value||'agent';
 $('log-source').replaceChildren(...logs.map(l=>{const o=document.createElement('option');o.value=l.source;o.textContent=logNames[l.source]||'Модуль '+l.source;o.selected=l.source===keep;return o;}));
 showLog();
}
async function loadTokens(){
 const list=await api('/api/v1/system/tokens');$('token-list').replaceChildren();
 for(const t of list){
  const row=document.createElement('div'),label=document.createElement('span'),remove=document.createElement('button');
  row.className='module-row';label.textContent=t.name+' · '+(scopeNames[t.scope]||t.scope)+' · создан '+new Date(t.created).toLocaleDateString();
  remove.textContent='Отозвать';remove.className='ghost';
  remove.onclick=async()=>{if(!window.confirm('Отозвать токен '+t.name+'? Устройства с ним потеряют доступ.'))return;
   try{const response=await fetch('/api/v1/system/tokens/'+encodeURIComponent(t.name),{method:'DELETE',headers:{'Authorization':'Bearer '+token},credentials:'omit',cache:'no-store'});
    const body=await response.json();if(!response.ok)throw Error(body.error||'Ошибка HTTP '+response.status);$('token-result').textContent='Токен '+t.name+' отозван.';await loadTokens();}catch(e){toast(e.message,true);}};
  row.append(label,remove);$('token-list').append(row);
 }
 if(!list.length)$('token-list').textContent='Дополнительных токенов нет.';
}
$('create-token').onclick=async()=>{
 try{const t=await api('/api/v1/system/tokens',{name:$('token-name').value.trim(),scope:$('token-scope').value});
  $('token-result').textContent='Токен '+t.name+' ('+scopeNames[t.scope]+'). Скопируй сейчас, позже его не показать: '+t.token;$('token-name').value='';$('token-add').open=false;await loadTokens();}
 catch(e){$('token-result').textContent=e.message;}
};
// Panels that need the main token are hidden for a read/control token.
const mainTokenOnly=(panel,load)=>load().then(()=>{$(panel).hidden=false;}).catch(e=>{if(e.status===403)$(panel).hidden=true;else toast(e.message,true);});
// ---- Modules: installed packages (GET /api/v1/modules) may add their own panel, mounted as mount(element, ctx).
const stateListeners=[],mountedPanels=new Map();let stagedModule=null;
function dropListeners(name){for(let i=stateListeners.length-1;i>=0;i--)if(stateListeners[i].name===name)stateListeners.splice(i,1);}
function moduleContext(name){
 const path=action=>'/api/v1/m/'+name+'/'+action;
 return {
  read:(action,params={})=>api(path(action),params),
  call:async(action,params={})=>{if(busy)throw Error('Дождись завершения предыдущей команды');busy=true;disableControls(true);try{return await api(path(action),params);}finally{busy=false;disableControls(false);}},
  rpc:(method,params)=>api(method,params),
  onState:fn=>{stateListeners.push({name,fn});},
  toast,
 };
}
async function mountPanel(m){
 const key=m.name+'@'+m.version;if(mountedPanels.get(m.name)?.key===key)return;
 mountedPanels.get(m.name)?.section.remove();dropListeners(m.name);
 const section=document.createElement('section'),title=document.createElement('h2'),body=document.createElement('div');
 section.className='panel';section.dataset.module=m.name;title.textContent=m.title;section.append(title,body);$('module-panels').append(section);
 mountedPanels.set(m.name,{key,section});
 try{const panel=await import('/m/'+encodeURIComponent(m.name)+'/panel.js?v='+encodeURIComponent(m.version));panel.default(body,moduleContext(m.name));}
 catch(e){body.textContent='Панель модуля не загрузилась: '+e.message;}
}
async function loadModules(){
 const list=await api('/api/v1/modules');if(!Array.isArray(list))throw Error('Некорректный список модулей');
 const rows=list.map(m=>{
  const row=document.createElement('div'),text=document.createElement('span'),remove=document.createElement('button');
  row.className='module-row';row.dataset.module=m.name;
  text.textContent=m.error?m.name+' · '+m.error:m.title+' · '+m.version+(m.boot_hook?' · в загрузке робота':'')+(m.service_running===true?' · работает':m.service_running===false?' · фоновый процесс остановлен':'');
  remove.textContent='Удалить';remove.onclick=()=>removeModule(m);row.append(text,remove);return row;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Модули не установлены'}));
 $('module-list').replaceChildren(...rows);
 for(const [name,mounted] of mountedPanels)if(!list.some(m=>m.name===name&&m.panel&&!m.error)){mounted.section.remove();mountedPanels.delete(name);dropListeners(name);}
 for(const m of list)if(m.panel&&!m.error)await mountPanel(m);
}
async function removeModule(m){
 if(busy||!window.confirm('Удалить модуль «'+(m.title||m.name)+'»?'+(m.boot_hook?' Его команды в загрузке робота перестанут действовать после перезагрузки.':'')))return;
 busy=true;disableControls(true);
 try{const r=await api('/api/v1/modules/remove',{name:m.name});toast(r.warning||'Модуль удалён',!!r.warning);}catch(e){toast(e.message,true);}finally{busy=false;disableControls(false);}
 await loadModules().catch(e=>toast(e.message,true));
}
$('stage-module').onclick=async()=>{
 const file=$('module-file').files[0];if(!file){toast('Выбери файл пакета',true);return;}if(busy)return;
 busy=true;disableControls(true);$('module-review').hidden=true;$('module-result').textContent='Проверяю пакет…';
 try{
  // Raw upload: the agent unpacks and validates it, nothing runs until «Установить».
  const response=await fetch('/api/v1/modules/stage',{method:'POST',headers:{'Authorization':'Bearer '+token,'Content-Type':'application/gzip'},body:file,credentials:'omit',cache:'no-store'});
  const body=await response.json();if(!response.ok)throw Error(body.error||'Ошибка HTTP '+response.status);
  stagedModule=body.result;const m=stagedModule.manifest;
  $('module-manifest').textContent=[m.title+' ('+m.name+') '+m.version+(stagedModule.installed_version?' · заменит '+stagedModule.installed_version:''),m.description||'',
   'SHA-256: '+stagedModule.sha256,'Сообщения прошивки: '+((m.firmware_messages||[]).join(', ')||'нет'),
   stagedModule.boot_hook?'Добавляет команды в загрузку робота (boot-env.sh): начнут действовать после перезагрузки.':'Не меняет загрузку робота.',
   m.control?'Может управлять роботом: запускать уборку, отправлять на базу и другие команды.':'Не управляет роботом.',
   m.service?'Работает постоянно в фоне (процесс '+m.service+').':'',
   'Файлы: '+stagedModule.files.join(', ')].filter(Boolean).join('\n');
  $('module-review').hidden=false;$('module-result').textContent='Проверь сведения и нажми «Установить».';
 }catch(e){$('module-result').textContent=e.message;}finally{busy=false;disableControls(false);}
};
$('install-module').onclick=async()=>{
 if(!stagedModule||busy)return;busy=true;disableControls(true);$('module-result').textContent='Устанавливаю…';
 try{const r=await api('/api/v1/modules/install',{id:stagedModule.id});$('module-result').textContent='Установлен модуль '+r.installed+' '+r.version+'.';$('module-review').hidden=true;stagedModule=null;$('module-file').value='';}
 catch(e){$('module-result').textContent=e.message;}finally{busy=false;disableControls(false);}
 await loadModules().catch(e=>toast(e.message,true));
};
$('cancel-module').onclick=()=>{stagedModule=null;$('module-review').hidden=true;$('module-result').textContent='';};
$('wifi-open').onchange=()=>{$('wifi-password').disabled=$('wifi-open').checked;$('wifi-password').required=!$('wifi-open').checked;if($('wifi-open').checked)$('wifi-password').value='';};
$('wifi-form').onsubmit=async e=>{
 e.preventDefault();if(busy)return;
 if(captiveSetup&&!token){token=$('token').value.trim();if(token.length<32){$('wifi-result').textContent='Введи API-токен агента в поле выше.';return;}}
 busy=true;disableControls(true);
 const params={ssid:$('wifi-ssid').value,password:$('wifi-open').checked?'':$('wifi-password').value,open:$('wifi-open').checked};$('wifi-password').value='';
 try{const result=await api('local.wifi_setup',params);if(!result.accepted)throw Error('Настройка сети не принята');$('wifi-result').textContent='Настройки приняты. Робот подключается. Точка доступа отключится; открой новый IP робота из домашней сети. При ошибке подожди до 3 минут для возврата точки доступа.';}
 catch(e){$('wifi-result').textContent=e.message;toast(e.message,true);}finally{params.password='';busy=false;disableControls(false);}
};
async function refresh(){
 if(!token||refreshing||busy||document.hidden)return;refreshing=true;const epoch=viewEpoch;
 try{
  // The agent polls the robot; this reads its cached snapshot.
  const snap=await api('/api/v1/state');
  if(epoch!==viewEpoch||!token)return;
  renderState(snap);
  const wifi=await api('local.wifi_status');if(epoch!==viewEpoch||!token)return;renderNetwork(wifi);
 }catch(e){if(!e.suspended&&epoch===viewEpoch){connectionLost(e.message);}}finally{if(epoch===viewEpoch)refreshing=false;}
}
// The agent reports whether the robot's state showed the last accepted command's effect (outcome.go).
let commandSeen=null;
function renderCommand(c){
  const key=c?c.at+c.outcome:'';
  if(commandSeen!==null&&key!==commandSeen&&c?.outcome==='executed')toast('Робот выполнил команду');
  if(commandSeen!==null&&key!==commandSeen&&c?.outcome==='unknown')toast('Робот принял команду, но за 30 с не видно, что она выполнена',true);
  commandSeen=key;
}
function renderState(snap){
  if(!snap?.state||typeof snap.state!=='object')throw Error('Некорректный ответ статуса');
  if(snap.stale&&!snap.updated_at)throw Error(snap.error||'Агент ещё не прочитал состояние робота');
  const s=snap.state;
  $('status').textContent=statusNames[s.status]||(s.status>1510&&s.status<1600?'Самопроверка: проверка '+(s.status-1510):'Статус '+(s.status??'недоступен'));
  $('battery').textContent=s.battery===undefined?'—':s.battery+'%';$('battery').style.setProperty('--charge',(s.battery??0)+'%');
  $('updated').textContent='Обновлено '+new Date(snap.updated_at).toLocaleTimeString();
  if(!fanDirty&&document.activeElement!==$('fan')&&s.suction!==undefined)$('fan').value=String(s.suction);
  if(!volumeDirty&&document.activeElement!==$('volume')&&s.volume!==undefined){$('volume').value=s.volume;$('volume-value').textContent=s.volume+'%';}
  if(!roomOptionsDirty){if(Number.isInteger(s.suction))$('room-fan').value=s.suction;if(Number.isInteger(s.water)&&s.water>=1&&s.water<=3)$('room-water').value=s.water;}
  $('station').textContent=s.station===undefined?'Состояние станции недоступно':'Состояние станции: '+(stationNames[s.station]||s.station)+(s.tank_drain===1?' · сливается бак робота':'')+(s.water_drain===1?' · сливается вода станции':'');
  $('diagnostics').textContent='Регион: '+(s.region??'—')+' · код доступа: '+(s.region_code??'—')+' · ошибка робота: '+(s.error??'—')+(s.error_name?' ('+s.error_name+')':'');
  $('robot-error').hidden=!s.error;$('robot-error').textContent=s.error?'Ошибка '+s.error+': '+errorText(s):'';
  renderCare(s);
  const look=[s.activity,s.station,s.charging,s.battery===100].join();if(look!==robotLook){robotLook=look;robotState=s;if(view&&!view.empty)draw();}robotState=s;
  // The agent records a finished run after its next totals read; reload the history then.
  const active=['cleaning','paused','returning'].includes(s.activity);if(wasActive&&!active)setTimeout(()=>{loadHistory().catch(()=>{});loadMessages().catch(()=>{});},12000);wasActive=active;followLive(active);
  for(const listener of stateListeners)try{listener.fn(s);}catch{}
  renderCommand(snap.command);
  if(snap.stale){connectionLost(snap.error||'Нет свежих данных от робота');return;}
  $('connection').textContent='Подключён';$('connection').classList.remove('connection-offline');$('reconnect').hidden=true;
}
// ---- Live position: while the robot is out, read the newest frame the firmware wrote for the cloud.
let live=null,liveTimer=null,robotState=null,robotLook='';
async function refreshLive(){
 try{live=await api('local.live_pose');}catch{live=null;}
 $('live-info').textContent=live?.live?'Положение робота обновлено '+new Date(live.updated_at).toLocaleTimeString():'Сохранённая карта · положение робота и путь видны во время уборки';
 draw();
}
function followLive(active){
 if(active&&!liveTimer){refreshLive();liveTimer=setInterval(()=>{if(!document.hidden)refreshLive();},3000);}
 else if(!active&&liveTimer){clearInterval(liveTimer);liveTimer=null;refreshLive();}
}
// Mopped floor, tinted deeper for each pass of the 300 mm mop pads; same rules as mopPasses in maprender.go.
function drawMopped(path,px){
 const w=canvas.width,h=canvas.height,r=Math.round(150*3/view.grid_size),count=new Uint8Array(w*h),last=new Float64Array(w*h);let travelled=0;
 for(let i=1;i<path.length;i++){
  travelled+=Math.hypot(path[i][0]-path[i-1][0],path[i][1]-path[i-1][1]);if(path[i][2]!==1)continue;
  const [x0,y0]=px(path[i-1][0],path[i-1][1]),[x1,y1]=px(path[i][0],path[i][1]),n=Math.max(Math.abs(x1-x0),Math.abs(y1-y0),1);
  for(let s=0;s<=n;s++){const cx=Math.round(x0+(x1-x0)*s/n),cy=Math.round(y0+(y1-y0)*s/n);
   for(let y=Math.max(cy-r,0);y<=Math.min(cy+r,h-1);y++)for(let x=Math.max(cx-r,0);x<=Math.min(cx+r,w-1);x++){
    if((x-cx)**2+(y-cy)**2>r*r)continue;const k=y*w+x;if(!count[k]||travelled-last[k]>600)count[k]=Math.min(count[k]+1,3);last[k]=travelled;}}
 }
 const img=ctx.getImageData(0,0,w,h),d=img.data,alpha=[0,70,120,165];
 for(let k=0;k<count.length;k++){if(!count[k]||!d[k*4+3])continue;const a=alpha[count[k]]/255;d[k*4]+= (77-d[k*4])*a;d[k*4+1]+=(163-d[k*4+1])*a;d[k*4+2]+=(255-d[k*4+2])*a;}
 ctx.putImageData(img,0,0);
}
// AI camera finds (type codes as in Tasshack's ObstacleType; 160 is not decoded yet).
// Names as in the Dreame app's r2505 plugin (ObstacleUtil); 131, 134, 135, 225, 226, 229 and 160 it does not name.
const obstacleTypes={128:['⬛','база'],129:['⚖️','весы'],130:['🔌','розетка'],131:['🔌','провод'],132:['🧸','игрушка'],133:['👟','обувь'],134:['🧦','носок'],135:['💩','экскременты'],136:['🗑️','мусорное ведро'],137:['👕','ткань'],138:['➰','кабель'],139:['💧','жидкое пятно'],142:['📦','препятствие'],158:['🐾','питомец'],160:['❔','неизвестное препятствие'],163:['🧹','средство для уборки'],169:['💧','пятно'],201:['🪑','мебель, где можно застрять'],202:['🟤','неочищаемое пятно'],205:['🍪','крупные частицы'],206:['🟤','засохшее пятно'],209:['🥣','вещи питомца'],210:['🥣','вещи питомца'],214:['💧','жидкое пятно'],215:['🟤','неочищаемое пятно'],216:['🟤','засохшее пятно'],217:['🍪','крупные частицы'],225:['💇','волосы'],226:['📄','бумага'],229:['📦','мелочи']};
const obstacleType=t=>obstacleTypes[t]||['❔','предмет '+t];
function renderObstacles(){
 const list=view?.obstacles||[],counts={};list.forEach(o=>{const n=obstacleType(o[2])[1];counts[n]=(counts[n]||0)+1;});
 $('obstacles').hidden=!list.length;
 $('obstacles').textContent=list.length?'Препятствия'+(view.obstacles_at?' от '+new Date(view.obstacles_at).toLocaleString():'')+': '+Object.entries(counts).map(([n,c])=>c>1?n+' ×'+c:n).join(', '):'';
}
function drawLive(){
 const px=(x,y)=>{const p=robotToPixel(view,x,y);return [p[0]*3,(view.height-p[1])*3];};
 const dot=(p,color,r)=>{const [x,y]=px(...p);ctx.fillStyle=color;ctx.strokeStyle='#17232d';ctx.lineWidth=2;ctx.beginPath();ctx.arc(x,y,r,0,Math.PI*2);ctx.fill();ctx.stroke();};
 ctx.save();
 const current=live?.live&&live.map_id===view.map_id?live:null;
 // Floor and walls the robot found this run that the saved map lacks.
 const g=current?.grid_size||0;(current?.new_cells||[]).forEach(([cx,cy,kind])=>{const [x0,y0]=px(cx,cy+g),[x1,y1]=px(cx+g,cy);ctx.fillStyle=kind===2?'#8fa3b3':'rgba(143,200,255,.45)';ctx.fillRect(x0,y0,x1-x0,y1-y0);});
 if(current?.path?.length>1){drawMopped(current.path,px);ctx.strokeStyle='rgba(255,255,255,.7)';ctx.lineWidth=1.5;ctx.beginPath();current.path.forEach((p,i)=>{const [x,y]=px(...p);i?ctx.lineTo(x,y):ctx.moveTo(x,y);});ctx.stroke();}
 // Zone being cleaned (corners from the robot) and spot points; the robot picks the spot area itself.
 ctx.strokeStyle='#ffae67';ctx.lineWidth=2;ctx.setLineDash([6,4]);
 (current?.zones||[]).forEach(([ax,ay,bx,by])=>{const [x0,y0]=px(ax,ay),[x1,y1]=px(bx,by);ctx.strokeRect(x0,y0,x1-x0,y1-y0);});
 ctx.setLineDash([]);(current?.spots||[]).forEach(p=>dot(p,'#ffae67',5));
 ctx.font='15px system-ui,sans-serif';ctx.textAlign='center';ctx.textBaseline='middle';
 (view.obstacles||[]).forEach(([ox,oy,type])=>{const [x,y]=px(ox,oy);ctx.fillStyle='rgba(23,35,45,.85)';ctx.beginPath();ctx.arc(x,y,10,0,Math.PI*2);ctx.fill();ctx.fillText(obstacleType(type)[0],x,y+1);});
 // Sizes as in the app: the robot is 320 mm wide, the station 1.2 times the robot.
 const r=Math.max(6,160/(view.grid_size||50)*3);
 const charger=current?.charger||view.charger;if(charger)drawCharger(...px(...charger),r*1.2);
 // Off a run the robot sits on the station and covers it, leaving its green rim, as in the app.
 const docked=!current?.robot&&charger&&robotState?.activity==='docked',pos=current?.robot?px(...current.robot):docked?px(...charger):null;
 if(pos)drawRobot(pos[0],pos[1],current?.robot?current.angle:null,r);
 ctx.restore();
 placeMarker(pos,r);
}
// The app's dock_log: light green disc, green rim and bolt.
function drawCharger(x,y,r){
 ctx.fillStyle='#9ef0a8';ctx.strokeStyle='#5edb72';ctx.lineWidth=r*.24;ctx.beginPath();ctx.arc(x,y,r-ctx.lineWidth/2,0,Math.PI*2);ctx.fill();ctx.stroke();
 const k=r/10;ctx.fillStyle='#5edb72';ctx.beginPath();ctx.moveTo(x+1.5*k,y-6*k);ctx.lineTo(x-3.5*k,y+.5*k);ctx.lineTo(x-.2*k,y+.5*k);ctx.lineTo(x-1.5*k,y+6*k);ctx.lineTo(x+3.5*k,y-.5*k);ctx.lineTo(x+.2*k,y-.5*k);ctx.closePath();ctx.fill();
}
// Round robot with the lidar turret; the notch points where it drives (robot angle in degrees, y up).
function drawRobot(x,y,angle,r){
 ctx.fillStyle='#f4f7f9';ctx.strokeStyle='#6b7780';ctx.lineWidth=Math.max(1.5,r*.14);ctx.beginPath();ctx.arc(x,y,r,0,Math.PI*2);ctx.fill();ctx.stroke();
 ctx.fillStyle='#4da3ff';ctx.beginPath();ctx.arc(x,y,r*.4,0,Math.PI*2);ctx.fill();
 if(Number.isFinite(angle)){const a=-angle*Math.PI/180;ctx.strokeStyle='#17232d';ctx.lineWidth=2;ctx.beginPath();ctx.moveTo(x+Math.cos(a)*r*.55,y+Math.sin(a)*r*.55);ctx.lineTo(x+Math.cos(a)*r*.92,y+Math.sin(a)*r*.92);ctx.stroke();}
}
// One small DOM element over the robot animated by CSS: no canvas repaint per frame.
function placeMarker(pos,r){
 const marker=$('map-marker'),s=robotState||{},mode=!pos?'':s.activity==='cleaning'?'pulse':s.activity==='returning'?'pulse':s.station===2?'heat':s.station===1?'wash':s.charging===1&&s.battery<100?'charge':s.activity==='docked'?'sleep':'';
 marker.hidden=!mode;if(!mode)return;
 marker.className='map-marker '+mode;marker.style.left=(pos[0]/canvas.width*100)+'%';marker.style.top=(pos[1]/canvas.height*100)+'%';
 marker.style.setProperty('--r',(r*canvas.clientWidth/canvas.width).toFixed(1)+'px'); // robot radius in page pixels
}
// ---- Compatibility report: capabilities without the robot's id, saved as a file to share.
$('compat-report').onclick=async()=>{
 try{const caps=await api('/api/v1/capabilities');delete caps.identity?.did;delete caps.modules;
  const link=document.createElement('a');link.href=URL.createObjectURL(new Blob([JSON.stringify(caps,null,2)],{type:'application/json'}));
  link.download='dreame-compat-'+(caps.identity?.model||'robot')+'-'+(caps.identity?.fw_ver||'')+'.json';link.click();setTimeout(()=>URL.revokeObjectURL(link.href),1000);}
 catch(e){toast(e.message,true);}
};
// ---- Cleaning history kept by the agent, newest first.
let wasActive=false;
const runModes={cleaning:'Уборка',room_cleaning:'Уборка комнат',zone_cleaning:'Уборка зоны',spot_cleaning:'Точечная уборка',part_cleaning:'Локальная уборка',edge_cleaning:'Вдоль стен',mapping:'Построение карты'};
async function loadHistory(){
 const runs=await api('/api/v1/history'),rows=runs.slice(0,30).map(r=>{
  const row=document.createElement('div'),start=new Date(r.start),minutes=r.minutes??(r.end?Math.round((new Date(r.end)-start)/60000):null);
  row.className='module-row history-run';
  row.textContent=start.toLocaleString([], {dateStyle:'short',timeStyle:'short'})+' · '+(runModes[r.mode]||r.mode||'Уборка')+(minutes!==null?' · '+Math.round(minutes)+' мин':'')+(r.area!==undefined?' · '+r.area+' м²':'')+' · '+(r.result==='error'?'ошибка: '+errorText({error_name:r.error}):r.result==='docked'?'на базе':'остановлена');
  if(r.obstacles?.length){
   const details=document.createElement('details'),photos=document.createElement('div');
   details.append(Object.assign(document.createElement('summary'),{textContent:'Препятствия: '+r.obstacles.length}),photos);
   photos.className='obstacle-photos';
   r.obstacles.forEach((o,i)=>{const [icon,name]=obstacleType(o[2]),figure=document.createElement('figure');figure.append(Object.assign(document.createElement('figcaption'),{textContent:icon+' '+name+' · '+o[3]+'%'}));if(r.photos?.[i])figure.dataset.photo=r.photos[i];photos.append(figure);});
   // Photos load on first open: they need the token, so they come through fetch, not <img src>.
   details.ontoggle=()=>{if(!details.open||details.dataset.loaded)return;details.dataset.loaded=1;photos.querySelectorAll('figure[data-photo]').forEach(async figure=>{try{const response=await fetch('/api/v1/history/photo?name='+encodeURIComponent(figure.dataset.photo),{headers:{'Authorization':'Bearer '+token},credentials:'omit'});if(!response.ok)throw Error();const img=document.createElement('img');img.src=URL.createObjectURL(await response.blob());img.alt=figure.textContent;figure.prepend(img);}catch{figure.append(' (фото не загрузилось)');}});};
   row.append(details);
  }
  return row;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Уборок пока не было.'}));
 $('history').replaceChildren(...rows);
}
$('refresh-history').onclick=()=>loadHistory().catch(e=>toast(e.message,true));
$('refresh-logs').onclick=()=>loadLogs().catch(e=>toast(e.message,true));$('log-source').onchange=showLog;
// ---- Maintenance: consumables, totals and do-not-disturb come with the state once a minute.
const consumableNames={main_brush:'Основная щётка',side_brush:'Боковая щётка',filter:'Фильтр',sensor:'Датчики',tank_filter:'Фильтр бака',mop_pad:'Салфетки',silver_ion:'Ионы серебра',detergent:'Моющее средство',squeegee:'Скребок',dirty_water_channel:'Канал грязной воды',onboard_dirty_water_tank:'Бак грязной воды робота',deodorizer:'Дезодоратор',wheel:'Колёса',scale_inhibitor:'Антинакипин',fluffing_roller:'Взбивающий валик',roller_mop_filter:'Фильтр валика',water_outlet_filter:'Фильтр слива',track:'Направляющие',washboard:'Поддон мойки',filter_cleaning:'Очистка фильтра',roller_cover:'Кожух валика',main_brush_cover:'Кожух основной щётки'};
// Error text: the app plugin's own (error_text from the agent); these cover codes the plugin does not name.
const errorTexts={9:'нет бака',10:'бак для воды пуст',19:'не найдена база',68:'снимите салфетки',109:'бак грязной воды засорён',122:'предупреждение без описания',123:'самопроверка не пройдена'};
const errorText=s=>s.error_text||errorTexts[s.error]||String(s.error_name||'неизвестная ошибка').replaceAll('_',' ');
function renderCare(s){
 const parts=s.consumables||{},key=JSON.stringify([parts,s.stats,s.dnd,s.dnd_periods,s.schedules,s.quick_commands]);
 if(key===careShown)return;careShown=key;
 $('care-panel').hidden=!Object.keys(parts).length&&!s.dnd;
 const rows=Object.entries(parts).map(([name,c])=>{
  const row=document.createElement('div'),text=document.createElement('span'),reset=document.createElement('button');
  row.className='module-row';row.dataset.consumable=name;
  text.textContent=(consumableNames[name]||name)+' · '+(c.left??'—')+'%'+(c.hours!==undefined?' · ещё '+c.hours+' ч':'');
  if(c.left!==undefined&&c.left<=10)text.classList.add('error');
  reset.textContent='Сбросить';reset.className='ghost';
  reset.onclick=()=>{if(window.confirm('Сбросить ресурс «'+(consumableNames[name]||name)+'»? Делай это после замены или чистки.'))control('/api/v1/command',{name:'reset_consumable',consumable:name}).catch(()=>{});};
  row.append(text,reset);return row;
 });
 $('consumables').replaceChildren(...rows);
 const st=s.stats;$('stats').textContent=st?'Всего уборок: '+(st.cleanings??'—')+' · '+(st.minutes!==undefined?Math.round(st.minutes/60)+' ч':'—')+' · '+(st.area??'—')+' м²':'';
 $('dnd').hidden=!s.dnd;
 if(s.dnd&&!dndDirty){$('dnd-enabled').checked=!!s.dnd.enabled;$('dnd-start').value=s.dnd.start||'';$('dnd-end').value=s.dnd.end||'';}
 renderQuick(s.quick_commands);renderSchedules(s.schedules);renderDNDPeriods(s.dnd_periods);
}
// ---- Schedules (8/2): days are seven 0/1 digits from Sunday.
const dayNames=['Вс','Пн','Вт','Ср','Чт','Пт','Сб'];
export function scheduleDays(days){const on=dayNames.filter((_,i)=>days[i]==='1');return on.length===7?'каждый день':on.length?on.join(', '):'один раз';}
function renderSchedules(list){
 $('schedules-block').hidden=!Array.isArray(list);if(!Array.isArray(list))return;
 const rows=list.map(t=>{
  const row=document.createElement('div'),text=document.createElement('span'),toggle=document.createElement('button'),del=document.createElement('button');
  row.className='module-row';
  const quick=t.quick_command&&(quickList.find(q=>q.id===t.quick_command)?.name||'№'+t.quick_command);
  text.textContent=t.time+' · '+scheduleDays(t.days||'')+' · '+(quick?'команда «'+quick+'»':t.rooms?'комнаты '+t.rooms.join(', '):'весь дом')+(t.enabled?'':' · выключено');
  toggle.textContent=t.enabled?'Выключить':'Включить';toggle.className='ghost';
  toggle.onclick=()=>command('set_schedule',{id:t.id,enabled:!t.enabled,time:t.time,days:t.days}).catch(()=>{});
  del.textContent='Удалить';del.className='ghost';
  del.onclick=()=>{if(window.confirm('Удалить расписание на '+t.time+'?'))command('delete_schedule',{id:t.id}).catch(()=>{});};
  row.append(text,toggle,del);return row;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Расписаний нет.'}));
 $('schedules').replaceChildren(...rows);
}
$('add-schedule').onclick=()=>{try{
 const days=dayNames.map((_,i)=>$('schedule-days').querySelector('input[value="'+i+'"]').checked?'1':'0').join('');
 let args={enabled:true,time:$('schedule-time').value,days};
 if($('schedule-quick').value)args.quick_command=Number($('schedule-quick').value);
 else if($('schedule-rooms').checked){const r=roomCommand([...selected],Number($('room-fan').value),Number($('room-water').value),Number($('room-repeats').value));args={...args,rooms:r.rooms,repeats:r.repeats,suction:r.suction,water:r.water};}
 command('set_schedule',args).catch(()=>{});
}catch(e){toast(e.message,true);}};
// ---- Quick commands (4/48): saved room sets the robot can run from a schedule.
let quickList=[];
function renderQuick(list){
 $('quick-block').hidden=!Array.isArray(list);quickList=Array.isArray(list)?list:[];
 $('schedule-quick-label').hidden=!quickList.length;
 const keep=$('schedule-quick').value;
 $('schedule-quick').replaceChildren(new Option('— нет —',''),...quickList.map(q=>new Option(q.name||'№'+q.id,String(q.id))));
 $('schedule-quick').value=quickList.some(q=>String(q.id)===keep)?keep:'';
 if(!Array.isArray(list))return;
 const rows=quickList.map(q=>{
  const row=document.createElement('div'),text=document.createElement('span'),del=document.createElement('button');
  row.className='module-row';text.textContent=q.name||'№'+q.id;
  del.textContent='Удалить';del.className='ghost';
  del.onclick=()=>{if(window.confirm('Удалить быструю команду «'+(q.name||q.id)+'»?'))command('delete_quick_command',{id:q.id}).catch(()=>{});};
  row.append(text,del);return row;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Быстрых команд нет.'}));
 $('quick-commands').replaceChildren(...rows);
}
$('add-quick').onclick=()=>{
 const rooms=[...selected];
 if(!rooms.length)return toast('Выбери комнаты на карте',true);
 if(!$('quick-name').value.trim())return toast('Назови команду',true);
 command('save_quick_command',{label:$('quick-name').value.trim(),rooms}).then(()=>{$('quick-name').value='';},()=>{});
};
// ---- Do-not-disturb periods (5/4), same day digits as schedules.
function renderDNDPeriods(list){
 $('dnd-periods-block').hidden=!Array.isArray(list);if(!Array.isArray(list))return;
 const rows=list.map(d=>{
  const row=document.createElement('div'),text=document.createElement('span'),toggle=document.createElement('button'),del=document.createElement('button');
  row.className='module-row';
  text.textContent=d.start+'–'+d.end+' · '+scheduleDays(d.days||'')+(d.enabled?'':' · выключено');
  toggle.textContent=d.enabled?'Выключить':'Включить';toggle.className='ghost';
  toggle.onclick=()=>command('set_dnd_period',{id:d.id,enabled:!d.enabled,start:d.start,end:d.end,days:d.days}).catch(()=>{});
  del.textContent='Удалить';del.className='ghost';
  del.onclick=()=>{if(window.confirm('Удалить период '+d.start+'–'+d.end+'?'))command('delete_dnd_period',{id:d.id}).catch(()=>{});};
  row.append(text,toggle,del);return row;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Периодов нет.'}));
 $('dnd-periods').replaceChildren(...rows);
}
$('add-dnd-period').onclick=()=>{
 const days=dayNames.map((_,i)=>$('dnd-period-days').querySelector('input[value="'+i+'"]').checked?'1':'0').join('');
 command('set_dnd_period',{enabled:true,start:$('dnd-period-start').value,end:$('dnd-period-end').value,days}).catch(()=>{});
};
for(const id of ['dnd-enabled','dnd-start','dnd-end'])$(id).oninput=()=>{dndDirty=true;};
$('save-dnd').onclick=()=>control('/api/v1/command',{name:'set_dnd',enabled:$('dnd-enabled').checked,start:$('dnd-start').value,end:$('dnd-end').value}).then(()=>{dndDirty=false;},()=>{});
// Live state: the agent pushes every change on /api/v1/events (Server-Sent Events read with fetch, so the token stays in a header).
let events=null;
function stopEvents(){events?.abort('suspend');events=null;}
async function streamEvents(){
 stopEvents();const controller=new AbortController();events=controller;
 while(events===controller&&token&&!document.hidden){
  try{
   const response=await fetch('/api/v1/events',{headers:{'Authorization':'Bearer '+token},credentials:'omit',cache:'no-store',signal:controller.signal});
   if(!response.ok||!response.body)throw Error('Ошибка HTTP '+response.status);
   const reader=response.body.pipeThrough(new TextDecoderStream()).getReader();let buffer='';
   for(;;){
    const {value,done}=await reader.read();if(done)break;buffer+=value;
    let end;while((end=buffer.indexOf('\n\n'))>=0){
     const block=buffer.slice(0,end);buffer=buffer.slice(end+2);
     const data=block.split('\n').filter(l=>l.startsWith('data: ')).map(l=>l.slice(6)).join('\n');
     if(data&&!busy)try{renderState(JSON.parse(data));}catch(e){connectionLost(e.message);}
    }
   }
  }catch(e){if(controller.signal.aborted)return;}
  if(events!==controller)return;
  connectionLost('Поток событий прервался');await new Promise(resolve=>setTimeout(resolve,3000));
 }
}
function invalidateEdit(){pending=null;$('map-preview').textContent='';$('apply-map-edit').disabled=true;}
function selectRoom(id){
 if($('map-operation').value==='split')selected=new Set(selected.has(id)?[]:[id]);
 else if(selected.has(id))selected.delete(id);else selected.add(id);
 points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);
}
function renderRoomButtons(){
 $('rooms').replaceChildren();$('room-selection').textContent=selected.size?'Выбрано: '+(view?.rooms||[]).filter(r=>selected.has(r.id)).map(r=>r.name).join(', '):'Выбери комнаты на карте или кнопками ниже.';
 for(const room of view?.rooms||[]){const b=document.createElement('button');b.textContent=room.name+' · '+room.id;b.style.setProperty('--room',palette[(room.id-1)%palette.length]);b.classList.toggle('selected',selected.has(room.id));b.setAttribute('aria-pressed',String(selected.has(room.id)));b.onclick=()=>selectRoom(room.id);$('rooms').append(b);}
}
// Rooms with their floor pattern at canvas size, rebuilt only when the map, the selection or a floor draft changes.
// Pattern sizes in map cells as the app's buildFloorMap: tiles 12, planks 4 × 16 laid in a half-board offset.
const roomIcons={1:'🛋',2:'🛏',15:'🛏',3:'📚',4:'🍲',5:'🍽',6:'🛁',7:'🌿',8:'👣',9:'📦',10:'👔',11:'🚪',12:'💼',13:'🏋',14:'☕'};
let baseCache={key:'',canvas:null};
function floorOf(room){const draft=$('room-floors').querySelector(`[data-room="${room.id}"]`)?.value;return draft===undefined?room.floor||0:Number(draft);}
function baseLayer(){
 const floors=view.rooms.map(r=>r.id+':'+floorOf(r)).join(','),key=view.revision+'|'+[...selected].join(',')+'|'+floors;
 if(baseCache.key===key)return baseCache.canvas;
 const s=3,w=view.width,h=view.height,W=w*s,H=h*s,grid=view.grid,out=document.createElement('canvas');out.width=W;out.height=H;
 const bc=out.getContext('2d'),img=bc.createImageData(W,H),d=img.data,fill=[],line=[],kind=[],box={};
 for(let i=0;i<grid.length;i++){const id=grid[i];if(!id||id===255)continue;const x=i%w,y=i/w|0,b=box[id]||(box[id]=[x,x,y,y]);if(x<b[0])b[0]=x;if(x>b[1])b[1]=x;if(y<b[2])b[2]=y;if(y>b[3])b[3]=y;}
 for(let id=1;id<255;id++){let rgb=palette[(id-1)%palette.length].slice(1).match(/../g).map(c=>parseInt(c,16));if(selected.size&&!selected.has(id))rgb=rgb.map(c=>Math.round(c*.42));fill[id]=rgb;line[id]=rgb.map(c=>Math.round(c*.84));}
 fill[255]=line[255]=[0xd1,0xd9,0xe2];
 for(const room of view.rooms){let f=floorOf(room);if(f===1){const b=box[room.id];f=b&&b[1]-b[0]>b[3]-b[2]?4:3;}kind[room.id]=f;}
 for(let Y=0;Y<H;Y++){const row=(h-1-(Y/s|0))*w;for(let X=0;X<W;X++){
  const id=grid[row+(X/s|0)];if(!id)continue;const f=kind[id];
  const edge=f===2?X%36===0||Y%36===0:f===4?Y%12===0||(X+(Y/12&1)*24)%48===0:f===3?X%12===0||(Y+(X/12&1)*24)%48===0:false;
  const c=edge?line[id]:fill[id],j=(Y*W+X)*4;d[j]=c[0];d[j+1]=c[1];d[j+2]=c[2];d[j+3]=255;
 }}
 // Outer border of the rooms, like the app's floor plan (#7F8B98): cell sides facing outside or a wall; 2 px inside the cell.
 const out_=(x,y)=>x<0||y<0||x>=w||y>=h||!grid[y*w+x]||grid[y*w+x]===255,paint=(X,Y)=>{const j=(Y*W+X)*4;d[j]=0x7f;d[j+1]=0x8b;d[j+2]=0x98;d[j+3]=255;};
 for(let y=0;y<h;y++)for(let x=0;x<w;x++){const id=grid[y*w+x];if(!id||id===255)continue;const X0=x*s,Y0=(h-1-y)*s;
  for(let k=0;k<s;k++){
   if(out_(x-1,y))for(let t=0;t<2;t++)paint(X0+t,Y0+k);
   if(out_(x+1,y))for(let t=0;t<2;t++)paint(X0+s-1-t,Y0+k);
   if(out_(x,y+1))for(let t=0;t<2;t++)paint(X0+k,Y0+t);
   if(out_(x,y-1))for(let t=0;t<2;t++)paint(X0+k,Y0+s-1-t);
  }}
 bc.putImageData(img,0,0);baseCache={key,canvas:out};return out;
}
function draw(){
 canvas.style.touchAction=zoneDrawing&&!zoneSpot?'none':''; // a finger drags the zone corner instead of scrolling
 ctx.clearRect(0,0,canvas.width,canvas.height);if(!view||view.empty)return;
 const scale=3;ctx.drawImage(baseLayer(),0,0);
 // Room labels like the app: number badge, type icon and name.
 ctx.textAlign='center';ctx.textBaseline='middle';ctx.lineJoin='round';
 for(const room of view.rooms){
  const x=(room.x+.5)*scale,y=(view.height-room.y-.5)*scale,color=palette[(room.id-1)%palette.length],dim=selected.size&&!selected.has(room.id);
  ctx.globalAlpha=dim?.55:1;ctx.fillStyle='#17232d';ctx.beginPath();ctx.arc(x,y-9,8,0,Math.PI*2);ctx.fill();ctx.strokeStyle=color;ctx.lineWidth=2;ctx.stroke();
  ctx.font='bold 10px system-ui';ctx.fillStyle='#fff';ctx.fillText(String(room.id),x,y-8.5);
  ctx.font='600 11px system-ui';const label=(roomIcons[room.type]?roomIcons[room.type]+' ':'')+room.name;ctx.lineWidth=3;ctx.strokeStyle='rgba(23,35,45,.8)';ctx.strokeText(label,x,y+8);ctx.fillStyle='#fff';ctx.fillText(label,x,y+8);
 }
 ctx.globalAlpha=1;
 drawThresholds();drawRestrictions();drawFurniture();drawPatrol();drawLive();
 if(zoneSpot&&zone.length){const [a]=zone;ctx.strokeStyle='#ffae67';ctx.fillStyle='rgba(255,174,103,.25)';ctx.lineWidth=2;const h=750/view.grid_size*scale;ctx.fillRect(a.x*scale-h,(view.height-a.y)*scale-h,2*h,2*h);ctx.strokeRect(a.x*scale-h,(view.height-a.y)*scale-h,2*h,2*h);}
 else if(zone.length){const [a,b=zoneHover||a]=zone;ctx.strokeStyle='#ffae67';ctx.fillStyle='rgba(255,174,103,.25)';ctx.lineWidth=2;const x=Math.min(a.x,b.x)*scale,y=(view.height-Math.max(a.y,b.y))*scale,w=Math.abs(a.x-b.x)*scale,h=Math.abs(a.y-b.y)*scale;ctx.fillRect(x,y,w,h);ctx.strokeRect(x,y,w,h);}
 if(points.length){ctx.strokeStyle='#ffae67';ctx.fillStyle='#ffae67';ctx.lineWidth=2;for(const p of points){ctx.beginPath();ctx.arc(p.x*scale,(view.height-p.y)*scale,4,0,Math.PI*2);ctx.fill();}if(points.length===2){ctx.beginPath();ctx.moveTo(points[0].x*scale,(view.height-points[0].y)*scale);ctx.lineTo(points[1].x*scale,(view.height-points[1].y)*scale);ctx.stroke();}}
}
async function refreshMap(){
 if(!token||busy)return;const epoch=viewEpoch;
 try{
  const next=await api('local.map_view');if(epoch!==viewEpoch||!token)return;
  if(next.empty){view=next;selected.clear();points=[];thresholdDraft=null;thresholdDrawing=false;zone=[];zoneDrawing=false;restrictDraft=null;restrictDrawing=false;invalidateEdit();$('map-info').textContent='Сохранённая карта ещё не создана';renderRoomButtons();renderThresholdChoices();renderRestrictions();renderCarpetTypes();renderRoomSettings();renderRoomTypes();renderObstacles();draw();disableControls(busy);return;}
  if(!Number.isInteger(next.width)||!Number.isInteger(next.height)||next.width*next.height>4194304||!Array.isArray(next.rooms))throw Error('Некорректный формат карты');
  const bytes=atob(next.pixels);if(bytes.length!==next.width*next.height)throw Error('Некорректный размер карты');
  next.grid=Uint8Array.from(bytes,c=>c.charCodeAt(0));
  if(view?.revision!==next.revision){selected=view?.map_id===next.map_id?new Set([...selected].filter(id=>next.rooms.some(r=>r.id===id))):new Set();points=[];invalidateEdit();thresholdDraft=null;thresholdDrawing=false;zone=[];zoneDrawing=false;restrictDraft=null;restrictDrawing=false;}
  view=next;canvas.width=view.width*3;canvas.height=view.height*3;
  $('map-info').textContent='Карта '+view.map_id+' · комнат: '+view.rooms.length+' · '+view.width+' × '+view.height;
  renderRoomButtons();renderThresholdChoices();renderRestrictions();renderCarpetTypes();renderRoomSettings();renderRoomTypes();renderObstacles();draw();disableControls(busy);
 }catch(e){if(!e.suspended&&epoch===viewEpoch){$('map-info').textContent=e.message;connectionLost(e.message);}}
}
function mapPoint(e){const rect=canvas.getBoundingClientRect();return {x:(e.clientX-rect.left)*view.width/rect.width,y:view.height-(e.clientY-rect.top)*view.height/rect.height};}
// Zone: the first press sets a corner, the opposite corner follows the pointer (a finger drags it)
// and the next press, or releasing a drag, fixes it.
canvas.onpointerdown=e=>{
 if(!zoneDrawing||zoneSpot||!view||view.empty||busy)return;
 const p=mapPoint(e);zoneDrag={second:zone.length===1,x:e.clientX,y:e.clientY,moved:false};
 if(!zone.length){zone=[p];zoneHover=p;$('zone-status').textContent='Веди к противоположному углу и нажми ещё раз.';}
 canvas.setPointerCapture?.(e.pointerId);draw();
};
canvas.onpointermove=e=>{
 if(!zoneDrawing||zoneSpot||zone.length!==1)return;
 if(zoneDrag&&Math.hypot(e.clientX-zoneDrag.x,e.clientY-zoneDrag.y)>8)zoneDrag.moved=true;
 zoneHover=mapPoint(e);draw();
};
canvas.onpointerup=e=>{
 const drag=zoneDrag;zoneDrag=null;
 if(!drag||!zoneDrawing||zoneSpot||zone.length!==1||!(drag.second||drag.moved))return;
 zone.push(mapPoint(e));zoneHover=null;zoneDrawing=false;$('zone-status').textContent='Зона выбрана. Повторы, мощность и влажность — как для комнат.';draw();disableControls(busy);
};
canvas.onpointercancel=()=>{zoneDrag=null;};
canvas.onclick=e=>{
 if(!view||view.empty||busy)return;
 const {x,y}=mapPoint(e);
 if(zoneDrawing&&!zoneSpot)return; // pointer handlers below
 if(restrictDrawing){addRestrictionPoint(x,y);return;}
 if(furniturePlacing){placeFurniture(x,y);return;}
 if(patrolDrawing){addPatrolPoint(x,y);return;}
 if(zoneDrawing){zone.push({x,y});if(zoneSpot){zoneDrawing=false;$('zone-status').textContent='Будет убран квадрат 1,5 × 1,5 м вокруг точки. Повторы, мощность и влажность — как для комнат.';}else if(zone.length===2){zoneDrawing=false;$('zone-status').textContent='Зона выбрана. Повторы, мощность и влажность — как для комнат.';}draw();disableControls(busy);return;}
 if(thresholdDrawing){addThresholdPoint(x,y);return;}
 if($('threshold-details').open&&selectThresholdNear(x,y))return;
 if($('map-operation').value==='split'&&selected.size===1){if(points.length===2)points=[];points.push({x,y,robot:pixelToRobot(view,x,y)});invalidateEdit();draw();return;}
 const px=Math.min(view.width-1,Math.max(0,Math.floor(x))),py=Math.min(view.height-1,Math.max(0,Math.floor(y))),id=view.grid[py*view.width+px];
 if(view.rooms.some(r=>r.id===id))selectRoom(id);
};
// Show only what this robot supports: commands the hardware table rules out and properties the robot never answers are hidden.
export function applyCapabilities(caps){
 const id=caps.identity||{};hardware=caps.hardware||null;
 $('model').textContent=(id.model||'Неизвестная модель')+' · '+(id.fw_ver||'?')+(caps.firmware?.verified?'':' · прошивка не проверена');
 for(const button of document.querySelectorAll('[data-command]')){const c=caps.commands?.[button.dataset.command];button.hidden=c?.available===false;if(c?.reason)button.title=c.reason;}
 for(const [prop,input,apply] of [['suction','fan','save-fan'],['volume','volume','save-volume']]){const hide=caps.properties?.[prop]?.readable===false;$(input).closest('label').hidden=hide;$(apply).hidden=hide;}
}
async function login(){
 const epoch=viewEpoch,loginToken=token,caps=await api('/api/v1/capabilities');if(epoch!==viewEpoch||loginToken!==token)return;if(captiveSetup)$('main-page').append($('wifi-panel'));applyCapabilities(caps);
 $('login').hidden=true;$('dashboard').hidden=false;$('logout').hidden=false;$('login-error').textContent='';
 sessionStorage.setItem('dreame-token',token);
 await refresh();await refreshMap();loadMessages().catch(()=>{});loadPatrolSchedules().catch(()=>{});await mainTokenOnly('modules-panel',loadModules);$('nav-modules').hidden=$('modules-panel').hidden;await mainTokenOnly('update-panel',loadUpdate);await mainTokenOnly('tokens-panel',loadTokens);await mainTokenOnly('logs-panel',loadLogs);await mainTokenOnly('voice-panel',()=>mountVoice($('voice'),{api,token:()=>token,toast}));await loadHistory().catch(e=>toast(e.message,true));clearInterval(timer);timer=setInterval(()=>refresh(),30000);streamEvents();
}
$('login-form').onsubmit=async e=>{e.preventDefault();token=$('token').value.trim();try{await login();if($('remember').checked)localStorage.setItem('dreame-token',token);else localStorage.removeItem('dreame-token');$('token').value='';}catch(e){$('login-error').textContent=e.message;}};
$('logout').onclick=()=>{stopEvents();viewEpoch++;for(const read of activeReads)read.abort('logout');refreshing=false;clearInterval(timer);timer=null;token='';sessionStorage.removeItem('dreame-token');localStorage.removeItem('dreame-token');$('dashboard').hidden=true;$('login').hidden=false;$('logout').hidden=true;};
for(const button of document.querySelectorAll('[data-command]'))button.onclick=async()=>{
 // data-command values are the agent's command names.
 try{if(button.dataset.confirm&&!window.confirm(button.dataset.confirm))return;if(button.dataset.command==='reboot')await rebootRobot();else await command(button.dataset.command);}catch{}
};
$('fan').onchange=()=>{fanDirty=true;};
$('save-fan').onclick=async()=>{try{await command('set_suction',{value:Number($('fan').value)});fanDirty=false;}catch{}};
$('volume').oninput=()=>{volumeDirty=true;$('volume-value').textContent=$('volume').value+'%';};
$('save-volume').onclick=async()=>{try{await command('set_volume',{value:Number($('volume').value)});volumeDirty=false;}catch{}};
$('refresh-map').onclick=()=>refreshMap();
$('clear-selection').onclick=()=>{selected.clear();points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);};
$('clean-rooms').onclick=async()=>{try{const ids=[...selected],mapID=view?.map_id;const args=roomCommand(ids,Number($('room-fan').value),Number($('room-water').value),Number($('room-repeats').value));await control('/api/v1/command',args,async()=>{const current=await api('local.map_view');if(current.map_id!==mapID||!ids.every(id=>current.rooms.some(r=>r.id===id)))throw Error('Комнаты изменились. Обнови карту и выбери их заново.');});}catch(e){toast(e.message,true);}};
for(const id of ['room-fan','room-water','room-repeats'])$(id).onchange=()=>{roomOptionsDirty=true;};
// ---- Zone cleaning: two opposite corners on the map; the agent checks the rectangle against the saved map.
$('draw-zone').onclick=()=>{if(!view||view.empty||busy)return;zoneDrawing=true;zoneSpot=false;zone=[];$('clean-zone').textContent='Убрать зону';$('zone-status').textContent='Нажми на угол зоны, затем на противоположный (на телефоне можно провести пальцем).';disableControls(busy);draw();};
$('draw-spot').onclick=()=>{if(!view||view.empty||busy)return;zoneDrawing=true;zoneSpot=true;zone=[];$('clean-zone').textContent='Убрать вокруг точки';$('zone-status').textContent='Нажми на точку на карте.';disableControls(busy);draw();};
$('cancel-zone').onclick=()=>{zoneDrawing=false;zone=[];zoneHover=null;$('zone-status').textContent='Выбор снят.';disableControls(busy);draw();};
$('clean-zone').onclick=async()=>{
 try{const [a,b]=zone.map(p=>pixelToRobot(view,p.x,p.y)),mapID=view.map_id;
  const target=zoneSpot?{name:'clean_spot',points:[a]}:{name:'clean_zone',zones:[[a[0],a[1],b[0],b[1]]]};
  await control('/api/v1/command',{...target,repeats:Number($('room-repeats').value),suction:Number($('room-fan').value),water:Number($('room-water').value)},async()=>{const current=await api('local.map_view');if(current.map_id!==mapID)throw Error('Карта изменилась. Выбери зону заново.');});}
 catch(e){toast(e.message,true);}
};
$('map-operation').onchange=()=>{thresholdDrawing=false;thresholdDraft=null;selected.clear();points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);};
$('prepare-map-edit').onclick=()=>{
 try{
  if(!view||view.empty)throw Error('Карта недоступна');const ids=[...selected];let data;
  if($('map-operation').value==='merge'){if(ids.length!==2)throw Error('Выбери две комнаты');data={msr:ids,mapid:view.map_id};}
  else{if(ids.length!==1||points.length!==2)throw Error('Выбери одну комнату и две точки линии');const line=points.flatMap(p=>p.robot);if(line[0]===line[2]&&line[1]===line[3])throw Error('Точки совпадают');data={dsrid:[...line,ids[0]],mapid:view.map_id};}
  pending={data,revision:view.revision,map_id:view.map_id};$('map-preview').textContent=JSON.stringify(data,null,2);$('apply-map-edit').disabled=false;
 }catch(e){toast(e.message,true);}
};
$('apply-map-edit').onclick=async()=>{
 if(!pending||busy)return;const change=pending;invalidateEdit();
 try{await control('action',{siid:6,aiid:2,in:[{piid:4,value:JSON.stringify(change.data)}]},async()=>{const current=await api('local.map_view');if(current.revision!==change.revision||current.map_id!==change.map_id)throw Error('Карта изменилась. Обнови её и выбери точки заново.');});setTimeout(()=>refreshMap(),1500);}catch(e){toast(e.message,true);}
};
$('send-rpc').onclick=async()=>{try{const method=$('rpc-method').value,params=JSON.parse($('rpc-params').value),result=method==='get_properties'?await api(method,params):await control(method,params);$('rpc-result').textContent=JSON.stringify(result,null,2);}catch(e){toast(e.message,true);}};
if(token)login().catch(e=>{if(e.status===401){token='';sessionStorage.removeItem('dreame-token');localStorage.removeItem('dreame-token');}else connectionLost(e.message);$('login-error').textContent=e.message;});

if(captiveSetup){$('login').querySelector('h2').textContent='Настройка Wi-Fi робота';$('login').querySelector('p.muted').textContent='Укажи API-токен агента, имя сети и пароль. Затем нажми «Подключить робот к сети».';$('login-form').querySelector('button').textContent='Открыть панель управления';$('wifi-details').open=true;$('login').append($('wifi-panel'));}

export function robotToPixel(v,x,y){
 if(!v||!Array.isArray(v.origin)||v.grid_size<=0||![x,y].every(Number.isFinite))throw Error('Некорректная точка карты');
 return [(x-v.origin[0])/v.grid_size,(y-v.origin[1])/v.grid_size];
}
function thresholdLines(){return ['passable','impassable'].flatMap(kind=>(view?.[kind+'_thresholds']||[]).map((line,index)=>({kind,index,line})));}
function renderThresholdChoices(){
 const previous=$('threshold-choice').value;$('threshold-choice').replaceChildren(new Option('Новый порог',''));
 for(const item of thresholdLines())$('threshold-choice').add(new Option((item.kind==='passable'?'Проходимый':'Непроходимый')+' '+(item.index+1),item.kind+':'+item.index));
 if([...$('threshold-choice').options].some(o=>o.value===previous))$('threshold-choice').value=previous;
 $('threshold-status').textContent=view?.thresholds_editable?'Выбери порог для изменения или нарисуй новый.':'Эта карта не содержит доступного формата редактирования порогов.';
}
function strokeThreshold(line,kind,dashed=false){
 if(!Array.isArray(line)||line.length!==4)return;
 const start=robotToPixel(view,line[0],line[1]),end=robotToPixel(view,line[2],line[3]);
 ctx.save();ctx.strokeStyle=kind==='passable'?'#55f2aa':'#ff6b80';ctx.lineWidth=dashed?3:2;ctx.setLineDash(dashed?[5,3]:[]);ctx.beginPath();ctx.moveTo(start[0]*3,(view.height-start[1])*3);ctx.lineTo(end[0]*3,(view.height-end[1])*3);ctx.stroke();ctx.restore();
}
function drawThresholds(){for(const item of thresholdLines())strokeThreshold(item.line,item.kind);if(thresholdDraft?.line)strokeThreshold(thresholdDraft.line,$('threshold-kind').value,true);}
function chooseThreshold(){
 thresholdDrawing=false;points=[];
 const [kind,rawIndex]=$('threshold-choice').value.split(':'),index=Number(rawIndex),line=view?.[kind+'_thresholds']?.[index];
 thresholdDraft=line?{line:[...line]}:null;if(line)$('threshold-kind').value=kind;
 $('threshold-status').textContent=line?'Порог выбран. Можно изменить проходимость, указать новые точки или удалить его.':'Укажи две точки нового порога на карте.';disableControls(busy);draw();
}
function selectThresholdNear(x,y){
 const tolerance=8*view.width/canvas.getBoundingClientRect().width;
 let closest=null,distance=Infinity;
 for(const item of thresholdLines()){
  const a=robotToPixel(view,item.line[0],item.line[1]),b=robotToPixel(view,item.line[2],item.line[3]),dx=b[0]-a[0],dy=b[1]-a[1],length=dx*dx+dy*dy;
  const t=length?Math.max(0,Math.min(1,((x-a[0])*dx+(y-a[1])*dy)/length)):0,d=Math.hypot(x-a[0]-t*dx,y-a[1]-t*dy);
  if(d<distance){distance=d;closest=item;}
 }
 if(closest&&distance<=tolerance){$('threshold-choice').value=closest.kind+':'+closest.index;chooseThreshold();return true;}return false;
}
function addThresholdPoint(x,y){
 if(points.length===2)points=[];points.push({x,y,robot:pixelToRobot(view,x,y)});
 thresholdDraft=points.length===2?{line:points.flatMap(p=>p.robot)}:null;
 if(thresholdDraft){thresholdDrawing=false;points=[];$('threshold-status').textContent='Линия подготовлена. Нажми «Сохранить порог».';}
 else $('threshold-status').textContent='Укажи вторую точку линии.';
 draw();disableControls(busy);
}
$('threshold-choice').onchange=chooseThreshold;
$('threshold-kind').onchange=()=>draw();
$('draw-threshold').onclick=()=>{if(busy||!view?.thresholds_editable)return;thresholdDrawing=true;thresholdDraft=null;points=[];invalidateEdit();$('threshold-status').textContent='Нажми на две точки порога на карте.';disableControls(busy);draw();};
$('cancel-threshold').onclick=()=>{thresholdDrawing=false;thresholdDraft=null;points=[];$('threshold-choice').value='';$('threshold-status').textContent='Выбор отменён.';disableControls(busy);draw();};
async function submitThreshold(remove=false){
 if(busy||!view?.thresholds_editable||(!remove&&!thresholdDraft))return;
 const passable=(view.passable_thresholds||[]).map(x=>[...x]),impassable=(view.impassable_thresholds||[]).map(x=>[...x]),lists={passable,impassable};
 const [oldKind,rawIndex]=$('threshold-choice').value.split(':'),index=Number(rawIndex),newKind=$('threshold-kind').value;
 if(oldKind&&lists[oldKind]?.[index])lists[oldKind].splice(index,1);else if(remove)return;
 if(!remove){const line=thresholdDraft.line;if(line[0]===line[2]&&line[1]===line[3]){toast('Точки порога совпадают',true);return;}if(oldKind===newKind)lists[newKind].splice(index,0,line);else lists[newKind].push(line);}
 try{await control('local.thresholds',{map_id:view.map_id,revision:view.revision,passable,impassable});$('threshold-status').textContent='Команда принята. Ожидаю обновление сохранённой карты.';for(const delay of [1500,5000,12000])setTimeout(()=>refreshMap(),delay);}
 catch(e){toast(e.message,true);}
}
$('save-threshold').onclick=()=>submitThreshold();$('delete-threshold').onclick=()=>submitThreshold(true);
// ---- Per-room settings: robot row [suction, water, repeats, order, mode, extra]; only changed rooms are sent.
const roomFields=[['suction',0,{0:'Тихо',1:'Стандарт',2:'Сильный',3:'Турбо'}],['water',1,{1:'Низкая',2:'Средняя',3:'Высокая'}],['repeats',2,{1:'1 раз',2:'2 раза',3:'3 раза'}],['mode',4,{0:'Только сухая',1:'Только влажная',2:'Сухая и влажная'}]];
function renderRoomSettings(){
 const sets=view?.room_settings,rows=[];
 $('room-settings-details').hidden=!sets;
 for(const room of view?.rooms||[]){
  const row=sets?.[room.id];if(!row)continue;
  const line=document.createElement('div');line.className='row';line.dataset.room=room.id;
  line.append(Object.assign(document.createElement('span'),{textContent:room.name}));
  for(const [key,index,names] of roomFields){
   const select=document.createElement('select');select.dataset.key=key;select.setAttribute('aria-label',room.name+': '+key);
   for(const [value,label] of Object.entries(names))select.add(new Option(label,value));
   if(!(row[index] in names))select.add(new Option(String(row[index]),row[index]));
   select.value=String(row[index]);select.dataset.saved=select.value;select.onchange=()=>{$('save-room-settings').disabled=busy||!changedRooms().length;};
   line.append(select);
  }
  rows.push(line);
 }
 $('room-settings').replaceChildren(...rows);$('save-room-settings').disabled=true;
 roomOrder=roomsByOrder(view?.rooms||[],sets);renderRoomOrder(false);renderFurniture();
}
// Cleaning order: row[3] is 1, 2, ...; rooms with 0 go last.
let roomOrder=[];
export function roomsByOrder(rooms,sets){
 const order=id=>sets?.[id]?.[3]||Infinity;
 return rooms.filter(r=>sets?.[r.id]).sort((a,b)=>order(a.id)-order(b.id)||a.id-b.id);
}
function renderRoomOrder(changed){
 $('room-order').replaceChildren(...roomOrder.map((room,i)=>{
  const line=document.createElement('div'),up=document.createElement('button');
  line.className='module-row';line.dataset.orderRoom=room.id;
  up.textContent='Выше';up.className='ghost';up.disabled=i===0;
  up.onclick=()=>{[roomOrder[i-1],roomOrder[i]]=[roomOrder[i],roomOrder[i-1]];renderRoomOrder(true);};
  line.append(Object.assign(document.createElement('span'),{textContent:(i+1)+'. '+room.name}),up);return line;
 }));
 $('save-room-order').disabled=busy||!changed;
}
// ---- Furniture (furniture.go): types 1..25 and default sizes as in the app's FURNITURE_TYPE.
const furnitureTypes={1:['Односпальная кровать',1000,2000],2:['Двуспальная кровать',1500,2000],3:['Кресло',800,700],4:['Двухместный диван',1400,600],5:['Трёхместный диван',2300,700],6:['Стол со стульями',1200,800],7:['Прикроватная тумба',500,400],8:['Журнальный столик (круглый)',1000,1000],9:['Унитаз',400,600],10:['Открытый кошачий туалет',300,500],11:['Лежанка питомца',500,400],12:['Миска',400,200],13:['Пелёнка для питомца',400,600],14:['Холодильник',600,600],15:['Стиральная машина',600,600],16:['Закрытый кошачий туалет',300,500],17:['Кондиционер',400,400],18:['Тумба под ТВ',1600,300],19:['Книжный шкаф',800,300],20:['Обувница',800,400],21:['Шкаф для одежды',2000,600],22:['Растение',300,300],23:['Напольное зеркало',800,400],24:['Угловой диван',2800,1700],25:['Журнальный столик (квадратный)',800,800]};
let furniturePlacing=false;
for(const [id,[name]] of Object.entries(furnitureTypes))$('furniture-type').add(new Option(name,id));
const furnitureName=f=>furnitureTypes[f.type]?.[0]||'Предмет '+f.type;
function drawFurniture(){
 if(!view?.furniture?.length)return;
 ctx.save();ctx.lineWidth=2;ctx.font='11px sans-serif';
 for(const f of view.furniture){
  const [px,py]=robotToPixel(view,f.x,f.y),x=px*3,y=(view.height-py)*3,w=f.width/view.grid_size*3,h=f.height/view.grid_size*3;
  ctx.save();ctx.translate(x,y);ctx.rotate(-f.rotation*Math.PI/180);
  const known=!!furnitureTypes[f.type];ctx.strokeStyle=known?'#c58b2b':'#888';ctx.fillStyle=known?'rgba(197,139,43,.25)':'rgba(136,136,136,.15)';
  ctx.fillRect(-w/2,-h/2,w,h);ctx.strokeRect(-w/2,-h/2,w,h);ctx.restore();
 }
 ctx.restore();
}
function renderFurniture(){
 $('furniture-details').hidden=!view?.furniture_editable;
 const rows=(view?.furniture||[]).map(f=>{
  const line=document.createElement('div'),del=document.createElement('button');line.className='module-row';
  line.append(Object.assign(document.createElement('span'),{textContent:furnitureName(f)+' · '+f.width+'×'+f.height+' мм'}));
  del.textContent='Убрать';del.className='ghost';
  del.onclick=()=>{if(window.confirm('Убрать «'+furnitureName(f)+'» с карты?'))saveFurniture({remove:[f.index],add:[]});};
  line.append(del);return line;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Мебели на карте нет.'}));
 $('furniture-list').replaceChildren(...rows);
}
async function saveFurniture(change){
 try{await control('local.furniture',{map_id:view.map_id,revision:view.revision,...change});$('furniture-status').textContent='Сохранено. Ожидаю обновление карты.';for(const delay of [1500,5000])setTimeout(()=>refreshMap(),delay);}
 catch(e){$('furniture-status').textContent=e.message;}
}
function placeFurniture(x,y){
 furniturePlacing=false;const type=Number($('furniture-type').value),[,w,h]=furnitureTypes[type],[rx,ry]=pixelToRobot(view,x,y);
 saveFurniture({remove:[],add:[{type,x:rx,y:ry,width:w,height:h,rotation:0}]});
}
$('place-furniture').onclick=()=>{if(busy||!view?.furniture_editable)return;furniturePlacing=true;restrictDrawing=false;zoneDrawing=false;thresholdDrawing=false;$('furniture-status').textContent='Нажми на карте, где стоит центр предмета.';};
// ---- Map backups (mapbackup.go): loaded when the block is opened.
const restoreStates={0:'',1:'восстанавливается…',3:'восстановлена'};
async function loadMapBackups(){
 const r=await api('local.map_backup_status'),state=Array.isArray(r.restore)?r.restore[0]?.value:undefined;
 $('map-backups-status').textContent=restoreStates[state]!==undefined?(restoreStates[state]?'Последнее восстановление: '+restoreStates[state]:''):'Состояние восстановления: '+state;
 const ids=r.maps?.ids||[];
 $('saved-maps').replaceChildren(...(ids.length>1?ids:[]).map(id=>{
  const line=document.createElement('div'),use=document.createElement('button');line.className='module-row';
  line.append(Object.assign(document.createElement('span'),{textContent:'Карта '+id+(id===r.maps.current?' · текущая':'')}));
  use.textContent='Сделать текущей';use.className='ghost';use.disabled=id===r.maps.current;
  use.onclick=async()=>{try{await control('local.map_use',{map_id:id});await refreshMap();await loadMapBackups();}catch(e){$('map-backups-status').textContent=e.message;}};
  line.append(use);return line;
 }));
 const rows=r.backups.map(b=>{
  const line=document.createElement('div'),use=document.createElement('button'),del=document.createElement('button');
  line.className='module-row';
  line.append(Object.assign(document.createElement('span'),{textContent:'Карта '+b.map_id+' · '+new Date(b.time*1000).toLocaleString('ru-RU')+' · '+Math.round(b.size/1024)+' КБ'}));
  use.textContent='Восстановить';use.className='ghost';
  use.onclick=async()=>{if(!window.confirm('Заменить текущую карту копией от '+new Date(b.time*1000).toLocaleString('ru-RU')+'?'))return;try{await control('local.map_restore',{name:b.name});$('map-backups-status').textContent='Робот загружает копию…';setTimeout(()=>{refreshMap().catch(()=>{});loadMapBackups().catch(()=>{});},8000);}catch(e){$('map-backups-status').textContent=e.message;}};
  del.textContent='Удалить';del.className='ghost';
  del.onclick=async()=>{if(!window.confirm('Удалить копию?'))return;try{await api('local.map_backup_delete',{name:b.name});await loadMapBackups();}catch(e){$('map-backups-status').textContent=e.message;}};
  line.append(use,del);return line;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Копий пока нет.'}));
 $('map-backups').replaceChildren(...rows);
}
$('map-backups-details').ontoggle=()=>{if($('map-backups-details').open)loadMapBackups().catch(e=>{$('map-backups-status').textContent=e.message;});};
$('make-map-backup').onclick=async()=>{try{await api('local.map_backup');await loadMapBackups();$('map-backups-status').textContent='Копия сделана.';}catch(e){$('map-backups-status').textContent=e.message;}};
$('save-room-order').onclick=async()=>{
 try{await control('local.room_order',{map_id:view.map_id,revision:view.revision,rooms:roomOrder.map(r=>r.id)});$('room-settings-status').textContent='Порядок сохранён.';await refreshMap();}
 catch(e){$('room-settings-status').textContent=e.message;}
};
function changedRooms(){
 return [...$('room-settings').querySelectorAll('[data-room]')].filter(line=>[...line.querySelectorAll('select')].some(s=>s.value!==s.dataset.saved))
  .map(line=>Object.fromEntries([['id',Number(line.dataset.room)],...[...line.querySelectorAll('select')].map(s=>[s.dataset.key,Number(s.value)])]));
}
$('save-room-settings').onclick=async()=>{
 const rooms=changedRooms();if(!rooms.length)return;
 try{await control('local.room_settings',{map_id:view.map_id,revision:view.revision,rooms});$('room-settings-status').textContent='Настройки сохранены.';await refreshMap();}
 catch(e){$('room-settings-status').textContent=e.message;}
};
// ---- Room types (seg_inf type, roomtypes.go): 0 is an own name; only changed rooms are sent.
const roomTypes={1:'Гостиная',2:'Спальня',15:'Вторая спальня',3:'Кабинет',4:'Кухня',5:'Столовая',6:'Ванная',7:'Балкон',8:'Коридор',9:'Кладовая',10:'Гардеробная',11:'Приёмная',12:'Офис',13:'Спортзал',14:'Зона отдыха',0:'Своё название'};
function renderRoomTypes(){
 const rooms=view?.rooms||[];$('room-types-details').hidden=!rooms.length;
 $('room-types').replaceChildren(...rooms.map(room=>{
  const line=document.createElement('div'),select=document.createElement('select'),name=document.createElement('input');
  line.className='row';line.dataset.room=room.id;line.append(Object.assign(document.createElement('span'),{textContent:room.name}),select,name);
  for(const [value,label] of Object.entries(roomTypes))select.add(new Option(label,value));
  select.setAttribute('aria-label',room.name+': тип');select.value=String(room.type??0);select.dataset.saved=select.value;
  name.maxLength=24;name.placeholder='Название';name.setAttribute('aria-label',room.name+': название');name.value=room.type?'':room.name;name.dataset.saved=name.value;
  const sync=()=>{name.hidden=select.value!=='0';$('save-room-types').disabled=busy||!changedRoomTypes().length;};
  select.onchange=sync;name.oninput=sync;name.hidden=select.value!=='0';return line;
 }));
 $('save-room-types').disabled=true;
 renderRoomFloors();
}
// ---- Floor material (roomfloors.go): the map previews the choice before it is saved.
const floorNames={0:'Прочее',2:'Плитка',4:'Доски горизонтально',3:'Доски вертикально'};
function renderRoomFloors(){
 $('room-floors').replaceChildren(...(view?.rooms||[]).map(room=>{
  const line=document.createElement('div'),select=document.createElement('select');line.className='row';
  for(const [value,label] of Object.entries(floorNames))select.add(new Option(label,value));
  if(room.floor===1)select.add(new Option('Доски (по длинной стороне)','1'));
  select.value=String(room.floor in floorNames||room.floor===1?room.floor:0);select.dataset.saved=select.value;select.dataset.room=room.id;select.setAttribute('aria-label',room.name+': покрытие пола');
  select.onchange=()=>{$('save-room-floors').disabled=busy||!changedFloors().length;draw();};
  line.append(Object.assign(document.createElement('span'),{textContent:room.name}),select);return line;
 }));
 $('save-room-floors').disabled=true;
}
const changedFloors=()=>[...$('room-floors').querySelectorAll('select')].filter(s=>s.value!==s.dataset.saved).map(s=>({id:Number(s.dataset.room),floor:Number(s.value)}));
$('save-room-floors').onclick=async()=>{
 const rooms=changedFloors();if(!rooms.length)return;
 try{await control('local.room_floors',{map_id:view.map_id,revision:view.revision,rooms});$('room-floors-status').textContent='Покрытие сохранено.';for(const delay of [1500,5000])setTimeout(()=>refreshMap(),delay);}
 catch(e){$('room-floors-status').textContent=e.message;}
};
function changedRoomTypes(){
 return [...$('room-types').querySelectorAll('[data-room]')].filter(line=>{const [s,n]=line.querySelectorAll('select,input');return s.value!==s.dataset.saved||(s.value==='0'&&n.value!==n.dataset.saved);})
  .map(line=>{const [s,n]=line.querySelectorAll('select,input'),room={id:Number(line.dataset.room),type:Number(s.value)};if(room.type===0)room.name=n.value.trim();return room;});
}
$('save-room-types').onclick=async()=>{
 const rooms=changedRoomTypes();if(!rooms.length)return;
 try{await control('local.room_types',{map_id:view.map_id,revision:view.revision,rooms});$('room-types-status').textContent='Типы сохранены.';for(const delay of [1500,5000])setTimeout(()=>refreshMap(),delay);}
 catch(e){$('room-types-status').textContent=e.message;}
};
// ---- Virtual walls and zones: edited locally as a draft, then all three lists are saved at once.
const restrictKinds={walls:'Стена',no_go_zones:'Запретная зона',no_mop_zones:'Без влажной уборки',carpets_added:'Ковёр',carpets_ignored:'Не считать ковром',ramps:'Пандус'},carpetKinds=['carpets_added','carpets_ignored'];
const restrictions=()=>restrictDraft||Object.fromEntries(Object.keys(restrictKinds).map(k=>[k,view?.[k]||[]]));
function renderRestrictions(){
 const lists=restrictions(),rows=[];
 for(const [kind,label] of Object.entries(restrictKinds))lists[kind].forEach((item,index)=>{
  const row=document.createElement('div'),text=document.createElement('span'),remove=document.createElement('button');
  row.className='module-row';text.textContent=label+' '+(index+1);remove.textContent='Удалить';remove.className='ghost';
  remove.onclick=()=>{const next=structuredClone(restrictions());next[kind].splice(index,1);restrictDraft=next;$('restrict-status').textContent='Удалено из черновика. Нажми «Сохранить изменения».';renderRestrictions();draw();disableControls(busy);};
  row.append(text,remove);rows.push(row);
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:view?.restrictions_editable?'Запретов и добавленных ковров нет.':'Эта карта не содержит доступного формата запретов.'}));
 $('restrict-list').replaceChildren(...rows);
}
// Checker tile of half cells (a cell is 3 canvas pixels, so 2-pixel squares are the closest whole size).
const carpetPatterns={};
function carpetPattern(alpha){
 if(!carpetPatterns[alpha]){const t=document.createElement('canvas');t.width=t.height=4;const c=t.getContext('2d');c.fillStyle=`rgba(0,0,0,${alpha})`;c.fillRect(2,0,2,2);c.fillRect(0,2,2,2);carpetPatterns[alpha]=ctx.createPattern(t,'repeat');}
 return carpetPatterns[alpha];
}
function drawRestrictions(){
 const lists=restrictions(),px=(x,y)=>{const p=robotToPixel(view,x,y);return [p[0]*3,(view.height-p[1])*3];};
 ctx.save();ctx.lineWidth=2;
 // Carpets the robot found: the app's texture, map cells split 2×2 with the dark halves in a checkerboard.
 ctx.fillStyle=carpetPattern(.15);for(const c of view?.carpets||[]){ctx.beginPath();for(let i=0;i+1<c.points.length;i+=2){const [x,y]=px(c.points[i],c.points[i+1]);i?ctx.lineTo(x,y):ctx.moveTo(x,y);}ctx.closePath();ctx.fill();}
 for(const [kind,items] of Object.entries(lists))for(const r of items){
  if(!Array.isArray(r)||r.length!==4)continue;const [x0,y0]=px(r[0],r[1]),[x1,y1]=px(r[2],r[3]);
  if(kind==='walls'){ctx.setLineDash([]);ctx.strokeStyle='#ff4d4d';ctx.beginPath();ctx.moveTo(x0,y0);ctx.lineTo(x1,y1);ctx.stroke();continue;}
  const style={no_mop_zones:['#5aa9ff','rgba(90,169,255,.25)'],carpets_added:['rgba(0,0,0,.45)',carpetPattern(.3)],carpets_ignored:['#8a8a8a','rgba(138,138,138,.15)'],ramps:['#2f9e44','rgba(47,158,68,.25)']}[kind]||['#ff4d4d','rgba(255,77,77,.25)'];
  ctx.strokeStyle=style[0];ctx.fillStyle=style[1];ctx.setLineDash(kind==='carpets_ignored'?[6,4]:[]);
  ctx.fillRect(Math.min(x0,x1),Math.min(y0,y1),Math.abs(x1-x0),Math.abs(y1-y0));ctx.strokeRect(Math.min(x0,x1),Math.min(y0,y1),Math.abs(x1-x0),Math.abs(y1-y0));
 }
 if(restrictPoint){const [x,y]=px(...restrictPoint);ctx.fillStyle='#ff4d4d';ctx.beginPath();ctx.arc(x,y,4,0,Math.PI*2);ctx.fill();}
 ctx.restore();
}
function addRestrictionPoint(x,y){
 const p=pixelToRobot(view,x,y);
 if(!restrictPoint){restrictPoint=p;$('restrict-status').textContent='Укажи вторую точку.';draw();return;}
 const kind=$('restrict-kind').value,row=[...restrictPoint,...p];restrictPoint=null;restrictDrawing=false;
 if(row[0]===row[2]&&row[1]===row[3]||(kind!=='walls'&&(row[0]===row[2]||row[1]===row[3]))){$('restrict-status').textContent='Точки слишком близко. Укажи их заново.';draw();return;}
 if(kind!=='walls')row.splice(0,4,Math.min(row[0],row[2]),Math.min(row[1],row[3]),Math.max(row[0],row[2]),Math.max(row[1],row[3]));
 const next=structuredClone(restrictions());next[kind].push(row);restrictDraft=next;
 $('restrict-status').textContent=restrictKinds[kind]+' добавлена в черновик. Нажми «Сохранить изменения».';renderRestrictions();draw();disableControls(busy);
}
$('draw-restrict').onclick=()=>{if(busy||!view?.restrictions_editable)return;restrictDrawing=true;restrictPoint=null;zoneDrawing=false;thresholdDrawing=false;$('restrict-status').textContent=$('restrict-kind').value==='walls'?'Нажми на два конца стены.':'Нажми на два противоположных угла зоны.';};
$('cancel-restrict').onclick=()=>{restrictDraft=null;restrictDrawing=false;restrictPoint=null;$('restrict-status').textContent='Изменения отменены.';renderRestrictions();draw();disableControls(busy);};
$('save-restrict').onclick=async()=>{
 if(!restrictDraft)return;
 const d=restrictDraft,changed=k=>JSON.stringify(d[k])!==JSON.stringify(view[k]||[]);
 try{
  if(['walls','no_go_zones','no_mop_zones'].some(changed))await control('local.restrictions',{map_id:view.map_id,revision:view.revision,walls:d.walls,no_go_zones:d.no_go_zones,no_mop_zones:d.no_mop_zones});
  if(carpetKinds.some(changed)){const now=await api('local.map_view');await control('local.carpets',{map_id:now.map_id,revision:now.revision,carpets_added:d.carpets_added,carpets_ignored:d.carpets_ignored});}
  if(changed('ramps')){const now=await api('local.map_view');await control('local.thresholds',{map_id:now.map_id,revision:now.revision,passable:now.passable_thresholds||[],impassable:now.impassable_thresholds||[],ramps:d.ramps});}
  $('restrict-status').textContent='Изменения сохранены. Ожидаю обновление сохранённой карты.';for(const delay of [1500,5000,12000])setTimeout(()=>refreshMap(),delay);}
 catch(e){$('restrict-status').textContent=e.message;}
};

// ---- Types of carpets the robot found (CarpetEditTypePage): 5 medium-to-high pile, 6 low pile, 7 other.
const carpetTypeNames={5:'Средний и высокий ворс',6:'Низкий ворс',7:'Прочее'};
// Per-carpet handling (carpet_clean_set); -1 follows the global carpet setting.
const carpetCleanNames=[['-1','Как для всех ковров'],['1','Объезжать'],['2','Только пылесос'],['3','Снимать швабру'],['6','Не замечать'],['7','Проезжать']];
function renderCarpetTypes(){
 const list=view?.carpets||[];$('carpet-types-block').hidden=!list.length;
 $('carpet-types').replaceChildren(...list.map(c=>{
  const row=document.createElement('label'),select=document.createElement('select');
  row.className='module-row';row.append('Ковёр '+c.id+' ');select.dataset.carpet=c.id;
  for(const [v,n] of Object.entries(carpetTypeNames))select.append(new Option(n,v));select.value=String(c.type??7);
  row.append(select);
  if(c.clean!==undefined){const mode=document.createElement('select');mode.dataset.clean=c.id;for(const [v,n] of carpetCleanNames)mode.append(new Option(n,v));mode.value=String(c.clean);row.append(mode);}
  return row;
 }));
}
$('save-carpet-types').onclick=async()=>{
 const types={},clean={};
 for(const s of $('carpet-types').querySelectorAll('select[data-carpet]')){const c=view.carpets.find(x=>String(x.id)===s.dataset.carpet);if(c&&String(c.type??7)!==s.value)types[c.id]=Number(s.value);}
 for(const s of $('carpet-types').querySelectorAll('select[data-clean]')){const c=view.carpets.find(x=>String(x.id)===s.dataset.clean);if(c&&String(c.clean)!==s.value)clean[c.id]=Number(s.value);}
 if(!Object.keys(types).length&&!Object.keys(clean).length){$('carpet-types-status').textContent='Ничего не изменено.';return;}
 try{
  // A new type makes the robot pick a recommended mode, so an explicit mode goes after it.
  if(Object.keys(types).length)await control('local.carpet_types',{map_id:view.map_id,revision:view.revision,types});
  if(Object.keys(clean).length){const now=await api('local.map_view');await control('local.carpet_clean',{map_id:now.map_id,revision:now.revision,clean});}
  $('carpet-types-status').textContent='Ковры сохранены.'+(Object.keys(types).length&&!Object.keys(clean).length?' Робот сам подобрал способ уборки под тип.':'');for(const delay of [1500,5000])setTimeout(()=>refreshMap(),delay);}
 catch(e){$('carpet-types-status').textContent=e.message;}
};
// Firmware hardware table of this model (capabilities); settings with "hw" need these values.
let hardware=null;
export function decodeSetting(def,values){
 if(def.hw&&hardware&&Object.entries(def.hw).some(([k,v])=>Object.hasOwn(hardware,'RUNTIME_'+k)&&String(hardware['RUNTIME_'+k])!==v))return undefined;
 const raw=values.get(def.siid+'/'+def.piid);if(raw===undefined||raw===null)return undefined;
 if(def.auto||def.json_key){
  let object;try{object=typeof raw==='string'?JSON.parse(raw):raw;}catch{return undefined;}
  if(def.auto){const entries=Array.isArray(object)?object:[object],found=entries.find(x=>x&&x.k===def.auto);return found?.v;}
  return object&&!Array.isArray(object)&&Object.hasOwn(object,def.json_key)?object[def.json_key]:undefined;
 }
 if(def.bit){if(typeof raw!=='number'||raw<0||raw>0xffffffff)return undefined;return !!(raw&def.bit);}
 if(def.packed){if(!Number.isInteger(raw)||raw<0||raw>0xffffffff)return undefined;const mode=raw&3;return def.packed==='mode'?(mode===0?2:mode===2?0:mode):def.packed==='interval'?((raw>>>8)&255):(raw>>>16);}
 if(def.kind==='window'){try{const parsed=typeof raw==='string'?JSON.parse(raw):raw;if(def.window==='dnd')return Array.isArray(parsed)?{enabled:!!parsed[0]?.en,start:parsed[0]?.st||'22:00',end:parsed[0]?.et||'08:00'}:undefined;return parsed&&!Array.isArray(parsed)&&typeof parsed==='object'?{enabled:!!parsed.enable,start:parsed.startTime||'22:00',end:parsed.endTime||'08:00'}:undefined;}catch{return undefined;}}
 if(def.kind==='bool'&&typeof raw!=='boolean'&&(!Number.isInteger(raw)||raw<0))return undefined;
 return raw;
}
export function settingValue(def,value,raw){
 if(def.auto){if(['MeticulousTwist','MopScalable','SmartAutoMop','SmartAutoWash'].includes(def.auto)&&Number(value)===0){const current=decodeSetting(def,new Map([[def.siid+'/'+def.piid,raw]]));if(current>0)value=-current;}return JSON.stringify({k:def.auto,v:Number(value)});}
 if(def.json_key){const data=typeof raw==='string'?JSON.parse(raw):structuredClone(raw);data[def.json_key]=typeof data[def.json_key]==='boolean'?!!value:Number(value);return typeof raw==='string'?JSON.stringify(data):data;}
 if(def.bit)return value?(raw|def.bit)>>>0:(raw&~def.bit)>>>0;
 if(def.packed){const next=Number(value);if(def.packed==='mode'){const code=next===0?2:next===2?0:next;return ((raw&~3)|code)>>>0;}if(def.packed==='interval')return ((raw&~0xff00)|(next<<8))>>>0;return ((raw&0xffff)|(next<<16))>>>0;}
 if(def.kind==='window'){
  if(!/^([01]\d|2[0-3]):[0-5]\d$/.test(value.start)||!/^([01]\d|2[0-3]):[0-5]\d$/.test(value.end)||value.start===value.end)throw Error('Укажи разные время начала и окончания.');
  const data=typeof raw==='string'?JSON.parse(raw):structuredClone(raw);
  if(def.window==='dnd'){if(!data.length)data.push({id:1,wk:127,ss:0});Object.assign(data[0],{en:value.enabled,st:value.start,et:value.end});}else Object.assign(data,{enable:value.enabled,startTime:value.start,endTime:value.end});
  return typeof raw==='string'?JSON.stringify(data):data;
 }
 if(def.kind==='bool')return typeof raw==='boolean'?!!value:value&&Number(raw)>0?raw:Number(!!value);
 return value;
}
function settingControl(def,value){
 const input=document.createElement(def.kind==='select'?'select':'input');input.id='setting-'+def.id;input.setAttribute('aria-label',def.label);
 if(def.kind==='select'){
  for(const option of def.options)input.add(new Option(option.label,String(option.value)));
  let normalized=value;if(def.auto&&Number(value)<0&&def.options.some(x=>x.value===0))normalized=0;
  if(![...input.options].some(x=>x.value===String(normalized)))input.add(new Option('Текущее: '+value,String(value)));
  input.value=String(normalized);
 }else if(def.kind==='bool'){input.type='checkbox';input.className='switch';input.setAttribute('role','switch');input.checked=!!value;}
 else{input.type=def.kind==='number'?'number':def.kind==='time'?'time':'text';input.value=String(value);if(def.kind==='number'){input.min=def.min;input.max=def.max;input.step=def.step||1;}else input.maxLength=128;}
 return input;
}
// Groups in the order of the app's settings pages; the first matching rule wins.
const settingGroups=[['Уборка',/^(cleaning_mode|mop_humidity|wetness_level|cleangenius(_mode)?|cleaning_route|customized_cleaning|tight_mopping|auto_recleaning|resume_cleaning|max_suction_power|ultra_clean_mode|mopping_type)$/],['Ковры',/carpet/],
 ['Чистка пола',/^(collision_avoidance|floor_direction_cleaning|stain_avoidance|side_reach|mop_extend(_frequency)?|gap_cleaning_extension|mopping_under_furnitures|auto_mount_mop|mop_pad_swing|wider_corner_coverage|pet_focused_cleaning|dynamic_obstacle_cleaning|pressurized_cleaning|mop_pressure|ai_bit_large_particles_boost)$/],
 ['Базовая станция',/^(?!dnd_).*(wash|dry|uv_|dust|empty|hair|detergent|water|self_clean|mop_temperature|scraper|low_lying|change_mop|mop_cleaning_remainder)/],['Препятствия и ИИ',/^ai_|obstacle|crossing|fill_light|intelligent_recognition|human_follow|lds/],
 ['Голос',/voice/],['Что ограничивает ночной режим',/^dnd_(disable|reduce)/],['Не беспокоить',/^dnd/],['Прочее',/./]];
// Groups with their own panel show there, next to the matching controls; if that panel is hidden
// (no rights or the robot lacks it) they stay under «Все настройки пылесоса».
const settingHomes={'Базовая станция':'station-settings','Голос':'voice-settings','Что ограничивает ночной режим':'dnd-settings'};
function settingGroup(id){
 const title=settingGroups.find(([,rule])=>rule.test(id))[0];
 if(!document.querySelector('.setting-group'))for(const [name] of settingGroups){
  const g=document.createElement('section'),home=$(settingHomes[name]),inHome=home&&!home.closest('[hidden]');g.className='setting-group';g.hidden=true;g.dataset.group=name;
  if(!inHome)g.append(Object.assign(document.createElement('h3'),{textContent:name}));(inHome?home:$('settings-list')).append(g);
 }
 return document.querySelector(`.setting-group[data-group="${title}"]`);
}
function renderSettings(){
 const query=$('settings-search').value.trim().toLocaleLowerCase();let available=0;
 for(const def of settings){
  const value=decodeSetting(def,settingsSnapshot);if(value===undefined)continue;available++;
  let row=settingsRows.get(def.id);
  if(!row){
   row=document.createElement('div');row.className='setting-row';row.dataset.setting=def.id;const label=document.createElement('label');label.textContent=def.label;label.htmlFor='setting-'+def.id;
   const current=document.createElement('span');current.className='setting-current';label.append(current);
   let input,readInput;
   if(def.kind==='window'){
    input=document.createElement('div');input.className='setting-window';const enabled=document.createElement('input'),start=document.createElement('input'),end=document.createElement('input');enabled.type='checkbox';enabled.className='switch';enabled.setAttribute('role','switch');enabled.setAttribute('aria-label',def.label+' включено');start.type=end.type='time';start.setAttribute('aria-label',def.label+' начало');end.setAttribute('aria-label',def.label+' окончание');input.append(enabled,start,end);row.setInput=v=>{enabled.checked=v.enabled;start.value=v.start;end.value=v.end;};readInput=()=>({enabled:enabled.checked,start:start.value,end:end.value});
   }else{input=settingControl(def,value);row.setInput=v=>{if(def.kind==='bool')input.checked=!!v;else input.value=String(def.auto&&v<0&&def.options?.some(x=>x.value===0)?0:v);};readInput=()=>def.kind==='bool'?input.checked:def.kind==='select'?def.options.find(x=>String(x.value)===input.value)?.value??value:def.kind==='number'?Number(input.value):input.value;}
   row.setInput(value);input.addEventListener('input',()=>settingsDirty.add(def.id));
   // Applied as soon as it changes; a refusal or a bad value puts back what the robot has.
   const apply=async()=>{
    settingsDirty.add(def.id);let sent=false;
    try{
     const chosen=readInput();if(def.kind==='number'&&(!Number.isInteger(chosen)||chosen<def.min||chosen>def.max))throw Error('Допустимое значение: '+def.min+'–'+def.max);
     if(def.kind==='time'&&!/^([01]\d|2[0-3]):[0-5]\d$/.test(chosen))throw Error('Укажи время в формате ЧЧ:ММ');
     if(busy)throw Error('Робот выполняет другую команду, попробуй ещё раз');
     const raw=settingsSnapshot.get(def.siid+'/'+def.piid);
     sent=true;await control('local.setting',{siid:def.siid,piid:def.piid,expected:raw,value:settingValue(def,chosen,raw)});
    }catch(e){if(!sent)toast(e.message,true); // control() already reported its own errors
    const now=decodeSetting(def,settingsSnapshot);if(now!==undefined)row.setInput(now);}
    finally{settingsDirty.delete(def.id);await refreshSettings();}
   };
   // Switches and lists apply on change; typed values (number, time, text) wait for «ОК», a schedule's switch applies at once.
   const typed=['number','time','text','window'].includes(def.kind);let ok=null;
   if(typed){ok=document.createElement('button');ok.textContent='ОК';ok.className='setting-apply';ok.onclick=apply;if(def.kind==='window')input.querySelector('.switch').addEventListener('change',apply);}
   else input.addEventListener('change',apply);
   row.append(label,input);if(ok)row.append(ok);settingsRows.set(def.id,row);settingGroup(def.id).append(row);
  }else if(!settingsDirty.has(def.id))row.setInput(value);
  row.querySelector('.setting-current').textContent=def.kind==='bool'?'':'Сейчас: '+(typeof value==='object'?(value.enabled?'включено':'выключено')+' '+value.start+'–'+value.end:typeof value==='boolean'?(value?'включено':'выключено'):String(value));
  row.hidden=!def.label.toLocaleLowerCase().includes(query)&&!row.closest('#station-settings,#voice-settings,#dnd-settings');
 }
 for(const group of document.querySelectorAll('.setting-group'))group.hidden=![...group.querySelectorAll('.setting-row')].some(r=>!r.hidden);
 $('settings-status').textContent='Доступно настроек: '+available;
 disableControls(busy);
}
async function refreshSettings(){
 if(!token||busy||settingsLoading||document.hidden)return;settingsLoading=true;const epoch=viewEpoch;$('settings-status').textContent='Читаю настройки…';
 try{
  const pairs=[...new Map(settings.map(d=>[d.siid+'/'+d.piid,{siid:d.siid,piid:d.piid}])).values()],snapshot=new Map();
  for(let i=0;i<pairs.length;i+=20){const rows=await api('get_properties',pairs.slice(i,i+20));if(epoch!==viewEpoch||!token)return;if(!Array.isArray(rows))throw Error('Некорректный ответ настроек');for(const row of rows)if(row.code===0&&row.siid&&row.piid&&'value'in row)snapshot.set(row.siid+'/'+row.piid,row.value);}
  settingsSnapshot=snapshot;for(const [id,row]of settingsRows){const def=settings.find(x=>x.id===id);if(decodeSetting(def,snapshot)===undefined)row.hidden=true;}renderSettings();
 }catch(e){if(!e.suspended&&epoch===viewEpoch){$('settings-status').textContent=e.message;connectionLost(e.message);}}finally{if(epoch===viewEpoch)settingsLoading=false;}
}
for(const id of ['settings-details','station-settings-details','voice-settings-details','dnd-settings-details'])$(id).ontoggle=e=>{if(e.target.open)refreshSettings();};$('refresh-settings').onclick=()=>refreshSettings();$('settings-search').oninput=()=>renderSettings();

async function rebootRobot(){
 if(busy||!window.confirm('Перезагрузить робот? Связь временно пропадёт.'))return;
 busy=true;disableControls(true);
 try{const result=await api('local.reboot');if(!result.accepted)throw Error('Запрос перезагрузки не принят');rebootingUntil=Date.now()+90000;connectionLost('Робот перезагружается');toast('Перезагрузка запрошена. Дождись восстановления связи.');}
 catch(e){toast(e.message,true);}finally{busy=false;disableControls(false);}
}
function suspendReads(reason='suspend'){stopEvents();viewEpoch++;refreshing=false;settingsLoading=false;for(const controller of activeReads)controller.abort(reason);}
function scheduleResume(){clearTimeout(resumeTimer);resumeTimer=setTimeout(async()=>{
 if(document.hidden||!token)return;if(busy){scheduleResume();return;}
 suspendReads();
 if($('dashboard').hidden){try{await login();}catch(e){if(!e.suspended)connectionLost(e.message);}return;}
 await refresh();streamEvents();await refreshMap();if(document.querySelector('[id$=settings-details][open]'))await refreshSettings();
 },250);}
$('reconnect').onclick=scheduleResume;
document.addEventListener('visibilitychange',()=>{if(document.hidden)suspendReads();else scheduleResume();});
window.addEventListener('online',scheduleResume);window.addEventListener('pageshow',scheduleResume);window.addEventListener('focus',scheduleResume);

// Modules live on their own page: #modules shows it, anything else the robot dashboard.
function showPage(){const modules=location.hash==='#modules';$('main-page').hidden=modules;$('modules-page').hidden=!modules;$('nav-main').classList.toggle('active',!modules);$('nav-modules').classList.toggle('active',modules);}
window.addEventListener('hashchange',showPage);showPage();
// ---- Messages (messages.go): the agent's own feed, newest first.
export function messageText(m){
 if(m.type==='error')return 'Ошибка: '+(m.text||errorTexts[m.code]||String(m.name||m.code).replaceAll('_',' '));
 if(m.type==='consumable')return (consumableNames[m.name]||m.name)+(m.code===0?': ресурс исчерпан':': осталось '+m.code+' %');
 if(m.type==='run')return m.name==='docked'?'Уборка закончена, робот на базе':m.name==='error'?'Уборка прервана ошибкой':'Уборка остановлена';
 if(m.type==='self_test')return m.code===1527?'Станция начала саморемонт':'Саморемонт станции закончился';
 return m.type;
}
async function loadMessages(){
 const list=await api('local.messages_status');
 $('messages-count').textContent=list.length?String(list.length):'';
 const rows=list.map(m=>{const row=document.createElement('div');row.className='module-row';row.append(Object.assign(document.createElement('span'),{textContent:new Date(m.time).toLocaleString('ru-RU')+' · '+messageText(m)}));if(m.type==='error')row.classList.add('error');return row;});
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Сообщений нет.'}));
 $('messages').replaceChildren(...rows);
}
$('messages-details').ontoggle=()=>{if($('messages-details').open)loadMessages().catch(e=>toast(e.message,true));};
$('clear-messages').onclick=async()=>{if(!window.confirm('Очистить сообщения?'))return;try{await api('local.messages_clear');await loadMessages();}catch(e){toast(e.message,true);}};
// ---- Camera trips (patrol.go): points in robot millimetres, numbered on the map.
let patrolDrawing=false,patrolPoints=[];
function drawPatrol(){
 if(!patrolPoints.length)return;
 ctx.save();ctx.font='bold 12px sans-serif';ctx.textAlign='center';ctx.textBaseline='middle';
 patrolPoints.forEach((p,i)=>{const [px,py]=robotToPixel(view,p[0],p[1]),x=px*3,y=(view.height-py)*3;ctx.fillStyle='#7b4dff';ctx.beginPath();ctx.arc(x,y,9,0,Math.PI*2);ctx.fill();ctx.fillStyle='#fff';ctx.fillText(String(i+1),x,y);});
 ctx.restore();
}
function patrolButtons(){$('go-to-point').disabled=patrolPoints.length!==1;$('start-patrol').disabled=!patrolPoints.length;}
function addPatrolPoint(x,y){
 if(patrolPoints.length>=20){$('patrol-status').textContent='Не больше 20 точек.';return;}
 patrolPoints.push(pixelToRobot(view,x,y));$('patrol-status').textContent='Точек: '+patrolPoints.length+'. Нажми ещё или начни.';patrolButtons();draw();
}
$('draw-patrol').onclick=()=>{if(busy||!view||view.empty)return;patrolDrawing=true;zoneDrawing=false;restrictDrawing=false;thresholdDrawing=false;furniturePlacing=false;$('patrol-status').textContent='Нажимай на карте точки по порядку.';};
$('clear-patrol').onclick=()=>{patrolDrawing=false;patrolPoints=[];$('patrol-status').textContent='';patrolButtons();draw();};
$('go-to-point').onclick=()=>{patrolDrawing=false;command('go_to',{points:patrolPoints}).then(()=>{$('patrol-status').textContent='Робот едет к точке.';},()=>{});};
$('start-patrol').onclick=()=>{patrolDrawing=false;command('patrol',{points:patrolPoints}).then(()=>{$('patrol-status').textContent='Патруль начат: точек '+patrolPoints.length+'.';},()=>{});};

// ---- Patrol schedules (patrolschedule.go): the agent starts its patrol at these times.
async function loadPatrolSchedules(){
 const list=await api('local.patrol_schedules_status');
 const rows=(list||[]).map(p=>{
  const row=document.createElement('div'),text=document.createElement('span'),toggle=document.createElement('button'),del=document.createElement('button');
  row.className='module-row';text.textContent=p.time+' · '+scheduleDays(p.days)+' · точек '+p.points.length+(p.enabled?'':' · выключено');
  toggle.textContent=p.enabled?'Выключить':'Включить';toggle.className='ghost';
  toggle.onclick=()=>control('local.patrol_schedule',{id:p.id,enabled:!p.enabled,time:p.time,days:p.days,points:p.points}).then(loadPatrolSchedules,()=>{});
  del.textContent='Удалить';del.className='ghost';
  del.onclick=()=>{if(window.confirm('Удалить расписание патруля на '+p.time+'?'))control('local.patrol_schedule_delete',{id:p.id}).then(loadPatrolSchedules,()=>{});};
  row.append(text,toggle,del);return row;
 });
 if(!rows.length)rows.push(Object.assign(document.createElement('p'),{className:'muted small',textContent:'Расписаний патруля нет.'}));
 $('patrol-schedules').replaceChildren(...rows);
}
$('add-patrol-schedule').onclick=()=>{
 if(!patrolPoints.length)return toast('Отметь точки патруля на карте',true);
 const days=dayNames.map((_,i)=>$('patrol-days').querySelector('input[value="'+i+'"]').checked?'1':'0').join('');
 control('local.patrol_schedule',{enabled:true,time:$('patrol-time').value,days,points:patrolPoints}).then(()=>{$('patrol-status').textContent='Расписание патруля сохранено.';loadPatrolSchedules().catch(()=>{});},()=>{});
};
