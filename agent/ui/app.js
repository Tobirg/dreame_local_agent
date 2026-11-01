import {settings} from '/settings.js';
const $=id=>document.getElementById(id);
const captiveSetup=new URLSearchParams(location.search).get('setup')==='wifi';
const statusNames={0:'Ожидание',1:'Пауза',2:'Уборка',3:'Возвращается',4:'Локальная уборка',5:'Вдоль стен',6:'Заряжается',12:'Ошибка',14:'Спит',17:'Ожидание',18:'Уборка комнат',19:'Уборка зоны',20:'Точечная уборка',21:'Строит карту'};
const palette=['#7fd9c2','#a3bcec','#e5bd8a','#bcaceb','#a7d39a','#e3a5b4','#93cdd9','#d4d093'];
let token=sessionStorage.getItem('dreame-token')||localStorage.getItem('dreame-token')||'';
let timer=null,busy=false,refreshing=false,view=null,selected=new Set(),points=[],pending=null,properties=new Map(),toastTimer=null,fanDirty=false,volumeDirty=false,guardDirty=false;
let viewEpoch=0,resumeTimer=null,roomOptionsDirty=false,thresholdDrawing=false,thresholdDraft=null,settingsLoading=false,settingsSnapshot=new Map(),settingsRows=new Map(),rebootingUntil=0;
const activeReads=new Set(),settingsDirty=new Set();
const canvas=$('map'),ctx=canvas.getContext('2d');

export function pixelToRobot(v,x,y){
 if(!Array.isArray(v.origin)||v.origin.length!==2||!Number.isFinite(v.grid_size)||v.grid_size<=0||![x,y,...v.origin].every(Number.isFinite))throw Error('Некорректная калибровка карты');
 return [Math.round(v.origin[0]+x*v.grid_size),Math.round(v.origin[1]+y*v.grid_size)];
}
export function roomCommand(ids,fan,water,repeats=1){
 if(!ids.length||!ids.every(id=>Number.isInteger(id)&&id>0))throw Error('Выбери комнаты');
 if(!Number.isInteger(fan)||fan<0||fan>3||!Number.isInteger(water)||water<1||water>3)throw Error('Параметры уборки ещё не получены от робота');
 if(!Number.isInteger(repeats)||repeats<1||repeats>3)throw Error('Допустимо от 1 до 3 повторов');
 return {siid:4,aiid:1,in:[{piid:1,value:18},{piid:10,value:JSON.stringify({selects:ids.map(id=>[id,repeats,fan,water,1])})}]};
}
function toast(message,error=false){clearTimeout(toastTimer);$('toast').textContent=message;$('toast').classList.toggle('error',error);$('toast').hidden=false;toastTimer=setTimeout(()=>$('toast').hidden=true,7000);}
function isRead(method){return method==='get_properties'||method==='miIO.info'||method==='local.map_view'||method==='local.maps'||method.endsWith('_status');}
function connectionLost(message){$('connection').textContent=rebootingUntil>Date.now()?'Перезагрузка…':'Восстанавливаю связь…';$('connection').classList.add('connection-offline');$('reconnect').hidden=!token;$('reconnect').title=message;}
async function api(method,params=null){
 const read=isRead(method),requestID=read?null:Array.from(crypto.getRandomValues(new Uint8Array(16)),x=>x.toString(16).padStart(2,'0')).join('');
 for(let attempt=0;attempt<(read?2:1);attempt++){
  const controller=new AbortController(),timeout=setTimeout(()=>controller.abort('timeout'),read?15000:40000);
  if(read)activeReads.add(controller);
  try{
   const response=await fetch('/rpc',{method:'POST',headers:{'Content-Type':'application/json','Authorization':'Bearer '+token,...(requestID?{'X-Dreame-Request-ID':requestID}:{})},body:JSON.stringify({method,params}),credentials:'omit',cache:'no-store',signal:controller.signal});
   const body=await response.json();if(!response.ok){const error=Error(body.error||'Ошибка HTTP '+response.status);error.status=response.status;throw error;}return body.result;
  }catch(e){
   if(controller.signal.reason==='suspend'||controller.signal.reason==='logout'){const error=Error('Чтение приостановлено');error.suspended=true;throw error;}
   if(e.status)throw e;
   if(read&&attempt===0&&!document.hidden&&token){await new Promise(resolve=>setTimeout(resolve,150));continue;}
   throw Error(read?'Нет ответа от робота. Подключение будет восстановлено автоматически.':'Связь прервалась. Команда могла выполниться: проверь состояние перед повтором.');
  }finally{clearTimeout(timeout);activeReads.delete(controller);}
 }
}

