import {settings} from '/settings.js';
import {pages} from '/pages.js';
import {actions} from '/actions.js';
import {mountVoice} from '/voice.js';
import {mountMonitor} from '/monitor.js';
const $=id=>document.getElementById(id);
const captiveSetup=new URLSearchParams(location.search).get('setup')==='wifi';
const statusNames={0:'Ожидание',1:'Пауза',2:'Уборка',3:'Возвращается',4:'Локальная уборка',5:'Вдоль стен',6:'Заряжается',12:'Ошибка',13:'Ручное управление',14:'Спит',17:'Ожидание',18:'Уборка комнат',19:'Уборка зоны',20:'Точечная уборка',21:'Строит карту',15:'Тестовый режим',1527:'Саморемонт станции',22:'Патрулирование',23:'Едет к точке камеры',1501:'Проверка воды'};
// Station codes observed on r2505; other codes are shown as numbers.
// 4/25 as the app plugin names it (washStation).
const stationNames={0:'ожидание',1:'мойка салфеток',2:'сушка',3:'возврат на мойку',4:'мойка на паузе',5:'набор воды',6:'грязная вода, возврат на станцию',7:'швабре нужна вода'};
const palette=['#7fd9c2','#a3bcec','#e5bd8a','#bcaceb','#a7d39a','#e3a5b4','#93cdd9','#d4d093'];
// Embedded in a dashboard (Home Assistant): /?embed=1#token=… shows only the map and the start controls; the token
// comes in the fragment (never sent to a server), is kept for the tab only and is removed from the address.
const embed=new URLSearchParams(location.search).get('embed')==='1',hashToken=new URLSearchParams(location.hash.slice(1)).get('token')||'';
if(embed)document.body.classList.add('embed');
if(hashToken){sessionStorage.setItem('dreame-token',hashToken);history.replaceState(null,'',location.pathname+location.search);}
let token=sessionStorage.getItem('dreame-token')||(embed?'':localStorage.getItem('dreame-token'))||'';
let timer=null,busy=false,refreshing=false,view=null,selected=new Set(),points=[],pending=null,toastTimer=null,fanDirty=false,volumeDirty=false;
let viewEpoch=0,resumeTimer=null,roomOptionsDirty=false,fanPending=false,thresholdDrawing=false,thresholdDraft=null,settingsLoading=false,settingsSnapshot=new Map(),settingsRows=new Map(),rebootingUntil=0;
const activeReads=new Set(),settingsDirty=new Set();
let dndDirty=false,careShown='',restrictDraft=null,restrictSel=null,zones=[],zoneSel=-1,furnitureDraft=null,patrolSel=-1;
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
const SUSPENDED='Чтение приостановлено';
// A read cut short on purpose (tab hidden, resume, logout) is not an error worth showing.
function toast(message,error=false){if(error&&message===SUSPENDED)return;clearTimeout(toastTimer);$('toast').textContent=message;$('toast').classList.toggle('error',error);$('toast').hidden=false;toastTimer=setTimeout(()=>$('toast').hidden=true,7000);}
function isRead(method){return method==='get_properties'||method==='miIO.info'||method==='local.map_view'||method==='local.live_pose'||method==='local.maps'||method.endsWith('_status')||method==='/api/v1/state'||method==='/api/v1/capabilities'||method==='/api/v1/modules'||method==='/api/v1/system/update'||method==='/api/v1/system/firmware'||method==='/api/v1/logs'||method==='/api/v1/pets/photos';}
function connectionLost(message){$('connection').textContent=rebootingUntil>Date.now()?'Перезагрузка…':'Восстанавливаю связь…';$('connection').classList.add('connection-offline');$('reconnect').hidden=!token;$('reconnect').title=message;}
// After an agent update an open page keeps the old interface. Every response carries the agent's version: when it changes,
// reload the page, or offer a banner while something would be lost (map editing, camera monitoring, a command, typing).
let pageAgentVersion=null,newAgentVersion=null;
function noteAgentVersion(response){
 const v=response.headers.get('X-Agent-Version');if(!v)return;
 if(!pageAgentVersion){pageAgentVersion=v;return;}
 if(v===pageAgentVersion||v===newAgentVersion)return;
 newAgentVersion=v;offerReload();
}
function pageInUse(){
 const a=document.activeElement;
 return busy||document.body.hasAttribute('data-map-editing')||!!document.body.dataset.monitor||!!a?.matches?.('textarea,input:not([type=range]):not([type=checkbox]):not([type=radio])');
}
function offerReload(){
 // Guard against a reload loop (an agent flipping between versions): one automatic reload a minute at most.
 let last=0;try{last=Number(sessionStorage.getItem('agent-reload-at'))||0;}catch{}
 if(!pageInUse()&&Date.now()-last>60000){try{sessionStorage.setItem('agent-reload-at',String(Date.now()));}catch{}location.reload();return;}
 $('update-banner').querySelector('span').textContent='Вышла новая версия агента '+newAgentVersion;$('update-banner').hidden=false;
}
$('update-reload').onclick=()=>location.reload();
async function api(method,params=null){
 const read=isRead(method),requestID=read?null:Array.from(crypto.getRandomValues(new Uint8Array(16)),x=>x.toString(16).padStart(2,'0')).join('');
 for(let attempt=0;attempt<(read?2:1);attempt++){
  const controller=new AbortController(),timeout=setTimeout(()=>controller.abort('timeout'),read?15000:40000);
  if(read)activeReads.add(controller);
  try{
   // "/api/v1/..." methods are agent paths (GET without params); everything else is a /rpc method.
   const v1=method.startsWith('/api/v1/'),post=!v1||params!==null;
   const response=await fetch(v1?method:'/rpc',{method:post?'POST':'GET',headers:{...(post?{'Content-Type':'application/json'}:{}),'Authorization':'Bearer '+token,...(requestID?{'X-Dreame-Request-ID':requestID}:{})},body:post?JSON.stringify(v1?params:{method,params}):undefined,credentials:'omit',cache:'no-store',signal:controller.signal});noteAgentVersion(response);
   const body=await response.json();if(!response.ok){const error=Error(body.error||'Ошибка HTTP '+response.status);error.status=response.status;throw error;}return body.result;
  }catch(e){
   if(controller.signal.reason==='suspend'||controller.signal.reason==='logout'){const error=Error(SUSPENDED);error.suspended=true;throw error;}
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
 $('clean-zone').disabled=disabled||!zones.length;
 $('draw-zone').disabled=disabled||zones.length>=5;
 $('save-furniture').disabled=disabled||!furnitureDraft;$('cancel-furniture').disabled=!furnitureDraft;
 $('save-restrict').disabled=disabled||!restrictDraft||!view?.restrictions_editable;
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
async function loadFirmware(){renderFirmware(await api('/api/v1/system/firmware'));$('video-cloud').checked=(await api('/api/v1/system/video_cloud')).enabled;}
$('video-cloud').onchange=async()=>{try{$('video-cloud').checked=(await api('/api/v1/system/video_cloud',{enabled:$('video-cloud').checked})).enabled;toast($('video-cloud').checked?'Видео не уйдёт в облако, пока едешь из агента':'Защита видео выключена');}catch(e){toast(e.message,true);$('video-cloud').checked=!$('video-cloud').checked;}};
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
async function loadEmbed(){$('embed-origins').value=((await api('/api/v1/system/embed')).origins||[]).join('\n');}
$('save-embed').onclick=async()=>{
 const origins=$('embed-origins').value.split('\n').map(s=>s.trim()).filter(Boolean);
 try{const r=await api('/api/v1/system/embed',{origins});$('embed-origins').value=r.origins.join('\n');$('embed-status').textContent=r.origins.length?'Сохранено. Страницу можно встроить с этих адресов.':'Сохранено. Встраивание запрещено.';}
 catch(e){$('embed-status').textContent=e.message;}
};
// ADB: the allowed keys and a switch until the next reboot; the last key cannot be removed.
function renderADB(s){
 $('adb-status').textContent=s.running?(s.auth===false?'ADB включён без проверки ключа!':'ADB включён, вход только по ключу.'):'ADB выключен до перезагрузки робота.';
 $('adb-toggle').textContent=s.running?'Выключить до перезагрузки':'Включить';$('adb-toggle').dataset.on=s.running?'1':'';
 $('adb-keys').replaceChildren();$('adb-add').hidden=!s.editable;
 for(const k of s.keys){
  const row=document.createElement('div'),label=document.createElement('span');row.className='module-row';label.textContent=(k.comment||'без подписи')+' · '+k.id;row.append(label);
  if(s.editable&&s.keys.length>1){const remove=document.createElement('button');remove.textContent='Удалить';remove.className='ghost';
   remove.onclick=async()=>{if(!window.confirm('Удалить ключ '+(k.comment||k.id)+'? Это устройство больше не сможет зайти по ADB.'))return;
    try{const response=await fetch('/api/v1/system/adb/keys/'+encodeURIComponent(k.id),{method:'DELETE',headers:{'Authorization':'Bearer '+token},credentials:'omit',cache:'no-store'});
     const body=await response.json();if(!response.ok)throw Error(body.error||'Ошибка HTTP '+response.status);renderADB(body.result);$('adb-result').textContent='Ключ удалён.';}catch(e){toast(e.message,true);}};
   row.append(remove);}
  $('adb-keys').append(row);
 }
 if(!s.keys.length)$('adb-keys').textContent=s.keys_error||'Ключей нет.';
}
async function loadADB(){renderADB(await api('/api/v1/system/adb'));}
$('adb-toggle').onclick=async()=>{
 const on=!!$('adb-toggle').dataset.on;if(on&&!window.confirm('Выключить ADB? Он включится снова после перезагрузки робота или здесь.'))return;
 $('adb-toggle').disabled=true;try{renderADB(await api('/api/v1/system/adb',{enabled:!on}));$('adb-result').textContent='';}catch(e){$('adb-result').textContent=e.message;}finally{$('adb-toggle').disabled=false;}
};
$('adb-add-key').onclick=async()=>{
 try{renderADB(await api('/api/v1/system/adb/keys',{key:$('adb-key').value.trim()}));$('adb-key').value='';$('adb-add').open=false;$('adb-result').textContent='Ключ добавлен.';}
 catch(e){$('adb-result').textContent=e.status===400?'Не подходит: '+e.message:e.message;}
};
async function loadTokens(){
 await loadEmbed().catch(()=>{});loadADB().catch(e=>{$('adb-status').textContent=e.message;});
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
// The password is written on the robot first; the panel switches to it only when the agent saved it.
$('password-form').onsubmit=async e=>{
 e.preventDefault();const current=$('password-current').value,next=$('password-new').value;
 if(next!==$('password-repeat').value){$('password-result').textContent='Новый пароль и повтор не совпадают.';return;}
 try{await api('/api/v1/system/password',{current,new:next});
  token=next;sessionStorage.setItem('dreame-token',next);if(localStorage.getItem('dreame-token'))localStorage.setItem('dreame-token',next);
  $('password-form').reset();$('password-result').textContent='Пароль сменён. На других устройствах войди с новым.';}
 catch(e){$('password-result').textContent=e.status===403?'Текущий пароль неверный.':e.status===400?'Пароль: от 8 до 128 символов, латиница, цифры и знаки, без пробелов по краям.':e.message;}
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
 if(captiveSetup&&!token){token=$('token').value.trim();if(token.length<8){$('wifi-result').textContent='Введи пароль агента в поле выше.';return;}}
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
  $('status').textContent=s.dust_status===1?'Автоочистка':statusNames[s.status]||(s.status>1510&&s.status<1600?'Самопроверка: проверка '+(s.status-1510):'Статус '+(s.status??'недоступен'));
  $('battery').textContent=s.battery===undefined?'—':s.battery+'%';$('battery').style.setProperty('--charge',(s.battery??0)+'%');
  $('stat-area').textContent=Number.isFinite(s.cleaned_area)?String(s.cleaned_area):'—';$('stat-time').textContent=Number.isFinite(s.cleaning_time)?String(s.cleaning_time):'—';document.body.dataset.activity=s.activity||'';
  $('updated').textContent='Обновлено '+new Date(snap.updated_at).toLocaleTimeString();
  if(!volumeDirty&&document.activeElement!==$('volume')&&s.volume!==undefined){$('volume').value=s.volume;$('volume-value').textContent=s.volume+'%';}
  if(!fanPending&&Number.isInteger(s.suction))$('room-fan').value=s.suction; // one suction: the robot's own value
  if(!roomOptionsDirty){if(Number.isInteger(s.water)&&s.water>=1&&s.water<=3)$('room-water').value=s.water;}
  syncChips();
  $('station').textContent=s.station===undefined?'Состояние станции недоступно':'Состояние станции: '+(stationNames[s.station]||s.station)+(s.tank_drain===1?' · сливается бак робота':'')+(s.water_drain===1?' · сливается вода станции':'');
  $('diagnostics').textContent='Регион: '+(s.region??'—')+' · код доступа: '+(s.region_code??'—')+' · ошибка робота: '+(s.error??'—')+(s.error_name?' ('+s.error_name+')':'');
  $('robot-error').hidden=!s.error;$('robot-error').textContent=s.error?'Ошибка '+s.error+': '+errorText(s):'';
  renderCare(s);
  const look=[s.activity,s.station,s.charging,s.battery===100,s.dust_status,s.dust_bag_dry,s.hot_water,s.water_temperature].join();if(look!==robotLook){robotLook=look;robotState=s;if(view&&!view.empty)draw();}robotState=s;
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
 const w=view.width*3,h=view.height*3,r=Math.round(150*3/view.grid_size),count=new Uint8Array(w*h),last=new Float64Array(w*h);let travelled=0;
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
 placeMarker(pos,r);placeStation(charger?px(...charger):null,r);
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
// The station bubble of the app (plugin renderWashAnim): above the dock, a little to the left, looping.
// Dust emptying wins over drying, drying over washing; a paused wash shows the fan standing still.
function stationJob(s){
 if(s.dust_status===1)return 'dust';if(s.dust_bag_dry===1)return 'bagdry';if(s.station===2)return 'dry';
 if(s.station===4)return 'wash paused';if(s.station===1||s.station===5)return 'wash';return '';
}
const stationArt={
 wash:'<svg viewBox="0 0 48 48"><circle cx="24" cy="24" r="22" fill="#fff"/><g class="fan"><ellipse cx="24" cy="14.5" rx="5.2" ry="8.5"/><ellipse cx="24" cy="14.5" rx="5.2" ry="8.5" transform="rotate(120 24 24)"/><ellipse cx="24" cy="14.5" rx="5.2" ry="8.5" transform="rotate(240 24 24)"/></g><circle cx="24" cy="24" r="3.2" fill="#fff"/></svg>',
 dry:'<svg viewBox="0 0 48 48"><circle cx="24" cy="24" r="22" fill="#fff"/><g class="waves" fill="none" stroke-width="2.6" stroke-linecap="round"><path d="M15 26c-3-3 3-5 0-8s3-5 0-8"/><path d="M24 26c-3-3 3-5 0-8s3-5 0-8"/><path d="M33 26c-3-3 3-5 0-8s3-5 0-8"/></g><rect x="8" y="30" width="32" height="9" rx="4.5" fill="#ff9a62"/></svg>',
 dust:'<svg viewBox="0 0 48 48"><circle cx="24" cy="24" r="22" fill="#fff"/><g class="bubbles" fill="#fff" stroke="#8f7bf2" stroke-width="1.2"><circle cx="18" cy="16" r="3"/><circle cx="27" cy="12" r="2.2"/><circle cx="31" cy="19" r="2.6"/></g><path d="M13 22h22l-2.5 19a3 3 0 0 1-3 2.6H18.5a3 3 0 0 1-3-2.6z" fill="#8f7bf2"/><rect x="11" y="19" width="26" height="4" rx="2" fill="#7563e0"/></svg>',
};
stationArt.bagdry=stationArt.dry;
function placeStation(dock,r){
 const el=$('station-bubble'),s=robotState||{},job=dock?stationJob(s):'';
 el.hidden=!job;if(!job)return;
 const kind=job.split(' ')[0];if(el.dataset.kind!==kind){el.innerHTML=stationArt[kind];el.dataset.kind=kind;}
 const hot=s.hot_water===1||s.water_temperature>0;el.className='station-bubble '+job+(hot?' hot':'');
 // Plugin sizes: w = 1.2 robot widths, box 1.5w, left = dock.x - 0.7w, top = dock.y - 2.2w.
 const k=canvas.clientWidth/(view.width*3),w=Math.min(32,Math.max(18,2*r*1.2*k)); // kept readable at any zoom
 el.style.width=el.style.height=(1.5*w).toFixed(1)+'px';
 el.style.left=`calc(${dock[0]/(view.width*3)*100}% - ${(0.7*w).toFixed(1)}px)`;el.style.top=`calc(${dock[1]/(view.height*3)*100}% - ${(2.2*w).toFixed(1)}px)`;
 el.title={dust:'Автоочистка',bagdry:'Сушка мешка для пыли',dry:'Сушка швабр',wash:'Мойка швабр'}[kind]+(job.includes('paused')?' (пауза)':'');
}
// One small DOM element over the robot animated by CSS: no canvas repaint per frame.
let markerArgs=null;
function placeMarker(pos,r){
 markerArgs=[pos,r];
 const marker=$('map-marker'),s=robotState||{},mode=!pos?'':s.activity==='cleaning'?'pulse':s.activity==='returning'?'pulse':stationJob(s)?'':s.charging===1&&s.battery<100?'charge':s.activity==='docked'?'sleep':'';
 marker.hidden=!mode;if(!mode)return;
 marker.className='map-marker '+mode;marker.style.left=(pos[0]/(view.width*3)*100)+'%';marker.style.top=(pos[1]/(view.height*3)*100)+'%';
 marker.style.setProperty('--r',(r*canvas.clientWidth/(view.width*3)).toFixed(1)+'px'); // robot radius in page pixels
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
// ---- Pet photos: the robot's pet GIFs the agent keeps and pet obstacle photos of runs, newest first.
// They need the token, so they come through fetch as blobs; tiles load when scrolled into view.
let petList=[],petIndex=-1,petBlobs=new Map(),petSwipe=null;
const petObserver=window.IntersectionObserver?new IntersectionObserver(entries=>{for(const e of entries)if(e.isIntersecting){petObserver.unobserve(e.target);e.target.load();}},{rootMargin:'200px'}):{observe:b=>b.load(),unobserve(){}};
function petBlob(p){
 if(!petBlobs.has(p.url))petBlobs.set(p.url,fetch(p.url,{headers:{'Authorization':'Bearer '+token},credentials:'omit',cache:'no-store'}).then(async r=>{if(!r.ok)throw Error('Фото не загрузилось');return URL.createObjectURL(await r.blob());}).catch(e=>{petBlobs.delete(p.url);throw e;})); // a failed one is fetched again next time
 return petBlobs.get(p.url);
}
const petCaption=p=>new Date(p.at).toLocaleString([], {dateStyle:'short',timeStyle:'short'})+(p.source==='obstacle'?' · уборка'+(p.confidence?' · '+p.confidence+'%':''):'');
async function loadPets(){
 const list=await api('/api/v1/pets/photos'),old=petBlobs;
 petList=list;petBlobs=new Map();for(const u of old.values())u.then(URL.revokeObjectURL,()=>{});
 const tiles=petList.map((p,i)=>{
  const b=document.createElement('button'),label=document.createElement('span');b.type='button';b.dataset.index=i;b.setAttribute('aria-label','Открыть фото: '+petCaption(p));label.textContent=petCaption(p);b.append(label);
  b.load=()=>petBlob(p).then(src=>{const img=document.createElement('img');img.src=src;img.alt='';b.prepend(img);},()=>label.textContent+=' · не загрузилось');
  b.onclick=()=>openPet(i);petObserver.observe(b);return b;
 });
 if(!tiles.length)tiles.push(Object.assign(document.createElement('p'),{className:'muted small empty',textContent:'Фото пока нет. Робот снимает питомцев во время уборки, когда включены «Распознавание животных» и «Фото домашних животных».'}));
 $('pet-photos').replaceChildren(...tiles);$('pets-count').textContent=petList.length?'Всего: '+petList.length:'';
}
function showPet(){
 const p=petList[petIndex];if(!p){closePet();return;}
 $('pet-viewer-caption').textContent=petCaption(p)+' · '+(petIndex+1)+' из '+petList.length;$('pet-viewer-img').removeAttribute('src');$('pet-viewer-img').alt='Фото питомца, '+petCaption(p);
 petBlob(p).then(src=>{if(petList[petIndex]===p)$('pet-viewer-img').src=src;},e=>toast(e.message,true));
 $('pet-prev').disabled=petIndex<=0;$('pet-next').disabled=petIndex>=petList.length-1;
}
function openPet(i){petIndex=i;$('pet-viewer').hidden=false;showPet();$('pet-viewer-close').focus({preventScroll:true});}
function closePet(){
 if($('pet-viewer').hidden)return;$('pet-viewer').hidden=true;$('pet-viewer-img').removeAttribute('src');
 const tile=$('pet-photos').querySelector('[data-index="'+petIndex+'"]');petIndex=-1;tile?.focus({preventScroll:true});
}
const stepPet=d=>{const i=petIndex+d;if(i>=0&&i<petList.length){petIndex=i;showPet();}};
$('pet-prev').onclick=()=>stepPet(-1);$('pet-next').onclick=()=>stepPet(1);$('pet-viewer-close').onclick=closePet;
$('pet-viewer').addEventListener('pointerdown',e=>{petSwipe=e.clientX;});
$('pet-viewer').addEventListener('pointerup',e=>{if(petSwipe===null)return;const dx=e.clientX-petSwipe;petSwipe=null;if(Math.abs(dx)>60)stepPet(dx>0?-1:1);});
document.addEventListener('keydown',e=>{
 if($('pet-viewer').hidden)return;
 if(e.key==='Escape'){e.stopImmediatePropagation();closePet();}else if(e.key==='ArrowLeft')stepPet(-1);else if(e.key==='ArrowRight')stepPet(1);
},true);
$('pet-delete').onclick=async()=>{
 const p=petList[petIndex];if(!p||!window.confirm('Удалить это фото? Вернуть его не получится.'))return;
 try{await api('/api/v1/pets/delete',{name:p.name});toast('Фото удалено');const at=petIndex;await loadPets();if(!petList.length)closePet();else{petIndex=Math.min(at,petList.length-1);showPet();}}
 catch(e){toast(e.status===403?'Удалять фото может только токен с правом управления':e.message,true);}
};
// Signing out drops the photos from the page.
function clearPets(){closePet();for(const u of petBlobs.values())u.then(URL.revokeObjectURL,()=>{});petBlobs=new Map();petList=[];$('pet-photos').replaceChildren();$('pets-count').textContent='';}
$('refresh-pets').onclick=()=>loadPets().catch(e=>toast(e.message,true));
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
   const response=await fetch('/api/v1/events',{headers:{'Authorization':'Bearer '+token},credentials:'omit',cache:'no-store',signal:controller.signal});noteAgentVersion(response);
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
const roomsShown=()=>dock.dataset.mode==='room'||editTool==='split';
function selectRoom(id){
 if(editTool==='split'&&$('map-operation').value==='split')selected=new Set(selected.has(id)?[]:[id]);
 else if(selected.has(id))selected.delete(id);else{selected.add(id);if(!openSheet&&dock.dataset.mode==='all')setMode('room');}
 points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);
}
function renderRoomButtons(){
 document.body.toggleAttribute('data-selected',selected.size>0);
 $('rooms').replaceChildren();$('room-selection').textContent=selected.size?'Выбрано: '+(view?.rooms||[]).filter(r=>selected.has(r.id)).map(r=>r.name).join(', '):'Выбери комнаты на карте или кнопками ниже.';
 for(const room of view?.rooms||[]){const b=document.createElement('button');b.textContent=room.name+' · '+room.num;b.style.setProperty('--room',palette[(room.id-1)%palette.length]);b.classList.toggle('selected',selected.has(room.id));b.setAttribute('aria-pressed',String(selected.has(room.id)));b.onclick=()=>selectRoom(room.id);$('rooms').append(b);}
}
// Rooms with their floor pattern at canvas size, rebuilt only when the map, the selection or a floor draft changes.
// Pattern sizes in map cells as the app's buildFloorMap: tiles 12, planks 4 × 16 laid in a half-board offset.
const roomIcons={1:'🛋',2:'🛏',15:'🛏',3:'📚',4:'🍲',5:'🍽',6:'🛁',7:'🌿',8:'👣',9:'📦',10:'👔',11:'🚪',12:'💼',13:'🏋',14:'☕'};
let baseCache={key:'',pending:'',layers:null,url:''};
function floorOf(room){const draft=$('room-floors').querySelector(`[data-room="${room.id}"]`)?.value;return draft===undefined?room.floor||0:Number(draft);}
// The base layer (floors, walls, room numbers) and the saved furniture, restrictions and thresholds are one SVG drawn
// by the agent, the same picture as in Home Assistant. It sits under the canvas as an <img>, so the browser
// redraws it sharp at every zoom and nothing is fetched again. The panel draws a layer itself only while it has a
// draft of it. The previous picture stays until the new one arrives.
const draftOf={restrictions:()=>restrictDraft,thresholds:()=>thresholdDraft?.line,furniture:()=>furnitureDraft};
const agentDraws=g=>baseCache.layers?.has(g)&&!draftOf[g]?.();
function baseLayer(){
 const floors=view.rooms.map(r=>r.id+':'+floorOf(r)).join(','),sel=roomsShown()?[...selected].join(','):'',layers=['furniture','restrictions','thresholds'].filter(g=>!draftOf[g]?.()),
  key=view.revision+'|'+sel+'|'+floors+'|'+layers;
 if(baseCache.key!==key&&baseCache.pending!==key){
  baseCache.pending=key;
  fetch('/api/v1/map.svg?base=1&selected='+sel+'&floors='+floors+'&layers='+layers,{headers:{'Authorization':'Bearer '+token},credentials:'omit',cache:'no-store'})
   .then(r=>r.ok?r.blob():Promise.reject(Error('map.svg '+r.status)))
   .then(blob=>{if(baseCache.pending!==key)return;const url=URL.createObjectURL(blob);$('map-base').src=url;if(baseCache.url)URL.revokeObjectURL(baseCache.url);baseCache={key,pending:'',layers:new Set(layers),url};draw();},()=>{if(baseCache.pending===key)baseCache.pending='';});
 }
}
// The whole map fits the window at 100%; the buttons zoom from there and only a zoomed map scrolls inside its frame.
let zoom=1,res=0; // res: canvas pixels per 3 drawing units; the canvas gets sharper while zoomed in
function layoutMap(){
 const stage=document.querySelector('.map-stage'),w=stage.clientWidth-16,h=stage.clientHeight-16;
 if(w<=0||h<=0)return; // hidden page: laid out again when the stage gets a size
 const fit=Math.max(1,Math.min(w,h*canvas.width/canvas.height)); // the stage is sized by the page layout, the same at every zoom
 stage.querySelector('.map-wrap').style.width=Math.round(fit*zoom)+'px';
 if(view&&!view.empty){const want=Math.max(1,Math.min(4,Math.floor(4096/(view.width*3)),Math.ceil(fit*zoom*(devicePixelRatio||1)/(view.width*3)))),mine=res;
  if(want!==mine){res=want;canvas.width=view.width*3*res;canvas.height=view.height*3*res;if(mine)draw();}}
 $('zoom-fit').textContent=Math.round(zoom*100)+'%';if(markerArgs)placeMarker(...markerArgs);
}
const setZoom=z=>{zoom=Math.min(6,Math.max(.4,z));draw();}; // draw: room labels keep a readable size
// A sheet or an editor changes the stage around the map; the map keeps its size and place on the screen.
function keepMap(change){
 const before=view&&!view.empty?canvas.getBoundingClientRect():null;change();
 if(!before||!before.width)return;
 const stage=document.querySelector('.map-stage'),w=stage.clientWidth-16,h=stage.clientHeight-16;if(w<=0||h<=0)return;
 const fit=Math.max(1,Math.min(w,h*canvas.width/canvas.height));zoom=Math.min(6,Math.max(.4,before.width/fit));layoutMap();
 const after=canvas.getBoundingClientRect();panX+=before.left-after.left;panY+=before.top-after.top;applyPan();
}
$('zoom-in').onclick=()=>setZoom(zoom*1.25);$('zoom-out').onclick=()=>setZoom(zoom/1.25);$('zoom-fit').onclick=()=>setZoom(1);
addEventListener('resize',layoutMap);
// Wheel and pinch zoom around the pointer; dragging (mouse or finger) moves the map freely, even past the frame edge.
const mapStage=document.querySelector('.map-stage'),mapWrap=mapStage.querySelector('.map-wrap'),touches=new Map();
let panX=0,panY=0;
const applyPan=()=>{mapWrap.style.transform=`translate(${panX}px,${panY}px)`;};
function zoomAt(z,cx,cy){
 const r=canvas.getBoundingClientRect(),fx=(cx-r.left)/r.width,fy=(cy-r.top)/r.height;
 setZoom(z);const n=canvas.getBoundingClientRect();
 panX+=cx-(n.left+fx*n.width);panY+=cy-(n.top+fy*n.height);applyPan();
}
$('zoom-fit').addEventListener('click',()=>{panX=panY=0;applyPan();});
mapStage.addEventListener('wheel',e=>{e.preventDefault();zoomAt(zoom*Math.exp(-e.deltaY*(e.deltaMode?.05:.0015)),e.clientX,e.clientY);},{passive:false});
let pan=null,pinch=0,panMoved=false;
const pinchDist=()=>{const [a,b]=[...touches.values()];return Math.hypot(a.x-b.x,a.y-b.y);};
mapStage.addEventListener('pointerdown',e=>{
 if(e.pointerType==='touch'){touches.set(e.pointerId,{x:e.clientX,y:e.clientY});if(touches.size===2){pinch=pinchDist();pan=null;return;}}
 else if(e.button!==0)return;
 pan={x:e.clientX,y:e.clientY,px:panX,py:panY,moved:false};
});
mapStage.addEventListener('pointermove',e=>{
 if(touches.has(e.pointerId)){
  touches.set(e.pointerId,{x:e.clientX,y:e.clientY});
  if(touches.size===2&&pinch){const [a,b]=[...touches.values()],d=pinchDist();zoomAt(zoom*d/pinch,(a.x+b.x)/2,(a.y+b.y)/2);pinch=d;return;}
 }
 if(!pan||touches.size>1||(e.pointerType!=='touch'&&!(e.buttons&1)))return;
 const dx=e.clientX-pan.x,dy=e.clientY-pan.y;
 if(!pan.moved&&Math.hypot(dx,dy)>6){pan.moved=true;mapStage.classList.add('panning');mapStage.setPointerCapture?.(e.pointerId);}
 if(pan.moved){panX=pan.px+dx;panY=pan.py+dy;applyPan();}
});
const endPointer=e=>{touches.delete(e.pointerId);if(touches.size<2)pinch=0;panMoved=!!pan?.moved;if(panMoved)setTimeout(()=>{panMoved=false;},0);pan=null;mapStage.classList.remove('panning');};
mapStage.addEventListener('pointerup',endPointer);mapStage.addEventListener('pointercancel',endPointer);
mapStage.addEventListener('click',e=>{if(panMoved){e.stopPropagation();e.preventDefault();panMoved=false;}},true); // a drag is not a tap on a room
// Room names and order badges are page text over the map, not canvas pixels, so they stay sharp at any zoom.
// Size: the agent's label_size (panel → Агент), or 10–14 px following the zoom when it is 0.
let labelSize=0;
async function loadLook(){try{labelSize=Number((await api('/api/v1/ui')).label_size)||0;}catch{labelSize=0;}const o=$('label-size');if(![...o.options].some(x=>Number(x.value)===labelSize))o.add(new Option(labelSize+' px',String(labelSize)));o.value=String(labelSize);}
function renderLabels(order){
 const box=$('map-labels'),cw=canvas.getBoundingClientRect().width,perUnit=cw?cw/(3*view.width):1;
 box.style.fontSize=(labelSize||Math.min(14,Math.max(10,11*perUnit)))+'px';
 const items=[];
 for(const room of view.rooms){
  const at=`left:${(room.x+.5)/view.width*100}%;top:${(view.height-room.y-.5)/view.height*100}%`;
  const name=document.createElement('span');name.className='room-label';name.style.cssText=at;name.textContent=(roomIcons[room.type]?roomIcons[room.type]+' ':'')+room.name;
  name.classList.toggle('dim',!!(roomsShown()&&selected.size&&!selected.has(room.id)));items.push(name);
  const i=order.indexOf(room.id);
  if(i>=0){const b=document.createElement('span');b.className='room-badge';b.style.cssText=at+';background:'+palette[(room.id-1)%palette.length];b.textContent=String(i+1);items.push(b);}
 }
 box.replaceChildren(...items);
}
function draw(){
 layoutMap();
 ctx.setTransform(1,0,0,1,0,0);ctx.clearRect(0,0,canvas.width,canvas.height);if(!view||view.empty){$('map-labels').replaceChildren();return;}ctx.setTransform(res,0,0,res,0,0);
 const scale=3;baseLayer();
 // Order badges as the app: the selection order in the room mode, the saved cleaning order in «Все».
 const order=dock.dataset.mode==='room'&&!editTool?[...selected]:dock.dataset.mode==='all'&&!editTool&&!openSheet?roomOrder.map(r=>r.id):[];
 renderLabels(order);
 drawThresholds();drawRestrictions();drawFurniture();drawPatrol();drawLive();
 if(points.length){ctx.strokeStyle='#ffae67';ctx.fillStyle='#ffae67';ctx.lineWidth=2;for(const p of points){ctx.beginPath();ctx.arc(p.x*scale,(view.height-p.y)*scale,4,0,Math.PI*2);ctx.fill();}if(points.length===2){ctx.beginPath();ctx.moveTo(points[0].x*scale,(view.height-points[0].y)*scale);ctx.lineTo(points[1].x*scale,(view.height-points[1].y)*scale);ctx.stroke();}}
 drawEditor();
}
async function refreshMap(){
 if(!token||busy)return;const epoch=viewEpoch;
 try{
  const next=await api('local.map_view');if(epoch!==viewEpoch||!token)return;
  if(next.empty){view=next;selected.clear();points=[];thresholdDraft=null;thresholdDrawing=false;zones=[];zoneSel=-1;restrictDraft=null;restrictSel=null;furnitureDraft=null;invalidateEdit();$('map-info').textContent='Сохранённая карта ещё не создана';renderRoomButtons();renderThresholdChoices();renderRestrictions();renderCarpetTypes();renderRoomSettings();renderRoomTypes();renderObstacles();draw();disableControls(busy);return;}
  if(!Number.isInteger(next.width)||!Number.isInteger(next.height)||next.width*next.height>4194304||!Array.isArray(next.rooms))throw Error('Некорректный формат карты');
  const bytes=atob(next.pixels);if(bytes.length!==next.width*next.height)throw Error('Некорректный размер карты');
  next.grid=Uint8Array.from(bytes,c=>c.charCodeAt(0));
  if(view?.revision!==next.revision){selected=view?.map_id===next.map_id?new Set([...selected].filter(id=>next.rooms.some(r=>r.id===id))):new Set();points=[];invalidateEdit();thresholdDraft=null;thresholdDrawing=false;zones=[];zoneSel=-1;restrictDraft=null;restrictSel=null;furnitureDraft=null;}
  view=next;res=0;
  $('map-info').textContent='Карта '+view.map_id+' · комнат: '+view.rooms.length+' · '+view.width+' × '+view.height;
  renderRoomButtons();renderThresholdChoices();renderRestrictions();renderCarpetTypes();renderRoomSettings();renderRoomTypes();renderObstacles();draw();disableControls(busy);
 }catch(e){if(!e.suspended&&epoch===viewEpoch){$('map-info').textContent=e.message;connectionLost(e.message);}}
}
function mapPoint(e){const rect=canvas.getBoundingClientRect();return {x:(e.clientX-rect.left)*view.width/rect.width,y:view.height-(e.clientY-rect.top)*view.height/rect.height};}
// ---- Shapes on the map, edited as in the app. A tap selects a shape and a drag moves it; the handles at its
// corners delete it (top left), turn it (top right, furniture), resize it (bottom right) and move it (bottom left).
// Lines move by their ends. Geometry is in map cells with y up: rect {cx,cy,w,h,a} (a counterclockwise, radians),
// line {x0,y0,x1,y1}, point {x,y}. Each editor gives its shapes with get/set into its own data or draft.
const cellsPerPx=()=>view.width/canvas.getBoundingClientRect().width;
const mm=v=>v/view.grid_size;
const turn=(x,y,a)=>[x*Math.cos(a)-y*Math.sin(a),x*Math.sin(a)+y*Math.cos(a)];
const rectCorner=(g,sx,sy)=>{const [x,y]=turn(sx*g.w/2,sy*g.h/2,g.a||0);return [g.cx+x,g.cy+y];};
const toRobotRect=g=>{const [x0,y0]=pixelToRobot(view,g.cx-g.w/2,g.cy-g.h/2),[x1,y1]=pixelToRobot(view,g.cx+g.w/2,g.cy+g.h/2);return [x0,y0,x1,y1];};
const fromRobotRect=r=>{const [ax,ay]=robotToPixel(view,r[0],r[1]),[bx,by]=robotToPixel(view,r[2],r[3]);return {cx:(ax+bx)/2,cy:(ay+by)/2,w:Math.abs(bx-ax),h:Math.abs(by-ay)};};
const toRobotLine=g=>[...pixelToRobot(view,g.x0,g.y0),...pixelToRobot(view,g.x1,g.y1)];
const fromRobotLine=r=>{const [x0,y0]=robotToPixel(view,r[0],r[1]),[x1,y1]=robotToPixel(view,r[2],r[3]);return {x0,y0,x1,y1};};
export function handlesOf(s,k){
 const g=s.geom(),out=[];
 if(s.kind==='rect'){if(s.remove)out.push(['delete',...rectCorner(g,-1,1)]);if(s.rotatable)out.push(['rotate',...rectCorner(g,1,1)]);out.push(['resize',...rectCorner(g,1,-1)],['move',...rectCorner(g,-1,-1)]);}
 else if(s.kind==='line'){const dx=g.x1-g.x0,dy=g.y1-g.y0,l=Math.hypot(dx,dy)||1;out.push(['p0',g.x0,g.y0],['p1',g.x1,g.y1]);if(s.remove)out.push(['delete',(g.x0+g.x1)/2-dy/l*26*k,(g.y0+g.y1)/2+dx/l*26*k]);}
 else if(s.remove)out.push(['delete',g.x-20*k,g.y+20*k]);
 return out;
}
function onShape(s,x,y,tol){
 const g=s.geom();
 if(s.kind==='rect'){const [lx,ly]=turn(x-g.cx,y-g.cy,-(g.a||0));return Math.abs(lx)<=g.w/2+tol&&Math.abs(ly)<=g.h/2+tol;}
 if(s.kind==='line'){const dx=g.x1-g.x0,dy=g.y1-g.y0,l=dx*dx+dy*dy,t=l?Math.max(0,Math.min(1,((x-g.x0)*dx+(y-g.y0)*dy)/l)):0;return Math.hypot(x-g.x0-t*dx,y-g.y0-t*dy)<=tol;}
 return Math.hypot(x-g.x,y-g.y)<=tol;
}
export function hitShape(shapes,x,y,k){
 const sel=shapes.find(s=>s.selected);
 if(sel)for(const [part,hx,hy] of handlesOf(sel,k))if(Math.hypot(x-hx,y-hy)<=18*k)return {s:sel,part};
 for(const s of [...(sel?[sel]:[]),...shapes.filter(s=>!s.selected).reverse()])if(onShape(s,x,y,(s.kind==='rect'?4:s.kind==='line'?12:16)*k))return {s,part:'move'};
 return null;
}
// The new geometry for a drag of a handle (part) from geometry g; p is the pointer, (dx,dy) its way so far.
export function dragShape(kind,part,g,p,dx,dy,min=0){
 if(part==='move')return kind==='rect'?{...g,cx:g.cx+dx,cy:g.cy+dy}:kind==='line'?{x0:g.x0+dx,y0:g.y0+dy,x1:g.x1+dx,y1:g.y1+dy}:{x:g.x+dx,y:g.y+dy};
 if(part==='p0')return {...g,x0:p.x,y0:p.y};
 if(part==='p1')return {...g,x1:p.x,y1:p.y};
 const a=g.a||0;
 if(part==='rotate'){let deg=Math.round((Math.atan2(p.y-g.cy,p.x-g.cx)-Math.atan2(g.h,g.w))*180/Math.PI);deg=((deg%360)+360)%360;const snap=Math.round(deg/90)*90;if(Math.abs(deg-snap)<=4)deg=snap%360;return {...g,a:deg*Math.PI/180};}
 const [fx,fy]=rectCorner(g,-1,1),[vx,vy]=turn(p.x-fx,p.y-fy,-a),w=Math.max(min,vx),h=Math.max(min,-vy),[ox,oy]=turn(w/2,-h/2,a); // the top left corner stays
 return {...g,w,h,cx:fx+ox,cy:fy+oy};
}
// Where a new shape goes: the middle of the part of the map that is on screen and not under a sheet.
function visibleCenter(){
 const r=canvas.getBoundingClientRect(),s=mapStage.getBoundingClientRect(),sh=openSheet?.getBoundingClientRect();
 let bottom=Math.min(r.bottom,s.bottom),right=Math.min(r.right,s.right);
 if(sh&&sh.width){if(sh.left<=s.left+8&&sh.top>s.top)bottom=Math.min(bottom,sh.top);else if(sh.left>s.left)right=Math.min(right,sh.left);}
 const p=mapPoint({clientX:(Math.max(r.left,s.left)+right)/2,clientY:(Math.max(r.top,s.top)+bottom)/2});
 return {x:Math.min(view.width-2,Math.max(2,p.x)),y:Math.min(view.height-2,Math.max(2,p.y))};
}
const fitSize=v=>Math.min(mm(v),view.width*.4,view.height*.4);
function newRect(w,h,offset=0){const c=visibleCenter(),d=mm(300)*offset;return {cx:c.x+d,cy:c.y-d,w:fitSize(w),h:fitSize(h),a:0};}
function newLine(len){const c=visibleCenter(),l=fitSize(len)/2;return {x0:c.x-l,y0:c.y,x1:c.x+l,y1:c.y};}
// Zones for cleaning: up to 5, kept until the map changes.
const zoneStyle=['#3b82f6','rgba(59,130,246,.22)'];
function zoneShapes(){return zones.map((z,i)=>({kind:'rect',geom:()=>z,set:g=>Object.assign(z,g),selected:i===zoneSel,select:()=>{zoneSel=i;},remove:()=>{zones.splice(i,1);zoneSel=zones.length-1;zoneStatus();},done:zoneStatus,min:mm(300),
 draw:u=>{const [x0,y0]=toCanvasXY(z.cx-z.w/2,z.cy+z.h/2);ctx.fillStyle=zoneStyle[1];ctx.strokeStyle=zoneStyle[0];ctx.lineWidth=2;ctx.fillRect(x0,y0,z.w*3,z.h*3);ctx.strokeRect(x0,y0,z.w*3,z.h*3);
  const [cx,cy]=toCanvasXY(z.cx,z.cy);ctx.fillStyle=zoneStyle[0];ctx.beginPath();ctx.arc(cx,cy,9*u,0,Math.PI*2);ctx.fill();ctx.fillStyle='#fff';ctx.font=`600 ${11*u}px system-ui`;ctx.textAlign='center';ctx.textBaseline='middle';ctx.fillText(String(i+1),cx,cy);}}));}
const zoneRobot=z=>{const [x0,y0,x1,y1]=toRobotRect(z);return [x0,y1,x1,y0];};
function zoneStatus(){
 const z=zones[zoneSel];
 $('zone-status').textContent=zones.length?'Зон: '+zones.length+' из 5. Двигай зону пальцем, размер меняется за уголок ↘.'+(z?' Зона '+(zoneSel+1)+' для HA: '+JSON.stringify(zoneRobot(z)):''):'Зон нет: нажми «Добавить» справа на карте.';
 disableControls(busy);
}
// Virtual walls, no-go and no-mop zones, carpets and ramps: every change goes to the draft saved by one button.
const restrictEdit=()=>restrictDraft||(restrictDraft=structuredClone(restrictions()));
function restrictChanged(text){$('restrict-status').textContent=text+' Сохранить — ✓ вверху.';renderRestrictions();disableControls(busy);}
function restrictShapes(kinds){
 if(!view.restrictions_editable)return [];
 const lists=restrictions(),shapes=[];
 for(const kind of kinds)(lists[kind]||[]).forEach((r,i)=>{
  if(!Array.isArray(r)||r.length!==4)return;
  const line=kind==='walls';
  shapes.push({kind:line?'line':'rect',geom:()=>line?fromRobotLine(r):fromRobotRect(r),set:g=>{restrictEdit()[kind][i]=line?toRobotLine(g):toRobotRect(g);},min:mm(200),
   selected:restrictSel?.kind===kind&&restrictSel.index===i,select:()=>{restrictSel={kind,index:i};$('restrict-status').textContent=restrictKinds[kind]+' '+(i+1)+' выбрана.';},
   remove:()=>{restrictEdit()[kind].splice(i,1);restrictSel=null;restrictChanged(restrictKinds[kind]+' удалена из черновика.');},done:()=>restrictChanged(restrictKinds[kind]+' изменена в черновике.')});
 });
 return shapes;
}
// Thresholds: the chosen or new one is the draft line.
function thresholdShapes(){
 const chosen=$('threshold-choice').value,shapes=[];
 if(thresholdDraft)shapes.push({kind:'line',geom:()=>fromRobotLine(thresholdDraft.line),set:g=>{thresholdDraft={line:toRobotLine(g)};},selected:!restrictSel,select:()=>{restrictSel=null;},
  remove:()=>{if(chosen&&window.confirm('Удалить этот порог с карты робота?'))submitThreshold(true);else if(!chosen)$('cancel-threshold').click();},
  done:()=>{$('threshold-status').textContent='Порог изменён. Сохранить — ✓ вверху.';disableControls(busy);}});
 for(const item of thresholdLines())if(!(thresholdDraft&&chosen===item.kind+':'+item.index))
  shapes.push({kind:'line',geom:()=>fromRobotLine(item.line),set:g=>{thresholdDraft={line:toRobotLine(g)};},selected:false,select:()=>{restrictSel=null;$('threshold-choice').value=item.kind+':'+item.index;chooseThreshold();},
   done:()=>{$('threshold-status').textContent='Порог изменён. Сохранить — ✓ вверху.';disableControls(busy);}});
 return shapes;
}
// Furniture: the chosen or new item is a draft until «Сохранить»; an edit replaces the item.
const furnitureGeom=f=>{const [x,y]=robotToPixel(view,f.x,f.y);return {cx:x,cy:y,w:mm(f.width),h:mm(f.height),a:(f.rotation||0)*Math.PI/180};};
function setFurniture(g){
 const [x,y]=pixelToRobot(view,g.cx,g.cy),size=v=>Math.min(4000,Math.max(100,Math.round(v*view.grid_size/10)*10));
 furnitureDraft={...furnitureDraft,x,y,width:size(g.w),height:size(g.h),rotation:((Math.round(g.a*180/Math.PI)%360)+360)%360};
}
function furnitureStatus(text){$('furniture-status').textContent=text;disableControls(busy);}
function furnitureShapes(){
 const shapes=[],common={kind:'rect',rotatable:true,min:mm(100),set:setFurniture,done:()=>furnitureStatus('Предмет изменён. Нажми «Сохранить».')};
 if(furnitureDraft)shapes.push({...common,geom:()=>furnitureGeom(furnitureDraft),selected:true,select(){},remove:()=>{
  const d=furnitureDraft;furnitureDraft=null;if(d.index!=null&&window.confirm('Убрать «'+furnitureName(d)+'» с карты?'))saveFurniture({remove:[d.index],add:[]});else furnitureStatus('');}});
 for(const f of view.furniture||[])if(furnitureTypes[f.type]&&f.index!==furnitureDraft?.index)
  shapes.push({...common,geom:()=>furnitureGeom(f),selected:false,select:()=>{furnitureDraft={index:f.index,type:f.type,x:f.x,y:f.y,width:f.width,height:f.height,rotation:f.rotation||0};$('furniture-type').value=String(f.type);furnitureStatus('Выбран «'+furnitureName(f)+'». Двигай, поворачивай за ↻, размер — за ↘.');}});
 return shapes;
}
// Split line: shown across the chosen room as soon as one room is picked.
function splitShapes(){
 if($('map-operation').value!=='split'||selected.size!==1)return [];
 if(points.length!==2){const room=view.rooms.find(r=>selected.has(r.id));if(!room)return [];const c={x:room.x+.5,y:room.y+.5},l=fitSize(1500)/2;points=[{x:c.x-l,y:c.y},{x:c.x+l,y:c.y}].map(p=>({...p,robot:pixelToRobot(view,p.x,p.y)}));}
 return [{kind:'line',geom:()=>({x0:points[0].x,y0:points[0].y,x1:points[1].x,y1:points[1].y}),selected:true,select(){},
  set:g=>{points=[{x:g.x0,y:g.y0},{x:g.x1,y:g.y1}].map(p=>({...p,robot:pixelToRobot(view,p.x,p.y)}));invalidateEdit();}}];
}
// Camera trip points.
function patrolShapes(){return patrolPoints.map((p,i)=>({kind:'point',geom:()=>{const [x,y]=robotToPixel(view,p[0],p[1]);return {x,y};},set:g=>{patrolPoints[i]=pixelToRobot(view,g.x,g.y);},
 selected:i===patrolSel,select:()=>{patrolSel=i;},remove:()=>{patrolPoints.splice(i,1);patrolSel=-1;$('patrol-status').textContent='Точек: '+patrolPoints.length+'.';patrolButtons();}}));}
// The shapes the open screen edits: a block of the map editor, the camera sheet or the zone mode.
function editorShapes(){
 if(!view||view.empty)return null;
 if(editTool==='restrict')return restrictShapes(['walls','no_go_zones','no_mop_zones']);
 if(editTool==='carpet')return restrictShapes(carpetKinds);
 if(editTool==='threshold')return [...(view.thresholds_editable?thresholdShapes():[]),...restrictShapes(['ramps'])];
 if(editTool==='furniture')return view.furniture_editable?furnitureShapes():[];
 if(editTool==='split')return splitShapes();
 if(openSheet?.id==='sheet-camera')return patrolShapes();
 return !openSheet&&dock.dataset.mode==='zone'?zoneShapes():null;
}
const toCanvasXY=(x,y)=>[x*3,(view.height-y)*3];
function drawIcon(part,x,y,u){
 ctx.beginPath();const d=4*u,line=(a,b,c,e)=>{ctx.moveTo(x+a*u,y+b*u);ctx.lineTo(x+c*u,y+e*u);};
 if(part==='delete'){line(-4,-4,4,4);line(4,-4,-4,4);}
 else if(part==='resize'){line(-4,-4,4,4);line(4,4,4,0);line(4,4,0,4);line(-4,-4,-4,0);line(-4,-4,0,-4);}
 else if(part==='rotate'){ctx.arc(x,y,d,-.3*Math.PI,1.3*Math.PI);const ex=x+d*Math.cos(-.3*Math.PI),ey=y+d*Math.sin(-.3*Math.PI);ctx.moveTo(ex,ey);ctx.lineTo(ex-3.5*u,ey-.5*u);ctx.moveTo(ex,ey);ctx.lineTo(ex+.5*u,ey+3.5*u);}
 else if(part==='move'){line(-5,0,5,0);line(0,-5,0,5);for(const [a,b] of [[1,0],[-1,0],[0,1],[0,-1]]){line(5*a,5*b,5*a-2*(a+b),5*b-2*(b-a));line(5*a,5*b,5*a-2*(a-b),5*b-2*(b+a));}}
 else{ctx.arc(x,y,3*u,0,Math.PI*2);ctx.fill();}
 ctx.stroke();
}
function drawEditor(){
 const shapes=editorShapes();document.body.toggleAttribute('data-map-editing',!!(openSheet&&shapes));
 if(!shapes)return;
 const k=cellsPerPx(),u=3*k; // canvas units per screen pixel
 ctx.save();for(const s of shapes)s.draw?.(u);ctx.restore();
 const s=shapes.find(s=>s.selected);if(!s)return;
 const g=s.geom();ctx.save();ctx.lineWidth=1.5*u;ctx.strokeStyle='#fff';ctx.setLineDash([5*u,4*u]);ctx.beginPath();
 if(s.kind==='rect'){[[-1,1],[1,1],[1,-1],[-1,-1]].forEach(([a,b],i)=>{const [x,y]=toCanvasXY(...rectCorner(g,a,b));i?ctx.lineTo(x,y):ctx.moveTo(x,y);});ctx.closePath();}
 else if(s.kind==='line'){ctx.moveTo(...toCanvasXY(g.x0,g.y0));ctx.lineTo(...toCanvasXY(g.x1,g.y1));}
 else ctx.arc(...toCanvasXY(g.x,g.y),12*u,0,Math.PI*2);
 ctx.stroke();ctx.setLineDash([]);
 for(const [part,hx,hy] of handlesOf(s,k)){
  const [x,y]=toCanvasXY(hx,hy);ctx.fillStyle='#17232d';ctx.shadowColor='rgba(0,0,0,.35)';ctx.shadowBlur=4*u;ctx.beginPath();ctx.arc(x,y,11*u,0,Math.PI*2);ctx.fill();ctx.shadowBlur=0;
  ctx.strokeStyle='#fff';ctx.fillStyle='#fff';ctx.lineWidth=1.7*u;ctx.lineCap='round';drawIcon(part,x,y,u);
 }
 // Size in metres, as the app shows it under the shape.
 const m=v=>(v*view.grid_size/1000).toFixed(1).replace('.',',');let text='',lx=0,ly=0;
 if(s.kind==='rect'){const ys=[[-1,1],[1,1],[1,-1],[-1,-1]].map(([a,b])=>rectCorner(g,a,b)[1]);text=m(g.w)+' × '+m(g.h)+' м';[lx,ly]=toCanvasXY(g.cx,Math.max(...ys));ly-=22*u;}
 else if(s.kind==='line'){text=m(Math.hypot(g.x1-g.x0,g.y1-g.y0))+' м';const dx=g.x1-g.x0,dy=g.y1-g.y0,l=Math.hypot(dx,dy)||1;[lx,ly]=toCanvasXY((g.x0+g.x1)/2+dy/l*22*k,(g.y0+g.y1)/2-dx/l*22*k);}
 if(text){ctx.font=`600 ${11*u}px system-ui`;ctx.textAlign='center';ctx.textBaseline='middle';const w=ctx.measureText(text).width+12*u;ctx.fillStyle='rgba(23,35,45,.78)';ctx.beginPath();ctx.roundRect?.(lx-w/2,ly-9*u,w,18*u,9*u);ctx.fill();ctx.fillStyle='#fff';ctx.fillText(text,lx,ly);}
 ctx.restore();
}
let grab=null,grabbed=false;
canvas.onpointerdown=e=>{
 grabbed=false;if(grab||!view||view.empty||busy||(e.pointerType!=='touch'&&e.button!==0))return;
 const shapes=editorShapes();if(!shapes)return;
 const p=mapPoint(e),hit=hitShape(shapes,p.x,p.y,cellsPerPx());if(!hit)return;
 e.stopPropagation();grabbed=true; // not a pan of the map and not a tap on a room
 if(!hit.s.selected)hit.s.select();
 if(hit.part==='delete'){hit.s.remove();draw();disableControls(busy);return;}
 grab={...hit,start:p,g0:structuredClone(hit.s.geom()),moved:false,id:e.pointerId};canvas.setPointerCapture?.(e.pointerId);draw();
};
canvas.onpointermove=e=>{
 if(!grab||e.pointerId!==grab.id)return;const p=mapPoint(e),dx=p.x-grab.start.x,dy=p.y-grab.start.y;
 if(!grab.moved&&Math.hypot(dx,dy)<2*cellsPerPx())return;grab.moved=true;
 grab.s.set(dragShape(grab.s.kind,grab.part,grab.g0,p,dx,dy,grab.s.min));draw();
};
canvas.onpointerup=canvas.onpointercancel=e=>{if(!grab||e.pointerId!==grab.id)return;const g=grab;grab=null;if(g.moved)g.s.done?.();draw();disableControls(busy);};
canvas.onclick=e=>{
 if(grabbed){grabbed=false;return;}
 if(!view||view.empty||busy)return;
 const {x,y}=mapPoint(e);
 if(patrolDrawing){addPatrolPoint(x,y);return;}
 if(thresholdDrawing){addThresholdPoint(x,y);return;}
 if(!editTool&&!openSheet&&dock.dataset.mode==='zone'){zoneSel=-1;zoneStatus();draw();return;} // in zone mode a tap does not pick rooms
 if($('threshold-details').open&&selectThresholdNear(x,y))return;
 const px=Math.min(view.width-1,Math.max(0,Math.floor(x))),py=Math.min(view.height-1,Math.max(0,Math.floor(y))),id=view.grid[py*view.width+px];
 if(view.rooms.some(r=>r.id===id))selectRoom(id);
};
// Show only what this robot supports: commands the hardware table rules out and properties the robot never answers are hidden.
export function applyCapabilities(caps){
 const id=caps.identity||{};hardware=caps.hardware||null;
 $('robot-name').textContent=robotName(id.model);
 $('model').textContent=(id.model||'Неизвестная модель')+' · '+(id.fw_ver||'?')+(caps.agent?.version?' · агент '+caps.agent.version:'')+(caps.firmware?.verified?'':' · прошивка не проверена');
 for(const button of document.querySelectorAll('[data-command]')){const c=caps.commands?.[button.dataset.command];button.hidden=c?.available===false;if(c?.reason)button.title=c.reason;}
 for(const [prop,input,apply] of [['suction','room-fan',null],['volume','volume','save-volume']]){const hide=caps.properties?.[prop]?.readable===false;$(input).closest('label').hidden=hide;if(apply)$(apply).hidden=hide;}
}
let loggingIn=false; // a reload fires pageshow and focus while the first load still runs: no resume then
async function login(){loggingIn=true;try{await loginNow();}finally{loggingIn=false;}}
async function loginNow(){
 const epoch=viewEpoch,loginToken=token,caps=await api('/api/v1/capabilities');if(epoch!==viewEpoch||loginToken!==token)return;if(captiveSetup)document.querySelector('#sheet-wifi .sheet-body').append($('wifi-panel'));applyCapabilities(caps);
 $('login').hidden=true;$('dashboard').hidden=false;$('logout').hidden=false;$('login-error').textContent='';
 sessionStorage.setItem('dreame-token',token);
 await loadLook();await refresh();await refreshMap();loadMessages().catch(()=>{});loadPatrolSchedules().catch(()=>{});await mainTokenOnly('video-code-panel',loadVideoCode);await mainTokenOnly('modules-panel',loadModules);$('nav-modules').hidden=$('modules-panel').hidden;await mainTokenOnly('update-panel',loadUpdate);$('look-panel').hidden=$('update-panel').hidden;await mainTokenOnly('tokens-panel',loadTokens);await mainTokenOnly('logs-panel',loadLogs);await mainTokenOnly('voice-panel',()=>mountVoice($('voice'),{api,token:()=>token,toast}));await loadHistory().catch(e=>toast(e.message,true));clearInterval(timer);timer=setInterval(()=>refresh(),30000);streamEvents();
}
$('login-form').onsubmit=async e=>{e.preventDefault();token=$('token').value.trim();try{await login();if($('remember').checked)localStorage.setItem('dreame-token',token);else localStorage.removeItem('dreame-token');$('token').value='';}catch(e){$('login-error').textContent=e.message;}};
$('logout').onclick=()=>{clearPets();stopEvents();viewEpoch++;for(const read of activeReads)read.abort('logout');refreshing=false;clearInterval(timer);timer=null;token='';sessionStorage.removeItem('dreame-token');localStorage.removeItem('dreame-token');$('dashboard').hidden=true;$('login').hidden=false;$('logout').hidden=true;};
// Buttons of the actions catalog (actions.js) go to their page's container; data-command keeps them under
// the capability hiding, the busy lock and the click handler below.
for(const act of actions){
 const box=document.querySelector(`.page-actions[data-page="${act.page}"]`);if(!box)continue;
 const b=document.createElement('button');b.textContent=act.label;b.dataset.command=act.command;if(act.confirm)b.dataset.confirm=act.confirm;if(act.danger)b.classList.add('danger');box.append(b);
}
for(const button of document.querySelectorAll('[data-command]'))button.onclick=async()=>{
 // data-command values are the agent's command names.
 try{if(button.dataset.confirm&&!window.confirm(button.dataset.confirm))return;if(button.dataset.command==='reboot')await rebootRobot();else await command(button.dataset.command);}catch{}
};
$('volume').oninput=()=>{volumeDirty=true;$('volume-value').textContent=$('volume').value+'%';};
$('save-volume').onclick=async()=>{try{await command('set_volume',{value:Number($('volume').value)});volumeDirty=false;}catch{}};
$('refresh-map').onclick=()=>refreshMap();
$('clear-selection').onclick=()=>{selected.clear();points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);};
$('clean-rooms').onclick=async()=>{try{const ids=[...selected],mapID=view?.map_id;const args=roomCommand(ids,Number($('room-fan').value),Number($('room-water').value),Number($('room-repeats').value));await control('/api/v1/command',args,async()=>{const current=await api('local.map_view');if(current.map_id!==mapID||!ids.every(id=>current.rooms.some(r=>r.id===id)))throw Error('Комнаты изменились. Обнови карту и выбери их заново.');});}catch(e){toast(e.message,true);}};
for(const id of ['room-water','room-repeats'])$(id).onchange=()=>{roomOptionsDirty=true;};
// One suction choice, as in the app: it is the robot's own setting (the whole home) and goes with room and zone cleaning.
$('room-fan').onchange=async()=>{fanPending=true;try{await command('set_suction',{value:Number($('room-fan').value)});}catch{}finally{fanPending=false;}};
// ---- Zone cleaning: up to 5 rectangles, added in the middle of the map and edited with the handles.
$('draw-zone').onclick=()=>{if(!view||view.empty||busy||zones.length>=5)return;zones.push(newRect(2000,2000,zones.length));zoneSel=zones.length-1;zoneStatus();draw();};
$('clean-zone').onclick=async()=>{
 try{const rects=zones.map(zoneRobot),mapID=view.map_id;
  await control('/api/v1/command',{name:'clean_zone',zones:rects,repeats:Number($('room-repeats').value),suction:Number($('room-fan').value),water:Number($('room-water').value)},async()=>{const current=await api('local.map_view');if(current.map_id!==mapID)throw Error('Карта изменилась. Выбери зону заново.');});}
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
 try{await control('action',{siid:6,aiid:2,in:[{piid:4,value:JSON.stringify(change.data)}]},async()=>{const current=await api('local.map_view');if(current.revision!==change.revision||current.map_id!==change.map_id)throw Error('Карта изменилась. Обнови её и выбери точки заново.');});setTimeout(()=>refreshMap(),1500);return true;}catch(e){toast(e.message,true);return false;}
};
$('send-rpc').onclick=async()=>{try{const method=$('rpc-method').value,params=JSON.parse($('rpc-params').value),result=method==='get_properties'?await api(method,params):await control(method,params);$('rpc-result').textContent=JSON.stringify(result,null,2);}catch(e){toast(e.message,true);}};
// The login form starts hidden: with a saved token the panel opens straight away, and the form shows only when
// there is no token or the agent turned it down, so a reload does not flash it.
if(token)login().catch(e=>{if(e.status===401){token='';sessionStorage.removeItem('dreame-token');localStorage.removeItem('dreame-token');}else connectionLost(e.message);$('login-error').textContent=e.message;if($('dashboard').hidden)$('login').hidden=false;});
else $('login').hidden=false;

if(captiveSetup){$('login').querySelector('h2').textContent='Настройка Wi-Fi робота';$('login').querySelector('p.muted').textContent='Укажи пароль агента, имя сети и пароль Wi-Fi. Затем нажми «Подключить робот к сети».';$('login-form').querySelector('button').textContent='Открыть панель управления';$('wifi-details').open=true;$('login').append($('wifi-panel'));}

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
function drawThresholds(){const chosen=thresholdDraft&&$('threshold-choice').value;if(!agentDraws('thresholds'))for(const item of thresholdLines())if(chosen!==item.kind+':'+item.index)strokeThreshold(item.line,item.kind);if(thresholdDraft?.line)strokeThreshold(thresholdDraft.line,$('threshold-kind').value,true);}
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
$('draw-threshold').onclick=()=>{if(busy||!view?.thresholds_editable)return;thresholdDrawing=false;points=[];$('threshold-choice').value='';thresholdDraft={line:toRobotLine(newLine(1000))};$('threshold-status').textContent='Порог добавлен в центр карты. Перетащи его концы на место и нажми «Сохранить порог».';disableControls(busy);draw();};
$('cancel-threshold').onclick=()=>{thresholdDrawing=false;thresholdDraft=null;points=[];$('threshold-choice').value='';$('threshold-status').textContent='Выбор отменён.';disableControls(busy);draw();};
async function submitThreshold(remove=false){
 if(busy||!view?.thresholds_editable||(!remove&&!thresholdDraft))return;
 const passable=(view.passable_thresholds||[]).map(x=>[...x]),impassable=(view.impassable_thresholds||[]).map(x=>[...x]),lists={passable,impassable};
 const [oldKind,rawIndex]=$('threshold-choice').value.split(':'),index=Number(rawIndex),newKind=$('threshold-kind').value;
 if(oldKind&&lists[oldKind]?.[index])lists[oldKind].splice(index,1);else if(remove)return;
 if(!remove){const line=thresholdDraft.line;if(line[0]===line[2]&&line[1]===line[3]){toast('Точки порога совпадают',true);return;}if(oldKind===newKind)lists[newKind].splice(index,0,line);else lists[newKind].push(line);}
 try{await control('local.thresholds',{map_id:view.map_id,revision:view.revision,passable,impassable});thresholdDraft=null;$('threshold-choice').value='';$('threshold-status').textContent='Команда принята. Ожидаю обновление сохранённой карты.';for(const delay of [1500,5000,12000])setTimeout(()=>refreshMap(),delay);return true;}
 catch(e){toast(e.message,true);return false;}
}
$('save-threshold').onclick=()=>submitThreshold();$('delete-threshold').onclick=()=>submitThreshold(true);
// ---- Per-room settings: robot row [suction, water, repeats, order, mode, extra]; only changed rooms are sent.
const roomFields=[['suction',0,{0:'Тихий',1:'Стандартный',2:'Турбо',3:'Максимум'}],['water',1,{1:'Низкая',2:'Средняя',3:'Высокая'}],['repeats',2,{1:'1 раз',2:'2 раза',3:'3 раза'}],['mode',4,{0:'Только сухая',1:'Только влажная',2:'Сухая и влажная'}]];
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
for(const [id,[name]] of Object.entries(furnitureTypes))$('furniture-type').add(new Option(name,id));
const furnitureName=f=>furnitureTypes[f.type]?.[0]||'Предмет '+f.type;
function drawFurniture(){
 if(agentDraws('furniture'))return;
 ctx.save();ctx.lineWidth=2;ctx.font='11px sans-serif';
 for(const f of [...(view?.furniture||[]).filter(f=>f.index!==furnitureDraft?.index),...(furnitureDraft?[furnitureDraft]:[])]){
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
 try{await control('local.furniture',{map_id:view.map_id,revision:view.revision,...change});$('furniture-status').textContent='Сохранено. Ожидаю обновление карты.';for(const delay of [1500,5000])setTimeout(()=>refreshMap(),delay);return true;}
 catch(e){$('furniture-status').textContent=e.message;return false;}
}
$('place-furniture').onclick=()=>{
 if(busy||!view?.furniture_editable)return;const type=Number($('furniture-type').value),[,w,h]=furnitureTypes[type],g=newRect(w,h);
 furnitureDraft={index:null,type,x:0,y:0,width:w,height:h,rotation:0};setFurniture({...g,w:mm(w),h:mm(h)});furnitureStatus('Предмет поставлен в центр карты. Двигай его, поворачивай за ↻, размер — за ↘, затем «Сохранить».');draw();
};
$('furniture-type').onchange=()=>{if(!furnitureDraft)return;const type=Number($('furniture-type').value),[,w,h]=furnitureTypes[type];furnitureDraft={...furnitureDraft,type,...(furnitureDraft.index==null?{width:w,height:h}:{})};draw();};
$('save-furniture').onclick=async()=>{const d=furnitureDraft;if(!d)return;furnitureDraft=null;if(!await saveFurniture({remove:d.index==null?[]:[d.index],add:[{type:d.type,x:d.x,y:d.y,width:d.width,height:d.height,rotation:d.rotation}]})){furnitureDraft=d;draw();}};
$('cancel-furniture').onclick=()=>{furnitureDraft=null;furnitureStatus('Изменение отменено.');draw();};
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
  remove.onclick=()=>{const next=structuredClone(restrictions());next[kind].splice(index,1);restrictDraft=next;restrictSel=null;$('restrict-status').textContent='Удалено из черновика. Нажми «Сохранить изменения».';renderRestrictions();draw();disableControls(busy);};
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
 const saved=!agentDraws('restrictions');
 ctx.fillStyle=carpetPattern(.15);for(const c of saved?view?.carpets||[]:[]){ctx.beginPath();for(let i=0;i+1<c.points.length;i+=2){const [x,y]=px(c.points[i],c.points[i+1]);i?ctx.lineTo(x,y):ctx.moveTo(x,y);}ctx.closePath();ctx.fill();}
 for(const [kind,items] of Object.entries(saved?lists:{}))for(const r of items){
  if(!Array.isArray(r)||r.length!==4)continue;const [x0,y0]=px(r[0],r[1]),[x1,y1]=px(r[2],r[3]);
  if(kind==='walls'){ctx.setLineDash([]);ctx.strokeStyle='#ff4d4d';ctx.beginPath();ctx.moveTo(x0,y0);ctx.lineTo(x1,y1);ctx.stroke();continue;}
  const style={no_mop_zones:['#5aa9ff','rgba(90,169,255,.25)'],carpets_added:['rgba(0,0,0,.45)',carpetPattern(.3)],carpets_ignored:['#8a8a8a','rgba(138,138,138,.15)'],ramps:['#2f9e44','rgba(47,158,68,.25)']}[kind]||['#ff4d4d','rgba(255,77,77,.25)'];
  ctx.strokeStyle=style[0];ctx.fillStyle=style[1];ctx.setLineDash(kind==='carpets_ignored'?[6,4]:[]);
  ctx.fillRect(Math.min(x0,x1),Math.min(y0,y1),Math.abs(x1-x0),Math.abs(y1-y0));ctx.strokeRect(Math.min(x0,x1),Math.min(y0,y1),Math.abs(x1-x0),Math.abs(y1-y0));
 }
 ctx.restore();
}
function addRestriction(kind){
 if(busy||!view?.restrictions_editable)return;const list=restrictEdit()[kind];
 list.push(kind==='walls'?toRobotLine(newLine(1500)):toRobotRect(newRect(1000,1000,list.length%4)));restrictSel={kind,index:list.length-1};
 restrictChanged(restrictKinds[kind]+' добавлена в центр карты. Двигай её и тяни за уголок ↘'+(kind==='walls'?' (стену — за концы).':'.'));draw();
}
for(const b of document.querySelectorAll('[data-add]'))b.onclick=()=>addRestriction(b.dataset.add);
$('cancel-restrict').onclick=()=>{restrictDraft=null;restrictSel=null;$('restrict-status').textContent='Изменения отменены.';renderRestrictions();draw();disableControls(busy);};
$('save-restrict').onclick=()=>saveRestrictions();
async function saveRestrictions(){
 if(!restrictDraft)return true;
 const d=restrictDraft,changed=k=>JSON.stringify(d[k])!==JSON.stringify(view[k]||[]);
 try{
  if(['walls','no_go_zones','no_mop_zones'].some(changed))await control('local.restrictions',{map_id:view.map_id,revision:view.revision,walls:d.walls,no_go_zones:d.no_go_zones,no_mop_zones:d.no_mop_zones});
  if(carpetKinds.some(changed)){const now=await api('local.map_view');await control('local.carpets',{map_id:now.map_id,revision:now.revision,carpets_added:d.carpets_added,carpets_ignored:d.carpets_ignored});}
  if(changed('ramps')){const now=await api('local.map_view');await control('local.thresholds',{map_id:now.map_id,revision:now.revision,passable:now.passable_thresholds||[],impassable:now.impassable_thresholds||[],ramps:d.ramps});}
  restrictDraft=null;restrictSel=null;$('restrict-status').textContent='Изменения сохранены. Ожидаю обновление сохранённой карты.';for(const delay of [1500,5000,12000])setTimeout(()=>refreshMap(),delay);return true;}
 catch(e){$('restrict-status').textContent=e.message;toast(e.message,true);return false;}
}

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
const isSlider=def=>def.kind==='number'&&def.max-def.min<=60; // a short range is a slider applied on release
function settingControl(def,value){
 const input=document.createElement(def.kind==='select'?'select':'input');input.id='setting-'+def.id;input.setAttribute('aria-label',def.label);
 if(def.kind==='select'){
  // In «Настроить» the route offers what the app does for this robot: standard and fast.
  for(const option of def.id==='cleaning_route'?def.options.filter(o=>o.value===1||o.value===4):def.options)input.add(new Option(option.label,String(option.value)));
  let normalized=value;if(def.auto&&Number(value)<0&&def.options.some(x=>x.value===0))normalized=0;
  if(![...input.options].some(x=>x.value===String(normalized)))input.add(new Option('Текущее: '+value,String(value)));
  input.value=String(normalized);
 }else if(def.kind==='bool'){input.type='checkbox';input.className='switch';input.setAttribute('role','switch');input.checked=!!value;}
 else{input.type=def.kind==='number'?'number':def.kind==='time'?'time':'text';input.value=String(value);if(def.kind==='number'){input.min=def.min;input.max=def.max;input.step=def.step||1;if(isSlider(def))input.type='range';}else input.maxLength=128;}
 return input;
}
// Pages (pages.js) with their own panel show there, next to the matching controls; if that panel is hidden
// (no rights or the robot lacks it) they stay under «Все настройки пылесоса». A closed sheet still counts as shown.
function settingGroup(id){
 const title=(pages.find(p=>p.items.includes(id))||pages.find(p=>new RegExp(p.match).test(id))).title;
 if(!document.querySelector('.setting-group'))for(const {title:name,home:homeId} of pages){
  const g=document.createElement('section'),home=homeId&&$(homeId),inHome=home&&!home.closest('[hidden]:not(.sheet)');g.className='setting-group';g.hidden=true;g.dataset.group=name;
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
     if(def.confirm&&chosen===true&&!confirm(def.confirm))throw Error('Не включено');
     const raw=settingsSnapshot.get(def.siid+'/'+def.piid);
     sent=true;await control('local.setting',{siid:def.siid,piid:def.piid,expected:raw,value:settingValue(def,chosen,raw)});
    }catch(e){if(!sent)toast(e.message,true); // control() already reported its own errors
    const now=decodeSetting(def,settingsSnapshot);if(now!==undefined)row.setInput(now);}
    finally{settingsDirty.delete(def.id);await refreshSettings();}
   };
   // Switches and lists apply on change; typed values (number, time, text) wait for «ОК», a schedule's switch applies at once.
   const typed=['number','time','text','window'].includes(def.kind)&&!isSlider(def);if(isSlider(def))input.addEventListener('input',()=>{current.textContent=input.value;});let ok=null;
   if(typed){ok=document.createElement('button');ok.textContent='ОК';ok.className='setting-apply';ok.onclick=apply;if(def.kind==='window')input.querySelector('.switch').addEventListener('change',apply);}
   else input.addEventListener('change',apply);
   row.append(label,input);if(ok)row.append(ok);settingsRows.set(def.id,row);settingGroup(def.id).append(row);if(row.closest('#tune-settings'))chipSetting(def,input);
  }else if(!settingsDirty.has(def.id))row.setInput(value);
  row.querySelector('.setting-current').textContent=def.kind==='bool'?'':isSlider(def)?String(value):'Сейчас: '+(typeof value==='object'?(value.enabled?'включено':'выключено')+' '+value.start+'–'+value.end:typeof value==='boolean'?(value?'включено':'выключено'):String(value));
  row.hidden=!def.label.toLocaleLowerCase().includes(query)&&!row.closest('#station-settings,#voice-settings,#dnd-settings');
 }
 for(const group of document.querySelectorAll('.setting-group'))group.hidden=![...group.querySelectorAll('.setting-row')].some(r=>!r.hidden);
 {const wet=settings.find(d=>d.id==='wetness_level');$('room-water').closest('label').hidden=!!wet&&decodeSetting(wet,settingsSnapshot)!==undefined;} // 1–32 replaces the three steps
 syncChips();tuneExtras();
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
for(const id of ['settings-details','station-settings-details','voice-settings-details','dnd-settings-details','pet-settings-details'])$(id).ontoggle=e=>{if(e.target.open)refreshSettings();};$('refresh-settings').onclick=()=>refreshSettings();$('settings-search').oninput=()=>renderSettings();

async function rebootRobot(){
 if(busy||!window.confirm('Перезагрузить робот? Связь временно пропадёт.'))return;
 busy=true;disableControls(true);
 try{const result=await api('local.reboot');if(!result.accepted)throw Error('Запрос перезагрузки не принят');rebootingUntil=Date.now()+90000;connectionLost('Робот перезагружается');toast('Перезагрузка запрошена. Дождись восстановления связи.');}
 catch(e){toast(e.message,true);}finally{busy=false;disableControls(false);}
}
function suspendReads(reason='suspend'){stopEvents();viewEpoch++;refreshing=false;settingsLoading=false;for(const controller of activeReads)controller.abort(reason);}
function scheduleResume(){clearTimeout(resumeTimer);resumeTimer=setTimeout(async()=>{
 if(document.hidden||!token||loggingIn)return;if(busy){scheduleResume();return;}
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
// ---- Video access code (monitor.go): remote_move drives inside the video monitoring session, which needs it.
const videoCodeText=on=>on?'Код задан: стрелки камеры ездят без мотора.':'Код не задан: ручное управление выключено.';
async function loadVideoCode(){$('video-code-status').textContent=videoCodeText((await api('/api/v1/system/video_code')).configured);}
async function saveVideoCode(code){
 try{const r=await api('/api/v1/system/video_code',{code});$('video-code').value='';$('video-code-status').textContent=videoCodeText(r.configured);}
 catch(e){$('video-code-status').textContent=e.message;}
}
$('save-video-code').onclick=()=>{const code=$('video-code').value.trim();if(!/^[0-9]{4,8}$/.test(code)){$('video-code-status').textContent='Код: 4–8 цифр.';return;}saveVideoCode(code);};
$('clear-video-code').onclick=()=>saveVideoCode('');
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
$('draw-patrol').onclick=()=>{if(busy||!view||view.empty)return;patrolDrawing=true;thresholdDrawing=false;$('patrol-status').textContent='Нажимай на карте точки по порядку.';};
$('clear-patrol').onclick=()=>{patrolDrawing=false;patrolPoints=[];patrolSel=-1;$('patrol-status').textContent='';patrolButtons();draw();};
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

// ---- App-style layout: one screen with the map; everything else opens in sheets.
const robotNames={'dreame.vacuum.r2505':'Dreame X50'};
function robotName(model){return robotNames[model]||(model?'Dreame '+String(model).split('.').pop():'Робот-пылесос');}
// The stage changes size with the window, an opened side sheet and the bottom bar's mode.
let layoutPending=false;
if('ResizeObserver' in window)new ResizeObserver(()=>{if(layoutPending)return;layoutPending=true;requestAnimationFrame(()=>{layoutPending=false;layoutMap();});}).observe(mapStage);
// Bottom bar: Комната / Все / Зона pick which "Чистить" button is shown.
const dock=$('dock');
export function setMode(mode){
 dock.dataset.mode=mode;document.body.dataset.mode=mode;for(const b of dock.querySelectorAll('.segmented button'))b.setAttribute('aria-selected',String(b.dataset.mode===mode));
 if(mode==='zone'&&!zones.length&&view&&!view.empty&&!busy){zones.push(newRect(2000,2000));zoneSel=0;} // the app starts the zone mode with one zone
 if(mode==='zone')zoneStatus();draw();
}
document.body.dataset.mode=dock.dataset.mode;
// «Циклы» (x1, x2, x3): the repeats of a room or zone cleaning, the same choice as in «Настроить».
const repeats=$('room-repeats'),cycles=$('cycles');
const syncCycles=()=>{cycles.textContent='x'+repeats.value;};
cycles.onclick=()=>{const o=[...repeats.options],next=o[(o.findIndex(x=>x.value===repeats.value)+1)%o.length];repeats.value=next.value;repeats.dispatchEvent(new Event('change',{bubbles:true}));};
repeats.addEventListener('change',syncCycles);syncCycles();
// ---- Map editors as in the app: a block of «Карта» takes the screen; ← leaves without saving, ✓ saves.
const editTools={'restrict-details':['restrict','Запретная зона / виртуальная стена','restrict-status','Нажми кнопку внизу: фигура появится в центре карты. Нажми на фигуру, чтобы двигать её.'],
 'carpet-details':['carpet','Ковры','restrict-status','Нажми «Добавить ковёр»: он появится в центре карты.'],
 'threshold-details':['threshold','Виртуальный порог / рампа','threshold-status','Нажми на порог, чтобы двигать его концы, или добавь новый.'],
 'furniture-details':['furniture','Мебель','furniture-status','Выбери предмет и нажми «Добавить», или нажми на предмет на карте.'],
 'split-details':['split','Разделение комнат','map-preview','']};
let editTool=null,editDetails=null;
const editStatus=text=>{$('edit-status').textContent=text;};
for(const [id,[tool,,statusId]] of Object.entries(editTools)){
 $(id).addEventListener('toggle',()=>{if($(id).open&&!editTool)enterEdit(id);});
 new MutationObserver(()=>{if(editTool===tool&&statusId!=='map-preview')editStatus($(statusId).textContent);}).observe($(statusId),{childList:true,characterData:true,subtree:true});
}
function enterEdit(id){
 const [tool,title,,hint]=editTools[id];editTool=tool;editDetails=id;$('edit-title').textContent=title;keepMap(()=>{document.body.dataset.edit=tool;closeSheet();});
 if(tool==='split')splitOp($('map-operation').value);else editStatus(hint);
 draw();disableControls(busy);
}
function leaveEdit(){
 const id=editDetails;editTool=null;editDetails=null;restrictSel=null;keepMap(()=>{delete document.body.dataset.edit;closeSheet();});if(id)$(id).open=false;draw();disableControls(busy);
}
const editDirty=()=>({restrict:!!restrictDraft,carpet:!!restrictDraft,threshold:!!thresholdDraft||!!restrictDraft,furniture:!!furnitureDraft,split:false})[editTool];
$('edit-back').onclick=()=>{
 if(editDirty()&&window.confirm('Сохранить текущую операцию?'))return $('edit-save').onclick(); // as the app: Отмена discards
 if(editTool==='furniture')furnitureDraft=null;
 if(editTool==='threshold'){thresholdDraft=null;$('threshold-choice').value='';}
 if(editTool==='split'){selected.clear();points=[];invalidateEdit();renderRoomButtons();}
 if(['restrict','carpet','threshold'].includes(editTool)){restrictDraft=null;renderRestrictions();}
 leaveEdit();
};
$('edit-save').onclick=async()=>{
 if(busy)return;let ok=true;
 if(editTool==='threshold'&&thresholdDraft)ok=await submitThreshold();
 if(ok&&['restrict','carpet','threshold'].includes(editTool))ok=await saveRestrictions();
 if(editTool==='furniture'&&furnitureDraft){const d=furnitureDraft;furnitureDraft=null;ok=await saveFurniture({remove:d.index==null?[]:[d.index],add:[{type:d.type,x:d.x,y:d.y,width:d.width,height:d.height,rotation:d.rotation}]});if(!ok)furnitureDraft=d;}
 if(editTool==='split'&&selected.size){$('prepare-map-edit').click();if(!pending)return;ok=await $('apply-map-edit').onclick();}
 if(ok)leaveEdit();
};
$('carpet-types-open').onclick=()=>{showSheet('map-edit');for(const d of document.querySelectorAll('#sheet-map-edit .sheet-body>details'))d.open=d.id==='carpet-details';};
for(const b of document.querySelectorAll('[data-threshold]'))b.onclick=()=>{
 if(busy||!view?.thresholds_editable)return;const kind=b.dataset.threshold,name=kind==='passable'?'проходимый':'непроходимый';$('threshold-kind').value=kind;restrictSel=null;
 if(thresholdDraft){editStatus('Выбранный порог теперь '+name+'. Сохранить — ✓ вверху.');draw();return;}
 $('draw-threshold').click();editStatus('Добавлен '+name+' порог. Перетащи его концы на место, сохранить — ✓ вверху.');
};
function splitOp(op){
 if($('map-operation').value!==op){$('map-operation').value=op;$('map-operation').dispatchEvent(new Event('change'));}
 for(const b of document.querySelectorAll('[data-op]'))b.setAttribute('aria-pressed',String(b.dataset.op===op));
 editStatus(op==='merge'?'Нажми на карте две соседние комнаты, затем ✓.':'Нажми на комнату: на ней появится линия. Перетащи её концы и нажми ✓.');
}
for(const b of document.querySelectorAll('[data-op]'))b.onclick=()=>splitOp(b.dataset.op);
for(const b of dock.querySelectorAll('.segmented button'))b.onclick=()=>setMode(b.dataset.mode);
// Sheets: one open at a time, without a backdrop, so the map stays usable for map tools.
let openSheet=null;
export function showSheet(name){
 const sheet=$('sheet-'+name);if(!sheet)return;
 if(openSheet&&openSheet!==sheet)openSheet.hidden=true;
 if(name==='menu')for(const item of sheet.querySelectorAll('[data-sheet]')){const target=$('sheet-'+item.dataset.sheet),panels=target?[...target.querySelectorAll('.sheet-body>.panel')]:[];item.hidden=panels.length>0&&panels.every(p=>p.hidden);}
 // A sheet holding one folded block opens it: the sheet's title already names it.
 const body=sheet.querySelector('.sheet-body'),only=body.querySelectorAll(':scope>.panel>details,:scope>details');if(only.length===1&&body.children.length===1&&!only[0].open)only[0].open=true;
 if(name==='pets')loadPets().catch(e=>toast(e.message,true));
 if(name==='tune')refreshSettings(); // mode, wetness, mop washing and route live with the robot's settings
 keepMap(()=>{sheet.hidden=false;openSheet=sheet;document.body.dataset.openSheet=name;});sheet.querySelector('.sheet-close').focus({preventScroll:true});draw();
}
export function closeSheet(){if(!openSheet)return;const opener=document.querySelector('[data-sheet="'+openSheet.id.slice(6)+'"]');keepMap(()=>{openSheet.hidden=true;openSheet=null;delete document.body.dataset.openSheet;});draw();if(opener&&opener.offsetParent)opener.focus({preventScroll:true});}
document.addEventListener('click',e=>{const opener=e.target.closest('[data-sheet]');if(opener){e.preventDefault();showSheet(opener.dataset.sheet);return;}if(e.target.closest('.sheet-close'))closeSheet();});
document.addEventListener('keydown',e=>{if(e.key==='Escape'&&openSheet)closeSheet();});
// A tap outside the open sheet closes it and does nothing else; the camera sheet keeps the map for its trip points.
let swallowClick=false;
document.addEventListener('pointerdown',e=>{
 swallowClick=false;
 if(!openSheet||openSheet.contains(e.target)||e.target.closest('[data-sheet],.photo-viewer,#toast,#update-banner'))return;
 if(openSheet.id==='sheet-camera'&&mapStage.contains(e.target))return;
 closeSheet();swallowClick=true;e.stopPropagation();
},true);
document.addEventListener('click',e=>{if(swallowClick){swallowClick=false;e.stopPropagation();e.preventDefault();}},true);
$('nav-modules').addEventListener('click',()=>closeSheet());
// «Настроить»: choices as round icons with a caption, as in the app; the select stays the source of truth
// (and what the code reads). Settings rows of the robot get the same look when they land in this sheet.
const I=d=>`<svg viewBox="0 0 24 24" aria-hidden="true">${d}</svg>`;
const fanI=n=>I(`<circle cx="12" cy="12" r="1.6"/>${[0,120,240].map(a=>`<path d="M12 10.4c-${n} -3 -${n/2} -6 0-7 ${n/2} 1.6 1.2 4.2 0 7" transform="rotate(${a} 12 12)"/>`).join('')}`);
const dropI='<path d="M12 3.5c3.3 4 5 6.8 5 9.2a5 5 0 0 1-10 0c0-2.4 1.7-5.2 5-9.2z"/>';
const chipIcons={
 'cleaning_mode:0':fanI(2),'cleaning_mode:1':I(dropI),
 'cleaning_mode:2':I('<path d="M9 5.5c2.4 3 3.6 5 3.6 6.8a3.6 3.6 0 0 1-7.2 0c0-1.8 1.2-3.8 3.6-6.8z"/><path d="M15.5 11.5c1.6-1.4 3.7-1 4.3.6-1.6.4-2.6 1.6-2.6 3.3-1.8-.4-2.7-2.4-1.7-3.9z"/>'),
 'cleaning_mode:3':I('<path d="M6.5 5v5M5 6.3 6.5 5"/><path d="M16 13.5c1.8 2.2 2.7 3.7 2.7 5a2.7 2.7 0 0 1-5.4 0c0-1.3.9-2.8 2.7-5z"/><path d="M5 14.5h3.2L5 19h3.4"/><path d="M11 9.5h5m-2-2 2 2-2 2"/>'),
 'room-fan:0':I('<path d="M16.5 15.8A6.5 6.5 0 0 1 9.2 6a6.5 6.5 0 1 0 7.3 9.8z"/>'),
 'room-fan:1':fanI(1.5),'room-fan:2':fanI(2.5),'room-fan:3':I('<circle cx="12" cy="12" r="8.5"/>'+fanI(3).slice(fanI(3).indexOf('>')+1,-6)),
 'room-water:1':I(dropI),'room-water:2':I('<path d="M8.5 6c2.3 2.8 3.5 4.7 3.5 6.4a3.5 3.5 0 0 1-7 0c0-1.7 1.2-3.6 3.5-6.4zM15.5 9c2.3 2.8 3.5 4.7 3.5 6.4a3.5 3.5 0 0 1-7 0"/>'),
 'room-water:3':I('<path d="M12 3c1.9 2.3 2.9 3.9 2.9 5.3a2.9 2.9 0 0 1-5.8 0C9.1 6.9 10.1 5.3 12 3zM7 11c1.9 2.3 2.9 3.9 2.9 5.3a2.9 2.9 0 0 1-5.8 0C4.1 14.9 5.1 13.3 7 11zm10 0c1.9 2.3 2.9 3.9 2.9 5.3a2.9 2.9 0 0 1-5.8 0c0-1.4 1-3 2.9-5.3z"/>'),
 'self_clean_frequency:1':I('<rect x="4.5" y="4.5" width="15" height="15" rx="2"/><path d="M4.5 12h15M12 4.5v15"/>'),
 'self_clean_frequency:2':I('<circle cx="12" cy="12" r="8"/><path d="M12 7.5V12l3 2"/>'),
 'self_clean_frequency:3':I('<path d="M6 20V5.5a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1V20M4 20h16"/><circle cx="14.5" cy="12.5" r=".9"/>'),
 'self_clean_frequency:4':I('<path d="M12 3.5l1.8 5 5 1.8-5 1.8-1.8 5-1.8-5-5-1.8 5-1.8z"/><path d="M18.5 16v4M16.5 18h4"/>'),
 'cleaning_route:1':I('<path d="M5 6h14v4H5v4h14v4H5"/>'),'cleaning_route:4':I('<path d="M5 7h14l-14 10h14"/><path d="M17 4.5 19.5 7 17 9.5"/>'),
};
const chipCaps={'cleaning_mode:0':'Сухая','cleaning_mode:1':'Влажная','cleaning_mode:2':'Сухая и влажная','cleaning_mode:3':'Влажная после сухой','self_clean_frequency:4':'Умная'};
// Titles in «Настроить» as the app has them; the settings catalog keeps its own names elsewhere.
const tuneTitles={self_clean_frequency:'Промывка швабры',self_clean_value:'Интервал промывки',wetness_level:'Влажность швабры'};
// The washing interval counts square metres or minutes; by room and smart washing need none.
function tuneExtras(){
 const f=decodeSetting(settings.find(d=>d.id==='self_clean_frequency')||{},settingsSnapshot),row=settingsRows.get('self_clean_value');
 if(row&&row.closest('#tune-settings')){if(f===3||f===4)row.hidden=true;const cur=row.querySelector('.setting-current');cur.dataset.unit=f===1?' м²':f===2?' мин':'';}
 for(const [id,t] of Object.entries(tuneTitles)){const lab=settingsRows.get(id)?.querySelector('label');if(lab&&lab.closest('#tune-settings')&&lab.firstChild.nodeType===3)lab.firstChild.textContent=t;}
}
function chipsFor(select,key){
 const box=document.createElement('div');box.className='chips icons';box.setAttribute('role','radiogroup');
 for(const o of select.options){const b=document.createElement('button');b.type='button';b.dataset.value=o.value;b.setAttribute('role','radio');
  b.innerHTML=`<span class="ic">${chipIcons[key+':'+o.value]||I('<circle cx="12" cy="12" r="3"/>')}</span><span class="cap"></span>`;b.lastChild.textContent=chipCaps[key+':'+o.value]||o.textContent;
  b.onclick=()=>{select.value=o.value;select.dispatchEvent(new Event('change',{bubbles:true}));syncChips();};box.append(b);}
 select.classList.add('chip-source');const label=select.closest('label');if(label)label.append(box);else select.after(box);return box;
}
const chipSets=[...document.querySelectorAll('#sheet-tune select')].map(s=>[s,chipsFor(s,s.id)]);
function syncChips(){for(const [s,box] of chipSets){if(box.children.length!==s.options.length){box.remove();chipSets.splice(chipSets.findIndex(c=>c[0]===s),1,[s,chipsFor(s,s.dataset.chips||s.id)]);return syncChips();}for(const b of box.children)b.setAttribute('aria-checked',String(b.dataset.value===s.value));}}
function chipSetting(def,input){if(def.kind!=='select'||chipSets.some(c=>c[0]===input))return;input.dataset.chips=def.id;chipSets.push([input,chipsFor(input,def.id)]);input.addEventListener('change',syncChips);}
for(const [s] of chipSets)s.addEventListener('change',syncChips);
document.querySelector('[data-sheet="tune"]').addEventListener('click',()=>requestAnimationFrame(syncChips));
syncChips();
// Sub-pages (map editor, station settings): an opened block takes the sheet; its title is the way back.
for(const d of document.querySelectorAll('.hub>details'))d.addEventListener('toggle',()=>{if(d.open)d.parentElement.scrollTop=0;draw();});

$('label-size').onchange=async e=>{const prev=labelSize;try{labelSize=Number(e.target.value);draw();labelSize=Number((await api('/api/v1/ui',{label_size:labelSize})).label_size)||0;toast('Размер подписей сохранён');}catch(err){labelSize=prev;e.target.value=String(prev);draw();toast(err.message,true);}};

// ---- Camera monitoring screen (monitor.js): opened by the camera button on the map.
const monitorScreen=mountMonitor({api,command,token:()=>token,rpc:(name,args={})=>api('/api/v1/command',{name,...args}),toast,statusText:()=>$('status').textContent,mapCanvas:canvas});
$('open-monitor').onclick=()=>{closeSheet();monitorScreen.open();};
