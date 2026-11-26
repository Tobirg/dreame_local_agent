// Voice panel: the robot's voices (built-in and uploaded), switching, uploading a pack and the
// phrase list with listening in the browser and on the robot.
import {phrases} from '/phrases.js';

const esc=s=>String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const kb=b=>b?Math.round(b/1024)+' КБ':'';

export async function mountVoice(el,{api,token,toast}){
 let state=await api('/api/v1/voice'),numbers=[],listenID='';
 el.innerHTML=`<p class="muted small" data-r="current"></p><div class="module-list" data-r="list"></div>
<details><summary>Загрузить пакет озвучки</summary><p class="muted small">Архив .tar.gz с файлами <code>N.ogg</code> (номер — фраза, см. «Фразы») и, по желанию, <code>tts.json</code> для ответов голосового помощника. Код — латиницей, например RU; встроенные коды (EN, JA, KO, ZH) заняты прошивкой. Пакет с тем же кодом заменяет прежний.</p>
<label>Код голоса<input data-r="code" maxlength="8" autocomplete="off" placeholder="RU"></label><label>Пакет<input data-r="file" type="file" accept=".gz,.tgz,application/gzip"></label><button data-r="upload">Загрузить</button></details>
<details data-r="phrases-box"><summary>Фразы</summary><p class="muted small">Номер файла и что робот говорит. Текст известен не для всех номеров: остальные можно прослушать (▶ — здесь, в браузере; 🔊 — на роботе текущим голосом).</p>
<div class="row"><label>Голос<select data-r="listen"></select></label><input data-r="search" type="search" placeholder="Номер или слово" aria-label="Поиск фразы"></div><div class="module-list" data-r="phrases"></div></details>
<p class="muted small" data-r="status" role="status"></p>`;
 const $=n=>el.querySelector(`[data-r="${n}"]`);
 const busy=on=>el.querySelectorAll('button').forEach(b=>b.disabled=on);
 async function act(path,body,done){
  busy(true);$('status').textContent='…';
  try{state=await api(path,body);render();$('status').textContent=done;}
  catch(e){$('status').textContent=e.message;toast(e.message,true);}
  finally{busy(false);}
 }
 function render(){
  $('current').textContent=state.current?'Сейчас робот говорит голосом '+state.current+'.':'Текущий голос неизвестен.';
  $('list').innerHTML=state.voices.map(v=>`<div class="module-row"><div><b>${esc(v.id)}</b> <span class="muted small">${v.builtin?'встроенный':'загружен'} · ${v.files} фраз ${kb(v.bytes)}${v.tts?' · tts.json':''}</span></div><div class="row">${v.id===state.current?'<span class="tag">используется</span>':`<button data-use="${esc(v.id)}">Включить</button>`}${!v.builtin&&v.id!==state.current?`<button class="ghost" data-remove="${esc(v.id)}">Удалить</button>`:''}</div></div>`).join('');
  for(const b of el.querySelectorAll('[data-use]'))b.onclick=()=>act('/api/v1/voice/use',{id:b.dataset.use},'Голос '+b.dataset.use+' включён');
  for(const b of el.querySelectorAll('[data-remove]'))b.onclick=()=>{if(confirm('Удалить голос '+b.dataset.remove+'?'))act('/api/v1/voice/remove',{id:b.dataset.remove},'Голос удалён');};
  const keep=$('listen').value||(state.voices.some(v=>v.id==='EN')?'EN':state.current);
  $('listen').innerHTML=state.voices.map(v=>`<option${v.id===keep?' selected':''}>${esc(v.id)}</option>`).join('');
 }
 async function loadNumbers(){
  listenID=$('listen').value;
  try{numbers=await api('/api/v1/voice/phrases?id='+encodeURIComponent(listenID));}catch(e){numbers=[];$('status').textContent=e.message;}
  renderPhrases();
 }
 function renderPhrases(){
  const q=$('search').value.trim().toLowerCase();
  const rows=numbers.filter(n=>!q||String(n)===q||(phrases[n]||'').toLowerCase().includes(q));
  $('phrases').innerHTML=rows.map(n=>`<div class="module-row"><div><b>${n}</b> <span class="muted small">${esc(phrases[n]||'—')}</span></div><div class="row"><button class="ghost" data-hear="${n}" aria-label="Прослушать ${n} здесь">▶</button><button class="ghost" data-say="${n}" aria-label="Сказать ${n} на роботе">🔊</button></div></div>`).join('')||'<p class="muted small">Нет фраз</p>';
  for(const b of el.querySelectorAll('[data-hear]'))b.onclick=async()=>{
   try{
    const r=await fetch(`/api/v1/voice/phrase?id=${encodeURIComponent(listenID)}&n=${b.dataset.hear}`,{headers:{'Authorization':'Bearer '+token()},credentials:'omit',cache:'no-store'});
    if(!r.ok)throw Error('Файла нет');
    const url=URL.createObjectURL(await r.blob()),audio=new Audio(url);audio.onended=()=>URL.revokeObjectURL(url);await audio.play();
   }catch(e){toast(e.message,true);}
  };
  for(const b of el.querySelectorAll('[data-say]'))b.onclick=()=>api('/api/v1/voice/play',{number:Number(b.dataset.say)}).catch(e=>toast(e.message,true));
 }
 $('upload').onclick=async()=>{
  const code=$('code').value.trim().toUpperCase(),file=$('file').files[0];
  if(!code||!file){toast('Укажи код и файл пакета',true);return;}
  busy(true);$('status').textContent='Загружаю…';
  try{
   const r=await fetch('/api/v1/voice/install?id='+encodeURIComponent(code),{method:'POST',headers:{'Authorization':'Bearer '+token(),'Content-Type':'application/octet-stream'},body:file,credentials:'omit',cache:'no-store'});
   const body=await r.json();if(!r.ok)throw Error(body.error||'Ошибка HTTP '+r.status);
   state=body.result;render();
   $('status').textContent='Голос '+code+' загружен'+(body.result.skipped?.length?'; пропущены лишние файлы: '+body.result.skipped.join(', '):'')+'. Нажми «Включить».';
  }catch(e){$('status').textContent=e.message;}finally{busy(false);}
 };
 $('listen').onchange=loadNumbers;
 $('search').oninput=renderPhrases;
 $('phrases-box').addEventListener('toggle',()=>{if($('phrases-box').open&&!numbers.length)loadNumbers();});
 render();
}