function assertCommand(result){
 const rows=Array.isArray(result)?result:[result];
 if(!rows.length||rows.some(r=>!r||r.code!==0))throw Error('Робот отклонил команду: '+JSON.stringify(result));
}
async function control(method,params,before=null){
 if(busy)return;busy=true;disableControls(true);
 try{if(before)await before();const result=await api(method,params);assertCommand(result);toast('Команда принята роботом');setTimeout(()=>refresh(),900);return result;}
 catch(e){toast(e.message,true);throw e;}
 finally{busy=false;disableControls(false);}
}
function disableControls(disabled){
 document.querySelectorAll('[data-command],#save-fan,#save-volume,#save-region,#region-code,#save-guard,#save-wifi,#send-rpc,.setting-apply').forEach(b=>b.disabled=disabled);
 $('clean-rooms').disabled=disabled||selected.size===0;
 $('apply-map-edit').disabled=disabled||!pending;
 $('save-threshold').disabled=disabled||!thresholdDraft||!view?.thresholds_editable;
 $('delete-threshold').disabled=disabled||!$('threshold-choice').value||!view?.thresholds_editable;
 $('draw-threshold').disabled=disabled||!view?.thresholds_editable;
}
const action=(siid,aiid,input=[])=>control('action',{siid,aiid,in:input});
function renderRegion(values){
 const region=values.get('4/19'),code=values.get('14/3');
 const clear=region==='matched'&&code===200,locked=region==='mismatched'&&code===401;
 $('region-status').textContent=clear?'Блокировка снята':locked?'Региональная блокировка активна':'Статус не подтверждён';
 $('region-values').textContent='Регион: '+(region??'недоступен')+' · код: '+(code??'недоступен');
}
async function changeRegion(){
 if(busy)return;
 const code=Number($('region-code').value);busy=true;disableControls(true);
 $('region-result').textContent='Отправка команды…';
 try{
  const result=await api('local.region_lock',{code});
  if(result?.dispatched!==true)throw Error('Сервис не подтвердил отправку команды');
  let confirmed=false;
  for(let attempt=0;attempt<4;attempt++){
   if(attempt)await new Promise(resolve=>setTimeout(resolve,1000));
   const rows=await api('get_properties',[{siid:4,piid:19},{siid:14,piid:3}]);
   if(!Array.isArray(rows))throw Error('Некорректный ответ статуса');
   const values=new Map(rows.filter(r=>r.code===0&&'value'in r).map(r=>[r.siid+'/'+r.piid,r.value]));
   renderRegion(values);
   confirmed=values.get('14/3')===code&&values.get('4/19')===(code===200?'matched':'mismatched');
   if(confirmed)break;
  }
  $('region-result').textContent=confirmed?'Изменение подтверждено состоянием робота.':'Команда отправлена, но нужный статус пока не подтверждён. Повтори проверку состояния.';
 }catch(e){$('region-result').textContent=e.message;toast(e.message,true);}
 finally{busy=false;disableControls(false);}
}
$('save-region').onclick=changeRegion;
function renderNetwork(wifi,guard){
 const network=wifi.ssid||'Не подключён',signal=Number.isFinite(wifi.signal_percent)?' · ~'+wifi.signal_percent+'%':'';
 $('wifi-summary').textContent=network+signal;$('wifi-details').open=$('wifi-details').open||wifi.ap_mode||captiveSetup;
 $('wifi-status').textContent='Сеть: '+network+signal+(Number.isFinite(wifi.signal_dbm)?' ('+wifi.signal_dbm+' dBm)':'')+' · '+(wifi.ap_mode?'Режим точки доступа':'Адреса: '+wifi.addresses.join(', '))+' · '+({idle:'Ожидание',connecting:'Подключение к сети',connected:'Wi-Fi подключён',failed:'Ошибка подключения'}[wifi.state]||wifi.state)+(wifi.error?' · '+wifi.error:'')+(wifi.ap_mode?' · портал: '+(wifi.portal_http&&wifi.portal_dns?'работает':wifi.portal_error||'запускается'):'');
 if(!guardDirty)$('region-guard').checked=guard.enabled;
 $('guard-status').textContent=guard.enabled?(guard.hook_loaded?'Защита включена'+(guard.blocked_since_boot?' · блокирующий вызов был отклонён':''):'Защита сохранена. Перезагрузи робот для загрузки обработчика.'):(guard.hook_loaded?'Защита выключена':'Защита выключена. Обработчик загрузится после перезагрузки робота.');
}
$('region-guard').onchange=()=>{guardDirty=true;};
$('save-guard').onclick=async()=>{
 if(busy)return;busy=true;disableControls(true);
 try{const enabled=$('region-guard').checked,result=await api('local.region_guard',{enabled});if(result.enabled!==enabled)throw Error('Не удалось сохранить режим');guardDirty=false;toast(enabled?'Режим защиты сохранён. Для снятия текущей блокировки примени код 200.':'Защита выключена');}
 catch(e){toast(e.message,true);}finally{busy=false;disableControls(false);await refresh();}
};
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
  const pairs=[[2,1],[2,2],[3,1],[4,1],[4,4],[4,5],[4,19],[14,3],[4,25],[7,1]];
  const rows=await api('get_properties',pairs.map(([siid,piid])=>({siid,piid})));
  if(epoch!==viewEpoch||!token)return;
  if(!Array.isArray(rows))throw Error('Некорректный ответ статуса');
  properties=new Map(rows.filter(r=>r.code===0&&r.siid&&r.piid&&'value'in r).map(r=>[r.siid+'/'+r.piid,r.value]));
  const value=k=>properties.get(k);
  $('status').textContent=statusNames[value('4/1')]||('Статус '+(value('4/1')??'недоступен'));
  $('battery').textContent=value('3/1')===undefined?'—':value('3/1')+'%';
  $('updated').textContent='Обновлено '+new Date().toLocaleTimeString();
  if(!fanDirty&&document.activeElement!==$('fan')&&value('4/4')!==undefined)$('fan').value=String(value('4/4'));
  if(!volumeDirty&&document.activeElement!==$('volume')&&value('7/1')!==undefined){$('volume').value=value('7/1');$('volume-value').textContent=value('7/1')+'%';}
  if(!roomOptionsDirty){if(Number.isInteger(value('4/4')))$('room-fan').value=value('4/4');const water=value('4/5');if(Number.isInteger(water)&&water>=1&&water<=3)$('room-water').value=water;}
  $('station').textContent=value('4/25')===undefined?'Состояние станции недоступно':'Состояние станции: '+value('4/25');
  renderRegion(properties);
  const wifi=await api('local.wifi_status'),guard=await api('local.region_guard_status');if(epoch!==viewEpoch||!token)return;renderNetwork(wifi,guard);
  $('diagnostics').textContent='Регион: '+(value('4/19')??'—')+' · код доступа: '+(value('14/3')??'—')+' · ошибка робота: '+(value('2/2')??'—');
  $('connection').textContent='Подключён';$('connection').classList.remove('connection-offline');$('reconnect').hidden=true;
 }catch(e){if(!e.suspended&&epoch===viewEpoch){$('region-status').textContent='Статус недоступен: нет связи';connectionLost(e.message);}}finally{if(epoch===viewEpoch)refreshing=false;}
}
function invalidateEdit(){pending=null;$('map-preview').textContent='';$('apply-map-edit').disabled=true;}
function selectRoom(id){
 if($('map-operation').value==='split')selected=new Set(selected.has(id)?[]:[id]);
 else if(selected.has(id))selected.delete(id);else selected.add(id);
 points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);
}
function renderRoomButtons(){
 $('rooms').replaceChildren();$('room-selection').textContent=selected.size?'Выбрано: '+(view?.rooms||[]).filter(r=>selected.has(r.id)).map(r=>r.name).join(', '):'Выбери комнаты на карте или кнопками ниже.';
 for(const room of view?.rooms||[]){const b=document.createElement('button');b.textContent=room.name+' · '+room.id;b.classList.toggle('selected',selected.has(room.id));b.setAttribute('aria-pressed',String(selected.has(room.id)));b.onclick=()=>selectRoom(room.id);$('rooms').append(b);}
}
function draw(){
 ctx.clearRect(0,0,canvas.width,canvas.height);if(!view||view.empty)return;
 const grid=view.grid,scale=3,buffer=document.createElement('canvas');buffer.width=view.width;buffer.height=view.height;const bc=buffer.getContext('2d'),image=bc.createImageData(view.width,view.height);
 for(let i=0;i<grid.length;i++){
  const id=grid[i];if(!id)continue;const j=((view.height-1-Math.floor(i/view.width))*view.width+i%view.width)*4;
  const color=id===255?'#d1d9e2':palette[(id-1)%palette.length];let rgb=color.slice(1).match(/../g).map(c=>parseInt(c,16));
  if(id!==255&&selected.size&&!selected.has(id))rgb=rgb.map(c=>Math.round(c*.42));
  image.data.set([...rgb,255],j);
 }
 bc.putImageData(image,0,0);ctx.imageSmoothingEnabled=false;ctx.drawImage(buffer,0,0,canvas.width,canvas.height);
 ctx.font='bold 12px system-ui';ctx.textAlign='center';ctx.textBaseline='middle';
 for(const room of view.rooms){const x=(room.x+.5)*scale,y=(view.height-room.y-.5)*scale;ctx.lineWidth=4;ctx.strokeStyle='#17232d';ctx.strokeText(String(room.id),x,y);ctx.fillStyle='#fff';ctx.fillText(String(room.id),x,y);}
 drawThresholds();
 if(points.length){ctx.strokeStyle='#ffae67';ctx.fillStyle='#ffae67';ctx.lineWidth=2;for(const p of points){ctx.beginPath();ctx.arc(p.x*scale,(view.height-p.y)*scale,4,0,Math.PI*2);ctx.fill();}if(points.length===2){ctx.beginPath();ctx.moveTo(points[0].x*scale,(view.height-points[0].y)*scale);ctx.lineTo(points[1].x*scale,(view.height-points[1].y)*scale);ctx.stroke();}}
}
async function refreshMap(){
 if(!token||busy)return;const epoch=viewEpoch;
 try{
  const next=await api('local.map_view');if(epoch!==viewEpoch||!token)return;
  if(next.empty){view=next;selected.clear();points=[];thresholdDraft=null;thresholdDrawing=false;invalidateEdit();$('map-info').textContent='Сохранённая карта ещё не создана';renderRoomButtons();renderThresholdChoices();draw();disableControls(busy);return;}
  if(!Number.isInteger(next.width)||!Number.isInteger(next.height)||next.width*next.height>4194304||!Array.isArray(next.rooms))throw Error('Некорректный формат карты');
  const bytes=atob(next.pixels);if(bytes.length!==next.width*next.height)throw Error('Некорректный размер карты');
  next.grid=Uint8Array.from(bytes,c=>c.charCodeAt(0));
  if(view?.revision!==next.revision){selected=view?.map_id===next.map_id?new Set([...selected].filter(id=>next.rooms.some(r=>r.id===id))):new Set();points=[];invalidateEdit();thresholdDraft=null;thresholdDrawing=false;}
  view=next;canvas.width=view.width*3;canvas.height=view.height*3;
  $('map-info').textContent='Карта '+view.map_id+' · комнат: '+view.rooms.length+' · '+view.width+' × '+view.height;
  renderRoomButtons();renderThresholdChoices();draw();disableControls(busy);
 }catch(e){if(!e.suspended&&epoch===viewEpoch){$('map-info').textContent=e.message;connectionLost(e.message);}}
}
canvas.onclick=e=>{
 if(!view||view.empty||busy)return;
 const rect=canvas.getBoundingClientRect(),x=(e.clientX-rect.left)*view.width/rect.width,y=view.height-(e.clientY-rect.top)*view.height/rect.height;
 if(thresholdDrawing){addThresholdPoint(x,y);return;}
 if($('threshold-details').open&&selectThresholdNear(x,y))return;
 if($('map-operation').value==='split'&&selected.size===1){if(points.length===2)points=[];points.push({x,y,robot:pixelToRobot(view,x,y)});invalidateEdit();draw();return;}
 const px=Math.min(view.width-1,Math.max(0,Math.floor(x))),py=Math.min(view.height-1,Math.max(0,Math.floor(y))),id=view.grid[py*view.width+px];
 if(view.rooms.some(r=>r.id===id))selectRoom(id);
};
async function login(){
 const epoch=viewEpoch,loginToken=token,info=await api('miIO.info');if(epoch!==viewEpoch||loginToken!==token)return;if(captiveSetup)$('dashboard').append($('wifi-panel'));$('model').textContent=info.model+' · '+info.fw_ver;
 $('login').hidden=true;$('dashboard').hidden=false;$('logout').hidden=false;$('login-error').textContent='';
 sessionStorage.setItem('dreame-token',token);
 await refresh();await refreshMap();clearInterval(timer);timer=setInterval(()=>refresh(),5000);
}
$('login-form').onsubmit=async e=>{e.preventDefault();token=$('token').value.trim();try{await login();if($('remember').checked)localStorage.setItem('dreame-token',token);else localStorage.removeItem('dreame-token');$('token').value='';}catch(e){$('login-error').textContent=e.message;}};
$('logout').onclick=()=>{viewEpoch++;for(const read of activeReads)read.abort('logout');refreshing=false;clearInterval(timer);timer=null;token='';sessionStorage.removeItem('dreame-token');localStorage.removeItem('dreame-token');$('dashboard').hidden=true;$('login').hidden=false;$('logout').hidden=true;};
for(const button of document.querySelectorAll('[data-command]'))button.onclick=async()=>{
 try{switch(button.dataset.command){
  case 'reboot':await rebootRobot();break;
  case 'start':await action(2,1);break;case 'pause':await action(2,2);break;case 'stop':await action(4,2);break;case 'dock':await action(3,1);break;case 'locate':await action(7,1);break;
  case 'mapping':await action(4,1,[{piid:1,value:21}]);break;case 'empty':await action(15,1);break;
  case 'wash':await action(4,4,[{piid:10,value:'2,1'}]);break;case 'dry':await action(4,4,[{piid:10,value:'3,1'}]);break;case 'stop-dry':await action(4,4,[{piid:10,value:'3,0'}]);break;
 }}catch{}
};
$('fan').onchange=()=>{fanDirty=true;};
$('save-fan').onclick=async()=>{try{await control('set_properties',[{siid:4,piid:4,value:Number($('fan').value)}]);fanDirty=false;}catch{}};
$('volume').oninput=()=>{volumeDirty=true;$('volume-value').textContent=$('volume').value+'%';};
$('save-volume').onclick=async()=>{try{await control('set_properties',[{siid:7,piid:1,value:Number($('volume').value)}]);volumeDirty=false;}catch{}};
$('refresh-map').onclick=()=>refreshMap();
$('clear-selection').onclick=()=>{selected.clear();points=[];invalidateEdit();renderRoomButtons();draw();disableControls(busy);};
$('clean-rooms').onclick=async()=>{try{const ids=[...selected],mapID=view?.map_id;const command=roomCommand(ids,Number($('room-fan').value),Number($('room-water').value),Number($('room-repeats').value));await control('action',command,async()=>{const current=await api('local.map_view');if(current.map_id!==mapID||!ids.every(id=>current.rooms.some(r=>r.id===id)))throw Error('Комнаты изменились. Обнови карту и выбери их заново.');});}catch(e){toast(e.message,true);}};
for(const id of ['room-fan','room-water','room-repeats'])$(id).onchange=()=>{roomOptionsDirty=true;};
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

