// Camera monitoring screen, laid out like the Dreame app's video page: the live picture over the whole
// screen, the robot's status on top, a mini map, light and flip on the left, a drive pad bottom left
// and photo, video, pet search and dock on the right. Frames come from the camera module (action
// "live"); driving and pet search go through the agent, which keeps the robot's video monitoring
// session open so it drives without the vacuum motor (monitor.go).
const html=`<header class="mon-head"><button class="mon-btn" data-m="back" aria-label="Закрыть мониторинг"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 5l-7 7 7 7"/></svg></button><h2 data-m="title">Мониторинг</h2><button class="mon-btn" data-sheet="camera" aria-label="Поездка и патруль" title="Поездка и патруль"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="6" cy="18" r="2"/><circle cx="18" cy="6" r="2"/><path d="M8 18h6a3 3 0 0 0 0-6h-4a3 3 0 0 1 0-6h6"/></svg></button></header>
<img data-m="img" alt="Камера робота вживую" hidden><p data-m="note" class="mon-note" role="status">Включаю камеру…</p>
<canvas data-m="mini" class="mon-mini" width="300" height="220" aria-label="Мини-карта"></canvas>
<div class="mon-left"><button class="mon-btn" data-m="light" aria-pressed="false" aria-label="Подсветка" title="Подсветка"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 3h8v4l-2 3v10h-4V10L8 7z"/><path d="M12 13v2"/></svg></button><button class="mon-btn" data-m="flip" aria-pressed="false" aria-label="Перевернуть изображение" title="Перевернуть изображение"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 9h15l-4-4M20 15H5l4 4"/></svg></button></div>
<div class="mon-pad" data-m="pad"><button data-move="200,0" aria-label="Вперёд"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 15l6-6 6 6"/></svg></button><button data-move="0,45" aria-label="Поворот влево"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 14 4 9l5-5"/><path d="M4 9h10a6 6 0 0 1 0 12h-3"/></svg></button><button data-move="0,-45" aria-label="Поворот вправо"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 14 5-5-5-5"/><path d="M20 9H10a6 6 0 0 0 0 12h3"/></svg></button><button data-move="0,180" aria-label="Разворот"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 12a8 8 0 1 0 2.3-5.6L4 9"/><path d="M4 4v5h5"/></svg></button></div>
<div class="mon-right"><button class="mon-btn big" data-m="photo" aria-label="Снимок" title="Снимок"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 8h3l2-3h6l2 3h3v11H4z"/><circle cx="12" cy="13" r="3.5"/></svg></button><button class="mon-btn big" data-m="rec" aria-pressed="false" aria-label="Запись видео" title="Запись видео"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="7" width="12" height="10" rx="2"/><path d="m15 10 6-3v10l-6-3"/></svg></button><button class="mon-btn big" data-m="pet" aria-label="Найти питомца" title="Найти питомца"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 4v4M4 4h4M20 4v4M20 4h-4M4 20v-4M4 20h4M20 20v-4M20 20h-4"/><path d="M8 10l1-3 2 2h2l2-2 1 3v3a4 4 0 0 1-8 0z"/></svg></button><button class="mon-btn big" data-m="dock" aria-label="На базу" title="На базу"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M13 3 6 13h5l-1 8 7-10h-5z"/></svg></button></div>`;

export const moves={ArrowUp:[200,0],ArrowLeft:[0,45],ArrowRight:[0,-45],ArrowDown:[0,180]}; // the app's VideoRemoteView values
const pace=1000; // the app repeats the held direction once a second

