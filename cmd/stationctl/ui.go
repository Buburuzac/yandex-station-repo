package main

// uiHTML — встроенный одностраничный пульт. Без внешних ресурсов: всё инлайном,
// чтобы работало на колонке без интернета. Токен хранится в localStorage.
const uiHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>stationctl — пульт станции</title>
<style>
  :root{--bg:#14161c;--card:#1c1f28;--line:#2a2e3a;--fg:#e6e8ee;--mut:#8b90a0;--acc:#6ea8fe;--ok:#4cd07d;--warn:#e7c14a;--err:#e86a6a;}
  *{box-sizing:border-box}
  body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,Segoe UI,Roboto,sans-serif}
  header{display:flex;align-items:center;gap:10px;padding:14px 18px;border-bottom:1px solid var(--line);position:sticky;top:0;background:var(--bg);z-index:5}
  header h1{font-size:16px;margin:0;font-weight:600}
  header .pill{font-size:12px;color:var(--mut);border:1px solid var(--line);border-radius:999px;padding:2px 10px}
  main{max-width:900px;margin:0 auto;padding:18px;display:grid;gap:16px}
  .card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px}
  .card h2{margin:0 0 12px;font-size:13px;letter-spacing:.3px;text-transform:uppercase;color:var(--mut)}
  .grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:10px}
  .kv{background:#151821;border:1px solid var(--line);border-radius:10px;padding:10px}
  .kv .k{color:var(--mut);font-size:12px}
  .kv .v{font-size:18px;font-weight:600;margin-top:2px}
  button{font:inherit;color:var(--fg);background:#262b36;border:1px solid var(--line);border-radius:9px;padding:9px 14px;cursor:pointer}
  button:hover{border-color:var(--acc)}
  button.acc{background:#223457;border-color:#2d4a7a}
  button.danger{background:#3a2020;border-color:#5a2b2b}
  button.danger:hover{border-color:var(--err)}
  .row{display:flex;gap:8px;flex-wrap:wrap;align-items:center}
  input[type=text],input[type=password],input[type=number],textarea{background:#11141b;color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:8px 10px;font:inherit}
  input[type=range]{flex:1}
  textarea{width:100%;min-height:90px;font-family:ui-monospace,Consolas,monospace;font-size:12.5px}
  pre{background:#0e1016;border:1px solid var(--line);border-radius:8px;padding:10px;overflow:auto;max-height:340px;font-size:12px;margin:8px 0 0;white-space:pre-wrap;word-break:break-word}
  select{background:#11141b;color:var(--fg);border:1px solid var(--line);border-radius:8px;padding:8px}
  .mut{color:var(--mut)}
  .toast{position:fixed;bottom:16px;left:50%;transform:translateX(-50%);background:#222;border:1px solid var(--line);padding:10px 16px;border-radius:10px;opacity:0;transition:.2s;pointer-events:none}
  .toast.show{opacity:1}
  a{color:var(--acc)}
</style>
</head>
<body>
<header>
  <h1>🔊 stationctl</h1>
  <span class="pill" id="host">—</span>
  <span class="pill" id="rootpill">—</span>
  <span style="flex:1"></span>
  <button id="tokbtn" title="сменить токен">🔑</button>
</header>
<main>
  <section class="card">
    <h2>Статус</h2>
    <div class="grid" id="statgrid"></div>
    <div class="row" style="margin-top:12px">
      <button onclick="loadStatus()">↻ Обновить</button>
      <button onclick="beep()">🔔 Сигнал (найти)</button>
      <button onclick="led()">💡 LED-цвет</button>
      <label class="mut"><input type="checkbox" id="autoref"> авто 5с</label>
    </div>
  </section>

  <section class="card">
    <h2>Громкость</h2>
    <div class="row">
      <button onclick="vol(-10)">−10</button>
      <input type="range" id="volr" min="0" max="100" step="1" oninput="volLabel(this.value)" onchange="volSet(this.value)">
      <button onclick="vol(10)">+10</button>
      <span class="kv" style="min-width:64px;text-align:center"><span class="v" id="vollbl">—</span>%</span>
    </div>
    <div class="mut" id="volctl" style="margin-top:6px"></div>
  </section>

  <section class="card">
    <h2>Медиа / Алиса <span class="mut" style="font-size:11px">(glagol :1961, experimental)</span></h2>
    <div class="row">
      <button onclick="media('prev')">⏮</button>
      <button class="acc" onclick="media('play')">▶</button>
      <button onclick="media('pause')">⏸</button>
      <button onclick="media('next')">⏭</button>
    </div>
    <div class="row" style="margin-top:8px">
      <input type="text" id="saytext" placeholder="Текст для Алисы…" style="flex:1" onkeydown="if(event.key==='Enter')media('say')">
      <button class="acc" onclick="media('say')">🗣 Сказать</button>
      <button onclick="media('ask')" title="Отправить как команду Алисе">Команда</button>
    </div>
    <div class="mut" id="mediainfo" style="margin-top:6px">Нужен glagol-токен (--glagol-token) или аккаунт-токен (--yandex-token).</div>
  </section>

  <section class="card">
    <h2>Spotify Connect <span class="mut" style="font-size:11px">(librespot, experimental)</span></h2>
    <div class="row" style="margin-bottom:8px">
      <span class="pill" id="spstate">—</span>
      <span class="mut" id="spinfo"></span>
    </div>
    <div class="row">
      <input type="text" id="spname" placeholder="Имя колонки в Spotify" style="flex:1">
      <select id="spdev" title="ALSA-устройство вывода"><option value="">устройство по умолчанию</option></select>
    </div>
    <div class="row" style="margin-top:8px">
      <button class="acc" onclick="spotifyAction('start')">▶ Запустить</button>
      <button class="danger" onclick="spotifyAction('stop')">⏹ Остановить</button>
      <button onclick="spotifyAction('restart')">⟳</button>
      <button onclick="loadSpotify()">↻</button>
    </div>
    <pre id="splog" class="mut" style="max-height:140px">—</pre>
  </section>

  <section class="card">
    <h2>ADB</h2>
    <div class="row" style="margin-bottom:8px">
      <span class="pill" id="adbstate">—</span>
      <span class="mut" id="adbinfo"></span>
    </div>
    <div class="row">
      <button class="acc" onclick="adbSet('enable')">Включить ADB (сеть :5555)</button>
      <button class="danger" onclick="adbSet('disable')">Выключить ADB</button>
      <button onclick="loadADB()">↻</button>
    </div>
    <div class="mut" style="margin-top:6px">Сетевой adbd даёт root-шелл без пароля всем в LAN. Только домашняя сеть.</div>
  </section>

  <section class="card">
    <h2>Логи демонов</h2>
    <div class="row">
      <select id="logsel"></select>
      <input type="number" id="loglines" value="200" min="10" max="5000" style="width:90px">
      <button onclick="loadLog()">Показать хвост</button>
    </div>
    <pre id="logout" class="mut">—</pre>
  </section>

  <section class="card" id="shellcard">
    <h2>Шелл (root)</h2>
    <div class="row" style="margin-bottom:8px">
      <input type="text" id="cmd" placeholder="например: ps | grep quasar" style="flex:1" onkeydown="if(event.key==='Enter')doExec()">
      <button class="acc" onclick="doExec()">Выполнить</button>
    </div>
    <pre id="execout" class="mut">—</pre>
  </section>

  <section class="card">
    <h2>Bluetooth</h2>
    <div class="row" style="margin-bottom:8px">
      <span class="pill" id="btstate">—</span>
      <button onclick="btAction('power_on')">Вкл</button>
      <button onclick="btAction('power_off')">Выкл</button>
      <button onclick="btAction('scan')">Поиск 8с</button>
      <button onclick="loadBT()">↻</button>
    </div>
    <div id="btlist" class="mut">—</div>
  </section>

  <section class="card">
    <h2>Файлы <span class="mut" style="font-size:11px">(/data, логи, vendor/quasar)</span></h2>
    <div class="row" style="margin-bottom:8px">
      <input type="text" id="fpath" placeholder="/data/quasar/daemons_logs" style="flex:1" onkeydown="if(event.key==='Enter')loadFiles(this.value)">
      <button onclick="loadFiles(document.getElementById('fpath').value)">Открыть</button>
    </div>
    <div id="filelist" class="mut">введите путь или откройте корень</div>
  </section>

  <section class="card">
    <h2>Автозапуск</h2>
    <div class="row">
      <span class="pill" id="asstate">—</span>
      <button class="acc" onclick="asAction('install')">Поставить (@reboot)</button>
      <button class="danger" onclick="asAction('uninstall')">Убрать</button>
    </div>
    <div class="mut" id="asline" style="margin-top:6px"></div>
  </section>

  <section class="card">
    <h2>Питание</h2>
    <div class="row">
      <button class="danger" onclick="power('reboot')">⟳ Перезагрузить</button>
      <button class="danger" onclick="power('poweroff')">⏻ Выключить</button>
    </div>
  </section>
</main>
<div class="toast" id="toast"></div>

<script>
const T = () => localStorage.getItem('sc_token') || '';
function setToken(){ const t = prompt('Токен stationctl:', T()); if(t!==null){ localStorage.setItem('sc_token', t.trim()); loadStatus(); loadLogsList(); loadADB(); loadBT(); loadAutostart(); loadSpotify(); } }
document.getElementById('tokbtn').onclick = setToken;

async function api(path, opts){
  opts = opts || {};
  opts.headers = Object.assign({'Content-Type':'application/json'}, opts.headers||{}, {'Authorization':'Bearer '+T()});
  const r = await fetch(path, opts);
  const j = await r.json().catch(()=>({error:'не JSON ('+r.status+')'}));
  if(r.status===401){ toast('Нужен токен — нажми 🔑'); }
  return {status:r.status, j};
}
let toastT;
function toast(m){ const el=document.getElementById('toast'); el.textContent=m; el.classList.add('show'); clearTimeout(toastT); toastT=setTimeout(()=>el.classList.remove('show'),2200); }

function esc(s){ return String(s??'—').replace(/[&<>"']/g, c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); }
function kv(k,v){ return '<div class="kv"><div class="k">'+esc(k)+'</div><div class="v">'+esc(v)+'</div></div>'; }
async function loadStatus(){
  const {status,j} = await api('/api/status');
  if(status!==200){ return; }
  document.getElementById('host').textContent = j.hostname || '—';
  const rp = document.getElementById('rootpill');
  rp.textContent = j.root ? 'root ✓' : 'не root';
  rp.style.color = j.root ? 'var(--ok)' : 'var(--warn)';
  const mem = j.mem||{};
  const memStr = mem.total ? Math.round((mem.total-(mem.avail||mem.free||0))/1024)+' / '+Math.round(mem.total/1024)+' МБ' : '—';
  const g = document.getElementById('statgrid');
  g.innerHTML = [
    kv('device_id', j.device_id||'—'),
    kv('платформа', j.platform||'—'),
    kv('аптайм', j.uptime||'—'),
    kv('темп., °C', j.temp_c ? j.temp_c.toFixed(1) : '—'),
    kv('память', memStr),
    kv('громкость', j.vol_ok ? (j.vol_percent+'%') : 'н/д'),
    kv('маркеры рута', (j.markers&&j.markers.length)? j.markers.length : '0'),
    kv('время', (j.now||'').split(' ')[1]||'—'),
  ].join('');
  const vr=document.getElementById('volr');
  if(j.vol_ok){ vr.disabled=false; vr.value=j.vol_percent; volLabel(j.vol_percent); }
  else { vr.disabled=true; volLabel('—'); }
  document.getElementById('volctl').textContent = j.vol_control ? ('контрол: '+j.vol_control) : 'amixer недоступен';
}

function volLabel(v){ document.getElementById('vollbl').textContent=v; }
async function volSet(v){ const {j}=await api('/api/volume',{method:'POST',body:JSON.stringify({percent:+v})}); if(j.error){ toast(j.error); return; } if(j.percent!=null){ volLabel(j.percent); document.getElementById('volr').value=j.percent; } }
async function vol(d){ const {j}=await api('/api/volume',{method:'POST',body:JSON.stringify({delta:d})}); if(j.error){ toast(j.error); return; } if(j.percent!=null){ volLabel(j.percent); document.getElementById('volr').value=j.percent; } }

async function beep(){ const {j}=await api('/api/beep',{method:'POST'}); toast(j.ok||j.error||''); }

async function loadADB(){
  const {status,j}=await api('/api/adb');
  if(status!==200){ return; }
  const el=document.getElementById('adbstate');
  if(!j.script){ el.textContent='нет activate_adb.sh'; el.style.color='var(--warn)'; }
  else if(j.listening){ el.textContent='ВКЛ · :5555 слушает'; el.style.color='var(--ok)'; }
  else if(j.enabled){ el.textContent='маркер есть, порт не слушает'; el.style.color='var(--warn)'; }
  else { el.textContent='ВЫКЛ'; el.style.color='var(--mut)'; }
  const flags=[]; if(j.quasmodrom)flags.push('quasmodrom'); if(j.sound)flags.push('sound'); if(j.glagol)flags.push('glagol'); if(j.network)flags.push('network');
  document.getElementById('adbinfo').textContent = flags.length?('маркеры: '+flags.join(', ')):'';
}
async function adbSet(action){
  if(action==='enable' && !confirm('Включить сетевой ADB? Это root-доступ без пароля для всей LAN.')){ return; }
  if(action==='disable' && !confirm('Выключить ADB на станции?')){ return; }
  const {status,j}=await api('/api/adb',{method:'POST',body:JSON.stringify({action:action})});
  if(status!==200){ toast(j.error||('ошибка '+status)); }
  else { toast(action==='enable'?'ADB включается…':'ADB выключается…'); }
  setTimeout(loadADB, 1200);
}

async function media(cmd){
  let payload={command:cmd};
  if(cmd==='say'||cmd==='ask'){ const t=document.getElementById('saytext').value.trim(); if(!t){ return; } payload.text=t; }
  const {status,j}=await api('/api/media',{method:'POST',body:JSON.stringify(payload)});
  const el=document.getElementById('mediainfo');
  if(status!==200){ el.textContent=j.error||('ошибка '+status); el.style.color='var(--warn)'; return; }
  el.style.color=''; el.textContent='ок: '+cmd+(payload.text?(' · "'+payload.text+'"'):'');
}

async function loadSpotify(){
  const {status,j}=await api('/api/spotify');
  const st=document.getElementById('spstate'), info=document.getElementById('spinfo');
  if(status!==200){ st.textContent='ошибка'; return; }
  if(!j.present){ st.textContent='нет librespot'; st.style.color='var(--warn)'; info.textContent='поставь пакет librespot или --spotify-bin'; }
  else if(j.running){ st.textContent='играет'; st.style.color='var(--ok)'; info.textContent='pid '+j.pid+(j.uptime_sec?(' · '+j.uptime_sec+'с'):''); }
  else { st.textContent='остановлен'; st.style.color='var(--mut)'; info.textContent=j.bin||''; }
  const sel=document.getElementById('spdev'); const cur=sel.value;
  sel.innerHTML='<option value="">устройство по умолчанию</option>';
  (j.alsa_devices||[]).forEach(d=>{ const o=document.createElement('option'); o.value=d; o.textContent=d; sel.appendChild(o); });
  sel.value=cur;
  const lg=document.getElementById('splog'); if(j.log){ lg.classList.remove('mut'); lg.textContent=j.log; }
}
async function spotifyAction(action){
  const body={action:action, name:document.getElementById('spname').value.trim(), device:document.getElementById('spdev').value};
  const {status,j}=await api('/api/spotify',{method:'POST',body:JSON.stringify(body)});
  toast(status===200?(action+': ок'):(j.error||('ошибка '+status)));
  setTimeout(loadSpotify, 1200);
}

async function loadBT(){
  const {status,j}=await api('/api/bt');
  const st=document.getElementById('btstate');
  if(status!==200||!j.available){ st.textContent='нет bluetoothctl'; st.style.color='var(--warn)'; return; }
  st.textContent = j.powered?'вкл':'выкл'; st.style.color = j.powered?'var(--ok)':'var(--mut)';
  const conn=new Set((j.connected||[]).map(d=>d.mac));
  const all=(j.paired||[]).concat((j.connected||[]).filter(d=>!(j.paired||[]).some(p=>p.mac===d.mac)));
  const box=document.getElementById('btlist');
  if(!all.length){ box.textContent='нет спаренных устройств'; return; }
  box.classList.remove('mut'); box.innerHTML='';
  all.forEach(d=>{
    const row=document.createElement('div'); row.className='row'; row.style.margin='4px 0';
    const c=conn.has(d.mac);
    row.innerHTML='<span style="flex:1">'+esc(d.name)+' <span class="mut">'+esc(d.mac)+'</span>'+(c?' <span style="color:var(--ok)">●</span>':'')+'</span>';
    const b=document.createElement('button'); b.textContent=c?'Отключить':'Подключить';
    b.onclick=()=>btAction(c?'disconnect':'connect', d.mac);
    row.appendChild(b); box.appendChild(row);
  });
}
async function btAction(action, mac){
  const {status,j}=await api('/api/bt',{method:'POST',body:JSON.stringify({action:action,mac:mac})});
  toast(status===200?(action+': ок'):(j.error||('ошибка '+status)));
  setTimeout(loadBT, action==='scan'?9000:1500);
}

async function loadFiles(path){
  const {status,j}=await api('/api/files'+(path?('?path='+encodeURIComponent(path)):''));
  const box=document.getElementById('filelist');
  if(status!==200){ box.textContent=j.error||('ошибка '+status); return; }
  box.classList.remove('mut'); box.innerHTML='';
  if(j.roots){ j.roots.forEach(r=>{ const a=document.createElement('div'); a.innerHTML='<a href="#" onclick="loadFiles(\''+r+'\');return false">'+esc(r)+'</a>'; box.appendChild(a); }); return; }
  const up=document.createElement('div'); const parent=j.path.replace(/\/[^/]*$/,'')||'/';
  up.innerHTML='<a href="#" onclick="loadFiles(\''+parent+'\');return false">⬆ ..</a> <span class="mut">'+esc(j.path)+'</span>'; box.appendChild(up);
  (j.entries||[]).forEach(e=>{
    const row=document.createElement('div'); row.style.margin='2px 0';
    if(e.is_dir){ row.innerHTML='📁 <a href="#" onclick="loadFiles(\''+e.path.replace(/'/g,"\\'")+'\');return false">'+esc(e.name)+'</a>'; }
    else { row.innerHTML='📄 <a href="/api/download?path='+encodeURIComponent(e.path)+'&token='+encodeURIComponent(T())+'">'+esc(e.name)+'</a> <span class="mut">'+e.size+' Б</span>'; }
    box.appendChild(row);
  });
}

async function loadAutostart(){
  const {status,j}=await api('/api/autostart');
  const st=document.getElementById('asstate');
  if(status!==200||!j.available){ st.textContent='нет crontab'; st.style.color='var(--warn)'; return; }
  st.textContent = j.installed?'установлен':'нет'; st.style.color = j.installed?'var(--ok)':'var(--mut)';
  document.getElementById('asline').textContent = j.line||'';
}
async function asAction(action){
  const {status,j}=await api('/api/autostart',{method:'POST',body:JSON.stringify({action:action})});
  toast(status===200?'ок':(j.error||('ошибка '+status)));
  setTimeout(loadAutostart, 800);
}
async function led(){ const {j}=await api('/api/led'); toast(j.color?('LED: '+j.color):(j.error||'—')); }

async function loadLogsList(){
  const {j}=await api('/api/logs');
  const sel=document.getElementById('logsel'); sel.innerHTML='';
  (j.files||[]).forEach(f=>{ const o=document.createElement('option'); o.value=f; o.textContent=f; sel.appendChild(o); });
}
async function loadLog(){
  const f=document.getElementById('logsel').value, n=document.getElementById('loglines').value;
  if(!f){ return; }
  const {j}=await api('/api/logs?file='+encodeURIComponent(f)+'&lines='+n);
  const el=document.getElementById('logout'); el.classList.remove('mut'); el.textContent=j.text||j.error||'(пусто)';
}

async function doExec(){
  const c=document.getElementById('cmd').value.trim(); if(!c){ return; }
  const el=document.getElementById('execout'); el.textContent='…';
  const {status,j}=await api('/api/exec',{method:'POST',body:JSON.stringify({cmd:c})});
  el.classList.remove('mut');
  if(status===401){ el.textContent='Нужен токен — нажми 🔑'; return; }
  if(status===403){ el.textContent=j.error||'Шелл выключен: запусти stationctl с --shell'; return; }
  if(j.exit===undefined){ el.textContent=j.error||'ошибка'; return; }
  el.textContent = (j.stdout||'') + (j.stderr?('\n[stderr]\n'+j.stderr):'') + '\n[exit '+j.exit+']';
}

async function power(what){
  if(!confirm(what==='reboot'?'Перезагрузить станцию?':'Выключить станцию?')){ return; }
  const {j}=await api('/api/'+what,{method:'POST'}); toast(j.ok||j.error||'');
}

let autoT;
document.getElementById('autoref').onchange = function(){ clearInterval(autoT); if(this.checked){ autoT=setInterval(loadStatus,5000); } };

if(!T()){ setToken(); }
loadStatus(); loadLogsList(); loadADB(); loadBT(); loadAutostart(); loadSpotify();
</script>
</body>
</html>`