export function decodeSetting(def,values){
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
 }else if(def.kind==='bool'){input.type='checkbox';input.checked=!!value;}
 else{input.type=def.kind==='number'?'number':def.kind==='time'?'time':'text';input.value=String(value);if(def.kind==='number'){input.min=def.min;input.max=def.max;input.step=def.step||1;}else input.maxLength=128;}
 return input;
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
    input=document.createElement('div');input.className='setting-window';const enabled=document.createElement('input'),start=document.createElement('input'),end=document.createElement('input');enabled.type='checkbox';enabled.setAttribute('aria-label',def.label+' включено');start.type=end.type='time';start.setAttribute('aria-label',def.label+' начало');end.setAttribute('aria-label',def.label+' окончание');input.append(enabled,start,end);row.setInput=v=>{enabled.checked=v.enabled;start.value=v.start;end.value=v.end;};readInput=()=>({enabled:enabled.checked,start:start.value,end:end.value});
   }else{input=settingControl(def,value);row.setInput=v=>{if(def.kind==='bool')input.checked=!!v;else input.value=String(def.auto&&v<0&&def.options?.some(x=>x.value===0)?0:v);};readInput=()=>def.kind==='bool'?input.checked:def.kind==='select'?def.options.find(x=>String(x.value)===input.value)?.value??value:def.kind==='number'?Number(input.value):input.value;}
   row.setInput(value);input.addEventListener('input',()=>settingsDirty.add(def.id));input.addEventListener('change',()=>settingsDirty.add(def.id));
   const apply=document.createElement('button');apply.textContent='Применить';apply.className='setting-apply';apply.onclick=async()=>{
    try{
     const chosen=readInput();if(def.kind==='number'&&(!Number.isInteger(chosen)||chosen<def.min||chosen>def.max))throw Error('Допустимое значение: '+def.min+'–'+def.max);
     if(def.kind==='time'&&!/^([01]\d|2[0-3]):[0-5]\d$/.test(chosen))throw Error('Укажи время в формате ЧЧ:ММ');
     const raw=settingsSnapshot.get(def.siid+'/'+def.piid),value=settingValue(def,chosen,raw);
     await control('local.setting',{siid:def.siid,piid:def.piid,expected:raw,value});settingsDirty.delete(def.id);await refreshSettings();
    }catch(e){toast(e.message,true);}
   };
   row.append(label,input,apply);settingsRows.set(def.id,row);$('settings-list').append(row);
  }else if(!settingsDirty.has(def.id))row.setInput(value);
  row.querySelector('.setting-current').textContent='Сейчас: '+(typeof value==='object'?(value.enabled?'включено':'выключено')+' '+value.start+'–'+value.end:typeof value==='boolean'?(value?'включено':'выключено'):String(value));
  row.hidden=!def.label.toLocaleLowerCase().includes(query);
 }
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
$('settings-details').ontoggle=()=>{if($('settings-details').open)refreshSettings();};$('refresh-settings').onclick=()=>refreshSettings();$('settings-search').oninput=()=>renderSettings();