export function mountMonitor({api,command,rpc,token,toast,statusText,mapCanvas,onClose}){
 const root=document.createElement('section');root.id='monitor';root.className='monitor';root.hidden=true;root.setAttribute('role','dialog');root.setAttribute('aria-label','Мониторинг камеры');root.innerHTML=html;document.body.append(root);
 const $=n=>root.querySelector(`[data-m="${n}"]`),img=$('img'),note=$('note');
 let open=false,flip=false,frame=null,stream=null,miniTimer=0,driving=null,driveTimer=0,rec=null;
 try{flip=localStorage.getItem('dreame-monitor-flip')==='1';}catch{}
 const showFlip=()=>{img.classList.toggle('flipped',flip);$('flip').setAttribute('aria-pressed',String(flip));};showFlip();
 const say=t=>{note.textContent=t;note.hidden=!t;};
 // The robot's monitoring session (needed for driving without the motor) makes the firmware encode
 // and send video to its cloud. With the cloud protection on, the screen opens it at once, as the
 // app does (voice, the firmware's light), and keeps it alive while open; without it, the agent opens
 // it on the first drive or pet search only. The screen ends it on exit.
 let alive=null;
 const session=()=>{clearInterval(alive);alive=null;return rpc('video_monitor',{enabled:false}).catch(()=>{});};
 async function watchSession(){
  let guarded=false;
  try{guarded=(await api('/api/v1/system/video_cloud')).enabled===true;}catch{} // control tokens cannot read it
  if(!guarded||!open)return;
  const ping=()=>rpc('video_monitor',{enabled:true}).catch(e=>{clearInterval(alive);alive=null;say(e.message);});
  ping();alive=setInterval(ping,10000);
 }
 // Frames: one long GET of /api/v1/camera/stream (multipart, each new JPEG as the module captures it).
 const enc=new TextEncoder(),dec=new TextDecoder(),gap=enc.encode('\r\n\r\n');
 function find(buf,pat,from=0){outer:for(let i=from;i<=buf.length-pat.length;i++){for(let j=0;j<pat.length;j++)if(buf[i+j]!==pat[j])continue outer;return i;}return -1;}
 function show(bytes){
  const url=URL.createObjectURL(new Blob([bytes],{type:'image/jpeg'})),old=img.src;
  img.onload=()=>{if(old.startsWith('blob:'))URL.revokeObjectURL(old);if(rec)rec.draw();};
  img.src=url;img.hidden=false;frame=Date.now();say('');
 }
 async function frames(){
  while(open){
   const ctl=new AbortController();stream=ctl;
   try{
    const res=await fetch('/api/v1/camera/stream',{headers:{'Authorization':'Bearer '+token()},credentials:'omit',cache:'no-store',signal:ctl.signal});
    if(res.status===404){say('Установи модуль камеры (Модули), чтобы видеть картинку.');return;}
    if(res.status===403){say('Этот токен не может смотреть камеру.');return;}
    if(!res.ok)throw Error('Камера: ошибка '+res.status);
    const reader=res.body.getReader();let buf=new Uint8Array(0);
    for(;;){
     const {value,done}=await reader.read();if(done)break;
     const next=new Uint8Array(buf.length+value.length);next.set(buf);next.set(value,buf.length);buf=next;
     for(;;){
      const end=find(buf,gap);if(end<0)break;
      const head=dec.decode(buf.subarray(0,end)),len=Number(/Content-Length:\s*(\d+)/i.exec(head)?.[1]);
      if(!Number.isFinite(len)){buf=new Uint8Array(0);break;}
      if(buf.length<end+4+len)break;
      const body=buf.slice(end+4,end+4+len);buf=buf.slice(Math.min(buf.length,end+4+len+2));
      if(/image\/jpeg/i.test(head))show(body);
      else try{const s=JSON.parse(dec.decode(body));if(!frame||Date.now()-frame>2500)say(s.reason||(s.state==='error'?'Камера не отвечает':'Включаю камеру…'));}catch{}
     }
    }
   }catch(e){if(!open)return;say(e.message);}
   if(open)await new Promise(r=>setTimeout(r,1000));
  }
 }
 function mini(){
  const c=$('mini'),g=c.getContext('2d');g.clearRect(0,0,c.width,c.height);
  if(!mapCanvas.width||!mapCanvas.height)return;
  const k=Math.min(c.width/mapCanvas.width,c.height/mapCanvas.height);g.drawImage(mapCanvas,(c.width-mapCanvas.width*k)/2,(c.height-mapCanvas.height*k)/2,mapCanvas.width*k,mapCanvas.height*k);
 }
 const title=()=>{$('title').textContent=statusText()||'Мониторинг';};
 // Driving: send at once, repeat while held, stop with 0,0 on release, as the app does.
 const step=()=>rpc('remote_move',{velocity:driving[0],rotation:driving[1]}).catch(e=>{toast(e.message+(/code|код/i.test(e.message)?' Задай код в «Поездка и патруль» → «Код доступа к видео».':''),true);drive(null);});
 function drive(move){
  const was=driving;clearInterval(driveTimer);driving=move;
  if(move){step();driveTimer=setInterval(step,pace);}
  else if(was&&(was[0]||was[1]))rpc('remote_move',{velocity:0,rotation:0}).catch(()=>{});
 }
 for(const b of root.querySelectorAll('[data-move]')){
  const move=b.dataset.move.split(',').map(Number);
  b.onpointerdown=e=>{e.preventDefault();b.setPointerCapture?.(e.pointerId);drive(move);};
  b.onpointerup=b.onpointercancel=()=>drive(null);
 }
 document.addEventListener('keydown',e=>{if(!open||e.repeat||e.target.closest?.('input,select,textarea'))return;if(e.key==='Escape'){close();return;}if(moves[e.key]){e.preventDefault();drive(moves[e.key]);}});
 document.addEventListener('keyup',e=>{if(open&&moves[e.key]&&driving)drive(null);});
 $('flip').onclick=()=>{flip=!flip;try{localStorage.setItem('dreame-monitor-flip',flip?'1':'0');}catch{}showFlip();};
 $('light').onclick=async()=>{const on=$('light').getAttribute('aria-pressed')!=='true';try{const s=await api('/api/v1/m/camera/light',{on});$('light').setAttribute('aria-pressed',String(!!s.light));}catch(e){toast(e.message,true);}};
 const stamp=()=>new Date().toISOString().slice(0,19).replace(/[-:T]/g,'');
 function save(href,name){const a=document.createElement('a');a.href=href;a.download=name;document.body.append(a);a.click();a.remove();}
 $('photo').onclick=()=>{if(!frame)return toast('Кадра ещё нет',true);const c=document.createElement('canvas');c.width=img.naturalWidth;c.height=img.naturalHeight;const g=c.getContext('2d');if(flip){g.translate(c.width,c.height);g.rotate(Math.PI);}g.drawImage(img,0,0);save(c.toDataURL('image/jpeg',.9),'robot-'+stamp()+'.jpg');};
 // Video: the frames drawn onto a canvas and recorded by the browser (webm).
 $('rec').onclick=()=>{
  if(rec){rec.stop();return;}
  if(!frame||typeof MediaRecorder==='undefined')return toast(frame?'Браузер не умеет записывать видео':'Кадра ещё нет',true);
  const c=document.createElement('canvas');c.width=img.naturalWidth||864;c.height=img.naturalHeight||480;const g=c.getContext('2d'),chunks=[];
  const draw=()=>{g.save();if(flip){g.translate(c.width,c.height);g.rotate(Math.PI);}g.drawImage(img,0,0,c.width,c.height);g.restore();};draw();
  const r=new MediaRecorder(c.captureStream(15),{mimeType:MediaRecorder.isTypeSupported?.('video/webm')?'video/webm':''});
  r.ondataavailable=e=>{if(e.data.size)chunks.push(e.data);};
  r.onstop=()=>{const url=URL.createObjectURL(new Blob(chunks,{type:'video/webm'}));save(url,'robot-'+stamp()+'.webm');setTimeout(()=>URL.revokeObjectURL(url),60000);};
  r.start(1000);$('rec').setAttribute('aria-pressed','true');
  rec={draw,stop(){rec=null;$('rec').setAttribute('aria-pressed','false');if(r.state!=='inactive')r.stop();}};
 };
 $('pet').onclick=()=>command('find_pet').then(()=>toast('Робот ищет питомца'),e=>toast(e.message,true));
 $('dock').onclick=()=>command('dock').then(()=>toast('Робот едет на базу'),e=>toast(e.message,true));
 function close(){
  if(!open)return;open=false;stream?.abort();drive(null);rec?.stop();clearInterval(miniTimer);root.hidden=true;delete document.body.dataset.monitor;session();onClose?.();
 }
 $('back').onclick=close;
 root.querySelector('[data-sheet="camera"]').addEventListener('click',()=>close(),{capture:true});
 return {
  open(){
   if(open)return;open=true;frame=null;img.hidden=true;say('Включаю камеру…');root.hidden=false;document.body.dataset.monitor='1';
   title();mini();miniTimer=setInterval(()=>{title();mini();},1000);
   api('/api/v1/m/camera/status',{}).then(s=>$('light').setAttribute('aria-pressed',String(!!s.light)),()=>{});
   frames();watchSession();
  },
  close,
  get isOpen(){return open;}
 };
}