async function rebootRobot(){
 if(busy||!window.confirm('Перезагрузить робот? Связь временно пропадёт.'))return;
 busy=true;disableControls(true);
 try{const result=await api('local.reboot');if(!result.accepted)throw Error('Запрос перезагрузки не принят');rebootingUntil=Date.now()+90000;connectionLost('Робот перезагружается');toast('Перезагрузка запрошена. Дождись восстановления связи.');}
 catch(e){toast(e.message,true);}finally{busy=false;disableControls(false);}
}
function suspendReads(reason='suspend'){viewEpoch++;refreshing=false;settingsLoading=false;for(const controller of activeReads)controller.abort(reason);}
function scheduleResume(){clearTimeout(resumeTimer);resumeTimer=setTimeout(async()=>{
 if(document.hidden||!token)return;if(busy){scheduleResume();return;}
 suspendReads();
 if($('dashboard').hidden){try{await login();}catch(e){if(!e.suspended)connectionLost(e.message);}return;}
 await refresh();await refreshMap();if($('settings-details').open)await refreshSettings();
 },250);}
$('reconnect').onclick=scheduleResume;
document.addEventListener('visibilitychange',()=>{if(document.hidden)suspendReads();else scheduleResume();});
window.addEventListener('online',scheduleResume);window.addEventListener('pageshow',scheduleResume);window.addEventListener('focus',scheduleResume);
