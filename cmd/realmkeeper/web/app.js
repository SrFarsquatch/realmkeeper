const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
const state={configured:false,servers:[],games:[],selectedGame:'hytale',serverId:null,status:null,resources:null,settings:null,logs:[],liveLogBuffers:new Set(),events:null,eventsOpen:false,current:'servers',configFile:'config.json',appUpdate:null,players:null,selectionGeneration:0,selectionAbort:new AbortController(),polling:false};
// Server-scoped calls send the server ID explicitly. Pass `serverId` captured when
// the user acted so a later selection change cannot redirect the request.
async function api(url,opts={}){const{serverId=state.serverId,...fetchOpts}=opts;const headers=fetchOpts.body instanceof FormData?{}:{'Content-Type':'application/json'};if(serverId)headers['X-HSM-Server']=serverId;const res=await fetch(url,{...fetchOpts,headers:{...headers,...(fetchOpts.headers||{})}});let body=null;try{body=await res.json()}catch{}if(res.status===401&&!url.includes('/login')){showAuth(false);throw new Error('Authentication required')}if(!res.ok)throw new Error(body?.error||`${res.status} ${res.statusText}`);return body}
// A selection context ties reads to the server and selection generation that
// started them. Changing servers aborts its requests, and `current()` turns false
// so late responses from the previous server are discarded instead of rendered.
function selectionContext(){const id=state.serverId,generation=state.selectionGeneration,signal=state.selectionAbort.signal;return{id,generation,signal,current:()=>!!id&&id===state.serverId&&generation===state.selectionGeneration}}
function scopedApi(ctx,url,opts={}){return api(url,{...opts,serverId:ctx.id,signal:ctx.signal})}
function toast(msg,error=false){const el=$('#toast');el.textContent=msg;el.className='toast show'+(error?' error':'');clearTimeout(toast.t);toast.t=setTimeout(()=>el.className='toast',3000)}
function fmtCount(n){n=Number(n)||0;return n>=1e6?(n/1e6).toFixed(1)+'M':n>=1e3?(n/1e3).toFixed(n>=1e4?0:1)+'k':String(n)}
function fmtBytes(n){if(!n)return'—';const u=['B','KB','MB','GB','TB'];let i=0;while(n>=1024&&i<u.length-1){n/=1024;i++}return`${n.toFixed(i>1?1:0)} ${u[i]}`}
function fmtUptime(sec){if(!sec)return'—';const d=Math.floor(sec/86400),h=Math.floor(sec%86400/3600),m=Math.floor(sec%3600/60);return d?`${d}d ${h}h`:h?`${h}h ${m}m`:`${m}m`}
function fmtDate(v){try{return new Date(v).toLocaleString()}catch{return v}}function fmtTime(v){try{return new Date(v).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',second:'2-digit'})}catch{return''}}
function esc(s){return String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
function selectedSummary(){return state.servers.find(s=>s.id===state.serverId)}
// Per-game form rules come from the backend (/api/games and each server), so
// forms validate the same limits the API enforces.
const fallbackRules={gameModes:['Adventure','Creative'],modeLabel:'Game mode',modeFixed:false,defaultMotd:'',defaultMaxPlayers:20,playerLimit:1000,jvmMemory:true,whitelist:true,portMin:0,portMax:0};
function rulesForGame(id){return state.games.find(g=>g.id===id)?.rules||state.servers.find(s=>s.gameType===id)?.rules||fallbackRules}
function setModeOptions(select,modes,value){select.innerHTML=modes.map(m=>'<option value="'+esc(m)+'">'+esc(m)+'</option>').join('');select.value=modes.includes(value)?value:modes[0]}
function statusLabel(s){if(s?.state==='crashed')return `Crashed (exit ${s.exitCode})`;if(s?.state==='starting')return'Starting';if(s?.state==='stopping')return'Stopping';if(s?.installBusy||s?.state==='installing')return'Installing';if(s?.state==='not-installed'||s?.installed===false)return'Not installed';if(s?.running)return'Online';return'Stopped'}
// Capabilities are declared per game by the backend catalog and delivered with
// each server summary. Anything missing is treated as unsupported.
const NO_CAPABILITIES={worlds:false,playerManagement:false,console:false,consoleCommands:false,fileManagement:false,backups:false,configuration:false,configFiles:[],mods:false,playerList:false,deviceAuth:false,gameUpdates:false};
function serverCapabilities(server=selectedSummary()){return{...NO_CAPABILITIES,...(server?.capabilities||{})}}
function serverProtocol(server){return String(server?.protocol||'').toUpperCase()||'Port'}
const VIEW_CAPABILITY={worlds:'worlds',players:'playerManagement',console:'console',config:'fileManagement',mods:'mods',backups:'backups',settings:'configuration'};
const CAPABILITY_LABELS=[['consoleCommands','console commands'],['playerList','the player list'],['mods','mods'],['backups','backups'],['deviceAuth','device login'],['gameUpdates','in-app game updates']];
function listJoin(items){return items.length<2?items.join(''):items.slice(0,-1).join(', ')+' or '+items[items.length-1]}
function gameLabel(id){return ({hytale:'Hytale','minecraft-java':'Minecraft Java','terraria':'Terraria','runescape-dragonwilds':'RuneScape: Dragonwilds'})[id]||'Unknown game'}
// Square monogram for small icon slots; the game wordmarks are wide (about 3:1)
// and only fit the wide tile on the Overview.
// Official artwork, when bundled, is reported by the backend per game. Without
// it a square monogram is used; the wide wordmarks only fit the Overview tile.
const gameArt={};
function rememberGameArt(id,art){if(id&&art)gameArt[id]=art}
function gameLogo(id){const icon=gameArt[id]?.icon;if(icon)return '<img src="'+esc(icon)+'" alt="" decoding="async">';const marks={'hytale':'H','minecraft-java':'MC','terraria':'T','runescape-dragonwilds':'DW'};return '<span class="mono mono-'+esc(id||'unknown')+'">'+(marks[id]||'?')+'</span>'}
function gameWordmark(id){const art=gameArt[id];if(art?.logo)return '<img src="'+esc(art.logo)+'" alt="" decoding="async">';if(art?.icon)return gameLogo(id);const files={'hytale':'hytale.svg','minecraft-java':'minecraft-java.svg','terraria':'terraria.svg','runescape-dragonwilds':'runescape-dragonwilds.svg'};return files[id]?'<img src="/assets/games/'+files[id]+'" alt="" decoding="async">':gameLogo(id)}
function statusDotClass(s){if(s?.state==='crashed')return'crashed';if(s?.state==='starting'||s?.state==='stopping'||s?.installBusy||s?.state==='installing')return'busy';if(s?.running)return'online';return''}
async function boot(){const b=await api('/api/bootstrap',{serverId:null});state.configured=b.configured;if(!b.configured){showAuth(true);return}try{await api('/api/servers',{serverId:null});showApp()}catch{showAuth(false)}}
function showAuth(setup){$('#app').classList.add('hidden');$('#auth').classList.remove('hidden');$('#auth-title').textContent=setup?'Create admin account':'Sign in';$('#auth-copy').textContent=setup?'Secure this server manager.':'Manage your game servers on this host.';$('#auth-submit').textContent=setup?'Create account':'Sign in';$('#auth-pass').autocomplete=setup?'new-password':'current-password';$('#auth-form').dataset.mode=setup?'setup':'login'}
async function showApp(){$('#auth').classList.add('hidden');$('#app').classList.remove('hidden');await loadServers(true);if(state.serverId)await loadSelected();startPolling();switchView(state.current);renderOnboarding()}
function startPolling(){clearInterval(state.poll);state.poll=setInterval(pollTick,2000);clearInterval(state.playerPoll)}
// One poll at a time: a slow response must not let ticks pile up and race.
// Logs are polled only while the live event stream is disconnected.
async function pollTick(){if(state.polling||document.hidden)return;state.polling=true;try{await loadServers(false);if(state.serverId){const ctx=selectionContext();const jobs=[refreshStatus(ctx),refreshResources(ctx)];if(!state.eventsOpen)jobs.push(loadLogs(ctx));await Promise.allSettled(jobs)}}finally{state.polling=false}}
$('#auth-form').addEventListener('submit',async e=>{e.preventDefault();$('#auth-error').textContent='';const body={username:$('#auth-user').value.trim(),password:$('#auth-pass').value};try{await api($('#auth-form').dataset.mode==='setup'?'/api/bootstrap':'/api/login',{method:'POST',body:JSON.stringify(body)});showApp()}catch(err){$('#auth-error').textContent=err.message}});$('#logout').addEventListener('click',async()=>{try{await api('/api/logout',{method:'POST'})}catch{}location.reload()});
$$('#nav button').forEach(b=>b.addEventListener('click',()=>switchView(b.dataset.view)));$$('[data-view-tab]').forEach(b=>b.addEventListener('click',()=>switchView(b.dataset.viewTab)));$$('[data-view-link]').forEach(b=>b.addEventListener('click',()=>switchView(b.dataset.viewLink)));
function switchView(v){
  const summary=selectedSummary();
  const caps=serverCapabilities(summary);
  const serverView=!['servers','app-settings'].includes(v);
  if(serverView&&!summary)v='servers';
  if(VIEW_CAPABILITY[v]&&!caps[VIEW_CAPABILITY[v]])v='dashboard';
  state.current=v;
  $$('[data-view-tab]').forEach(el=>{
    const capability=VIEW_CAPABILITY[el.dataset.viewTab];
    el.classList.toggle('hidden',!!capability&&!caps[capability]);
  });
  $('#console-form')?.classList.toggle('hidden',!caps.consoleCommands);
  $('.players-panel')?.classList.toggle('hidden',!caps.playerList);
  $('#metric-players-row')?.classList.toggle('hidden',!caps.playerList);
  $('#meta-players')?.classList.toggle('hidden',!caps.playerList);
  $$('[data-action="auth"]').forEach(el=>el.classList.toggle('hidden',!caps.deviceAuth));
  $('#setup-auth')?.classList.toggle('hidden',!caps.deviceAuth);
  $$('[data-action="backup"]').forEach(el=>el.classList.toggle('hidden',!caps.backups));
  // Hytale has its own backup command and schedule; other games use snapshots only.
  $$('.native-only').forEach(el=>el.classList.toggle('hidden',!caps.nativeBackups));
  $('#native-backups-card')?.classList.toggle('hidden',!caps.nativeBackups);
  $('#backup-columns')?.classList.toggle('single',!caps.nativeBackups);
  $('#backup-schedule-help').textContent=caps.nativeBackups?'How often Hytale makes automatic backups and how many copies to keep. Oldest automatic backups are removed first.':'How many full snapshots to keep. The oldest are removed first.';
  $('#snapshot-help').textContent=caps.liveSnapshots?'The world and server settings. Can be taken while the server is running: saving pauses for a moment while the world is copied.':'World, configuration, permissions and mods. Server must be stopped.';
  $('#mc-properties-card')?.classList.toggle('hidden',!caps.serverProperties);
  $$('[data-update]').forEach(el=>el.closest('.card')?.classList.toggle('hidden',!caps.gameUpdates));
  $('#config-editor-panel')?.classList.toggle('hidden',!caps.configFiles.length);
  syncConfigFileOptions(caps.configFiles);
  renderCapabilityNote(summary,caps,v);
  $$('.view').forEach(x=>x.classList.remove('active'));
  const target=document.getElementById('view-'+v);
  if(target)target.classList.add('active');
  $$('#nav button').forEach(x=>{
    const active=x.dataset.view===v;
    x.classList.toggle('active',active);
    if(active)x.setAttribute('aria-current','page');else x.removeAttribute('aria-current');
  });
  $$('[data-view-tab]').forEach(x=>{
    const active=x.dataset.viewTab===v;
    x.classList.toggle('active',active);
    if(active)x.setAttribute('aria-current','page');else x.removeAttribute('aria-current');
  });
  const showTabs=!!state.serverId&&!['servers','app-settings'].includes(v);
  $('#server-tabs').classList.toggle('hidden',!showTabs);
  const global=v==='servers'||v==='app-settings';
  $('#global-head').classList.toggle('hidden',!global||(v==='servers'&&!state.servers.length));
  $('#server-head').classList.toggle('hidden',global||!state.serverId);
  $('#view-title').textContent=v==='app-settings'?'Panel Settings':'My Servers';
  $('#view-subtitle').textContent=v==='app-settings'?'Application version, updates and security.':'Your game servers, status and controls in one place.';
  $('#servers-actions').classList.toggle('hidden',v!=='servers');
  $('#mobile-context').textContent=global?(v==='app-settings'?'Panel Settings':'My Servers'):(summary?.serverName||'Server');
  setNavOpen(false);
  renderWorkspaceError();
  loadCurrent();
}
// Header identity comes from the server summary so it is correct immediately
// after a switch, before the server's settings have loaded.
function renderServerHead(){
  const summary=selectedSummary();
  if(!summary)return;
  $('#server-name').textContent=summary.serverName;
  for(const id of['server-game','hero-game'])$('#'+id).textContent=gameLabel(summary.gameType);
  $('#server-game-icon').innerHTML=gameLogo(summary.gameType);$('#hero-game-icon').innerHTML=gameWordmark(summary.gameType);$('#hero-game-icon').classList.toggle('is-icon',!gameArt[summary.gameType]?.logo&&!!gameArt[summary.gameType]?.icon);const banner=gameArt[summary.gameType]?.banner,tile=$('.game-tile');tile.classList.toggle('has-banner',!!banner);tile.style.setProperty('--banner',banner?'url("'+banner.replace(/["\\]/g,'')+'")':'none');
  $('#meta-port').textContent=`${serverProtocol(summary)} ${summary.port}`;renderModsLabels(summary);
  if(!state.players)$('#meta-players-value').textContent='Players unknown';
  if(!['servers','app-settings'].includes(state.current))$('#mobile-context').textContent=summary.serverName;
}
// Mobile navigation drawer.
function setNavOpen(open){$('#app').classList.toggle('nav-open',open);$('#sidebar-scrim').hidden=!open;$('#nav-toggle').setAttribute('aria-expanded',String(open))}
$('#nav-toggle').addEventListener('click',()=>setNavOpen(!$('#app').classList.contains('nav-open')));
$('#sidebar-scrim').addEventListener('click',()=>setNavOpen(false));
document.addEventListener('keydown',e=>{if(e.key==='Escape')setNavOpen(false)});
$('#back-to-servers').addEventListener('click',()=>switchView('servers'));
function renderCapabilityNote(summary,caps,v){
  const note=$('#game-capability-note');
  if(!note)return;
  const missing=CAPABILITY_LABELS.filter(([key])=>!caps[key]).map(([,label])=>label);
  const show=!!summary&&missing.length>0&&!['servers','app-settings'].includes(v);
  note.classList.toggle('hidden',!show);
  note.textContent=show?`${gameLabel(summary.gameType)} servers do not support ${listJoin(missing)} in Realmkeeper yet, so those controls are hidden.`:'';
}
function syncConfigFileOptions(files){const sel=$('#config-file');if(!sel)return;const key=files.join('|');if(sel.dataset.files===key)return;sel.dataset.files=key;sel.innerHTML=files.map(f=>`<option>${esc(f)}</option>`).join('')}
// Essential workspace failures (settings or status) stay visible with a retry
// control rather than flashing a toast and leaving a half-loaded workspace.
function setWorkspaceError(message){state.workspaceError=message||null;renderWorkspaceError()}
function renderWorkspaceError(){const el=$('#workspace-error');if(!el)return;const show=!!state.workspaceError&&!!state.serverId&&!['servers','app-settings'].includes(state.current);el.classList.toggle('hidden',!show);$('#workspace-error-text').textContent=show?'Could not load '+(selectedSummary()?.serverName||'this server')+': '+state.workspaceError:''}
$('#workspace-retry').addEventListener('click',()=>{setWorkspaceError(null);loadSelected()});

let managedPath='.', managedOpenPath='', managedDirty=false;
function filePathJoin(base,name){return base==='.'?name:base+'/'+name}
function fileSize(n){if(n<1024)return n+' B';if(n<1048576)return (n/1024).toFixed(1)+' KB';if(n<1073741824)return (n/1048576).toFixed(1)+' MB';return (n/1073741824).toFixed(2)+' GB'}
async function loadFiles(path=managedPath,ctx=selectionContext()){if(!ctx.id)return;managedPath=path||'.';const list=$('#managed-files');list.innerHTML='<div class="empty-state">Loading files…</div>';try{const d=await scopedApi(ctx,'/api/files?path='+encodeURIComponent(managedPath));if(!ctx.current())return;managedPath=d.path||'.';renderFileBreadcrumbs();if(!d.entries.length){list.innerHTML='<div class="empty-state">This folder is empty.</div>';return}list.innerHTML=d.entries.map(f=>'<div class="managed-file-row" data-path="'+esc(f.path)+'" data-directory="'+f.directory+'"><button class="managed-file-name" data-file-action="open" title="'+esc(f.name)+'"><span class="managed-file-icon '+(f.directory?'directory':'')+'">'+(f.directory?'▰':'▤')+'</span><span>'+esc(f.name)+'</span></button><span class="managed-file-size">'+(f.directory?'Folder':fileSize(f.size))+'</span><span class="managed-file-date">'+esc(fmtDate(f.modified))+'</span><span class="managed-file-actions"><button class="quiet-btn" data-file-action="download" title="Download" '+(f.directory?'disabled':'')+'>↓</button><button class="quiet-btn" data-file-action="rename">Rename</button><button class="quiet-btn danger-text" data-file-action="delete">Delete</button></span></div>').join('');$('#file-manager-note').textContent=d.entries.length+' item'+(d.entries.length===1?'':'s')+' · '+(managedPath==='.'?'/':managedPath)}catch(e){if(!ctx.current())return;list.innerHTML='<div class="empty-state">'+esc(e.message)+' <button class="quiet-btn" data-file-retry>Retry</button></div>'}}
function renderFileBreadcrumbs(){const el=$('#file-breadcrumbs');const parts=managedPath==='.'?[]:managedPath.split('/');let path='.';el.innerHTML='<button data-file-dir=".">Server files</button>'+parts.map(p=>{path=path==='.'?p:path+'/'+p;return '<span>›</span><button data-file-dir="'+esc(path)+'">'+esc(p)+'</button>'}).join('')}
function openManagedFolder(path){if(managedDirty&&!confirm('Discard unsaved file changes?'))return;managedDirty=false;$('#file-editor').classList.add('hidden');loadFiles(path)}
async function editManagedFile(path){const ctx=selectionContext();if(!ctx.id)return;if(managedDirty&&!confirm('Discard unsaved file changes?'))return;try{const d=await scopedApi(ctx,'/api/files/content?path='+encodeURIComponent(path));if(!ctx.current())return;managedOpenPath=d.path;managedDirty=false;$('#file-editor').classList.remove('hidden');$('#file-editor-name').textContent=d.name;$('#file-editor-meta').textContent=fileSize(d.size)+' · '+fmtDate(d.modified);$('#managed-file-content').value=d.content;$('#file-editor-status').textContent='Loaded';$('#file-editor-dirty').textContent='';$('#file-editor-warning').classList.add('hidden');$('#managed-file-content').oninput=()=>{managedDirty=true;$('#file-editor-dirty').textContent='Unsaved changes'};$('#file-editor').scrollIntoView({behavior:'smooth',block:'nearest'})}catch(e){if(ctx.current())toast(e.message,true)}}
async function saveManagedFile(){const id=state.serverId,path=managedOpenPath;if(!id||!path)return;const btn=$('#file-editor-save');btn.disabled=true;try{await api('/api/files/content',{method:'PUT',serverId:id,body:JSON.stringify({path,content:$('#managed-file-content').value})});if(id!==state.serverId)return;managedDirty=false;$('#file-editor-status').textContent='Saved '+new Date().toLocaleTimeString();$('#file-editor-dirty').textContent='';toast('File saved');await loadFiles(managedPath)}catch(e){toast(e.message,true);if(id===state.serverId)$('#file-editor-status').textContent=e.message}finally{btn.disabled=false}}
async function downloadManagedFile(path){const id=state.serverId;if(!id)return;try{const res=await fetch('/api/files/download?path='+encodeURIComponent(path),{headers:{'X-HSM-Server':id}});if(!res.ok){let b={};try{b=await res.json()}catch{}throw new Error(b.error||'Download failed')}const blob=await res.blob(),url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download=path.split('/').pop();document.body.appendChild(a);a.click();a.remove();URL.revokeObjectURL(url)}catch(e){toast(e.message,true)}}
async function deleteManagedFile(path){const id=state.serverId;if(!id)return;if(!confirm('Permanently delete "'+path.split('/').pop()+'" and its contents if it is a folder? This cannot be undone.'))return;try{await api('/api/files?path='+encodeURIComponent(path),{method:'DELETE',serverId:id});toast('Deleted');if(id!==state.serverId)return;if(managedOpenPath===path){managedOpenPath='';managedDirty=false;$('#file-editor').classList.add('hidden')}await loadFiles(managedPath)}catch(e){toast(e.message,true)}}
async function renameManagedFile(path){const id=state.serverId;if(!id)return;const old=path.split('/').pop(),name=prompt('New name',old);if(name===null||!name.trim()||name===old)return;try{const d=await api('/api/files/rename',{method:'POST',serverId:id,body:JSON.stringify({path,name:name.trim()})});toast('Renamed');if(id!==state.serverId)return;if(managedOpenPath===path){managedOpenPath=d.path;$('#file-editor-name').textContent=name.trim()}await loadFiles(managedPath)}catch(e){toast(e.message,true)}}
async function createManagedFolder(){const id=state.serverId,base=managedPath;if(!id)return;const name=prompt('Folder name');if(name===null||!name.trim())return;try{await api('/api/files/mkdir',{method:'POST',serverId:id,body:JSON.stringify({path:filePathJoin(base,name.trim())})});toast('Folder created');if(id===state.serverId)await loadFiles(managedPath)}catch(e){toast(e.message,true)}}
async function createManagedFile(){const id=state.serverId;if(!id)return;const name=prompt('New file name');if(name===null||!name.trim())return;const path=filePathJoin(managedPath,name.trim());try{await api('/api/files/content',{method:'PUT',serverId:id,body:JSON.stringify({path,content:''})});toast('File created');if(id!==state.serverId)return;await loadFiles(managedPath);await editManagedFile(path)}catch(e){toast(e.message,true)}}
async function uploadManagedFiles(files){const id=state.serverId,dir=managedPath;if(!id)return;for(const file of files){const form=new FormData();form.append('file',file);form.append('path',dir);try{await api('/api/files/upload',{method:'POST',serverId:id,body:form});toast('Uploaded '+file.name)}catch(e){toast(file.name+': '+e.message,true)}}$('#file-upload').value='';if(id===state.serverId)await loadFiles(managedPath)}
$('#managed-files').addEventListener('click',e=>{const action=e.target.closest('[data-file-action]');if(e.target.closest('[data-file-retry]')){loadFiles(managedPath);return}const row=e.target.closest('.managed-file-row');if(!row)return;const path=row.dataset.path;switch(action?.dataset.fileAction){case'open':row.dataset.directory==='true'?openManagedFolder(path):editManagedFile(path);break;case'download':downloadManagedFile(path);break;case'rename':renameManagedFile(path);break;case'delete':deleteManagedFile(path);break;}});
$('#file-breadcrumbs').addEventListener('click',e=>{const b=e.target.closest('[data-file-dir]');if(b)openManagedFolder(b.dataset.fileDir)});
$('#file-refresh').addEventListener('click',()=>loadFiles(managedPath));
$('#file-new-folder').addEventListener('click',createManagedFolder);
$('#file-new-file').addEventListener('click',createManagedFile);
$('#file-upload').addEventListener('change',e=>{if(e.target.files&&e.target.files.length)uploadManagedFiles([...e.target.files])});
$('#file-editor-save').addEventListener('click',saveManagedFile);
$('#file-editor-close').addEventListener('click',()=>{if(managedDirty&&!confirm('Discard unsaved file changes?'))return;managedDirty=false;managedOpenPath='';$('#file-editor').classList.add('hidden')});

function loadCurrent(){if(state.current==='servers')renderFleet();if(state.current==='app-settings')loadAppUpdate();if(!state.serverId)return;const caps=serverCapabilities();if(state.current==='mods')loadMods();if(state.current==='backups')loadBackups();if(state.current==='config'){loadFiles();if(caps.configFiles.length)loadServerConfig()}if(state.current==='setup')loadInstallStatus();if(state.current==='settings'&&caps.serverProperties)loadMinecraftProperties();if(state.current==='schedules')loadSchedules();if(state.current==='worlds')loadWorlds();if(state.current==='players')loadMinecraftPlayers()}
async function loadServers(initial=false){
  let servers;
  try{servers=await api('/api/servers',{serverId:null})}catch(e){if(initial)toast(e.message,true);return}
  state.servers=servers;
  servers.forEach(s=>rememberGameArt(s.gameType,s.art));
  renderPortWarning();
  if(!servers.length){
    if(state.serverId!==null)setSelectedServer(null);
    renderServerList();renderFleet();renderFleetStats();populateCloneSources();renderOnboarding();
    if(initial||!['servers','app-settings'].includes(state.current))switchView('servers');
    return;
  }
  if(!state.serverId||!servers.some(s=>s.id===state.serverId)){
    // The selected server is missing (first load, or deleted elsewhere). Move to
    // a valid server through the same reset path as a user selection.
    const removed=state.serverId;
    let saved=null;try{saved=localStorage.getItem('realmkeeper-server')||localStorage.getItem('hsm-server')}catch{}
    setSelectedServer(saved&&servers.some(s=>s.id===saved)?saved:servers[0].id);
    if(removed&&!initial){
      toast('The selected server no longer exists. Showing '+(selectedSummary()?.serverName||'another server')+'.',true);
      if(!['servers','app-settings'].includes(state.current))switchView('dashboard');
      loadSelected();
    }
  }
  renderServerList();renderFleet();renderFleetStats();populateCloneSources();renderOnboarding();
  renderServerHead();
}
function renderFleetStats(){
  const online=state.servers.filter(s=>s.running).length,installed=state.servers.filter(s=>s.installed).length;
  $('#fleet-online').textContent=online;$('#fleet-total').textContent=state.servers.length;
  const stat=(icon,tone,value,label)=>`<div class="stat"><span class="stat-icon ${tone}"><svg class="icon"><use href="#i-${icon}"/></svg></span><div><strong>${value}</strong><span>${label}</span></div></div>`;
  $('#fleet-summary').innerHTML=stat('servers','',state.servers.length,'Servers')+stat('play','green',online,'Online')+stat('check','muted',installed,'Installed')+stat('stop','amber',state.servers.length-online,'Stopped');
}
function renderServerList(){
  const el=$('#server-list');
  const groups=new Map();
  for(const server of state.servers){
    const game=server.gameType||'unknown';
    if(!groups.has(game))groups.set(game,[]);
    groups.get(game).push(server);
  }
  const preferred=['hytale','minecraft-java','terraria','runescape-dragonwilds'];
  const order=[...preferred.filter(game=>groups.has(game)),...Array.from(groups.keys()).filter(game=>!preferred.includes(game)).sort((a,b)=>gameLabel(a).localeCompare(gameLabel(b)))];
  el.innerHTML=order.map(game=>{
    const servers=groups.get(game);
    return `<section class="server-group" aria-label="${esc(gameLabel(game))} servers">
      <div class="server-group-heading"><span>${esc(gameLabel(game))}</span><span class="server-group-count">${servers.length}</span></div>
      <div class="server-group-items">${servers.map(s=>`<button class="server-list-item ${s.id===state.serverId?'active':''}" data-select-server="${esc(s.id)}" aria-pressed="${s.id===state.serverId?'true':'false'}">
        <span class="server-list-mark"><span class="game-icon game-icon-${esc(s.gameType)}" aria-hidden="true">${gameLogo(s.gameType)}</span><span class="server-list-dot ${statusDotClass(s)}" aria-label="${esc(statusLabel(s))}"></span></span>
        <span class="server-list-copy"><b>${esc(s.serverName)}</b><small>${esc(statusLabel(s))} · ${esc(serverProtocol(s))} ${s.port}</small></span>
      </button>`).join('')}</div>
    </section>`;
  }).join('');
  el.querySelectorAll('[data-select-server]').forEach(button=>button.addEventListener('click',()=>selectServer(button.dataset.selectServer)));
}

async function loadAppUpdate(){const check=$('#app-update-check');if(check)check.disabled=true;$('#app-update-message').textContent='Checking GitHub for the latest stable release…';try{const info=await api('/api/app-update');state.appUpdate=info;renderAppUpdate(info)}catch(e){$('#app-update-badge').textContent='Check failed';$('#app-update-message').textContent=e.message;$('#app-update-apply').disabled=true}finally{if(check)check.disabled=false}}
function renderAppUpdate(info){if(info.status==='updating'&&info.latestVersion&&!state.waitingForUpdate)waitForAppVersion(info.latestVersion);$('#app-update-current').textContent='v'+(info.currentVersion||'unknown');$('#app-update-latest').textContent='v'+(info.latestVersion||info.currentVersion||'unknown');$('#app-update-date').textContent=info.publishedAt?fmtDate(info.publishedAt):'—';const badge=$('#app-update-badge');badge.textContent=info.updateAvailable?'Update available':(info.status==='updating'?'Updating…':(info.message?.includes('No stable release')?'No release yet':'Up to date'));badge.className='update-badge'+(info.updateAvailable?' available':'');$('#app-update-message').textContent=info.message||(info.updateAvailable?'A newer stable version is ready to install.':'You are running the latest release.');$('#app-update-apply').disabled=!info.updateAvailable||!info.updaterReady||info.status==='updating'||info.status==='starting';$('#app-update-apply').textContent=info.status==='updating'?'Updating…':'Update and restart';const link=$('#app-update-release-link');if(info.releaseUrl&&info.releaseUrl.startsWith('https://github.com/SrFarsquatch/realmkeeper/releases/')){link.href=info.releaseUrl;link.classList.remove('hidden')}else link.classList.add('hidden');const notes=$('#app-update-notes');if(info.releaseNotes?.trim()){notes.textContent=info.releaseNotes;notes.classList.remove('hidden')}else{notes.textContent='';notes.classList.add('hidden')}}
$('#app-update-check').addEventListener('click',loadAppUpdate);
$('#app-update-apply').addEventListener('click',async()=>{const info=state.appUpdate;if(!info?.updateAvailable||!info.updaterReady)return;if(!confirm('Update Realmkeeper to v'+info.latestVersion+'? The dashboard will briefly go offline while the container restarts. Your server data volume and game worlds will be preserved.'))return;const btn=$('#app-update-apply');btn.disabled=true;btn.textContent='Starting update…';$('#app-update-badge').textContent='Updating…';$('#app-update-message').textContent='Preparing the update. Keep this page open while the manager restarts.';try{const result=await api('/api/app-update',{method:'POST',body:'{}'});$('#app-update-message').textContent=result.message||'Update started. Waiting for the new version…';waitForAppVersion(info.latestVersion)}catch(e){toast(e.message,true);$('#app-update-message').textContent=e.message;await loadAppUpdate()}});
async function waitForAppVersion(target){
  if(state.waitingForUpdate)return;state.waitingForUpdate=true;try{await waitForAppVersionLoop(target)}finally{state.waitingForUpdate=false}
}
async function waitForAppVersionLoop(target){
  const started=Date.now(),timeout=15*60*1000;
  let attempts=0,lastMessageAt=0,delay=2000;
  while(Date.now()-started<timeout){
    await new Promise(resolve=>setTimeout(resolve,delay));
    attempts++;
    try{
      const health=await fetch('/api/health',{cache:'no-store'});
      if(health.ok){
        const body=await health.json();
        if(String(body.version||'').replace(/^v/,'')===String(target).replace(/^v/,'')){
          toast('Updated successfully to v'+target);
          setTimeout(()=>location.reload(),900);
          return;
        }
        // Still the old version: show the helper's progress, and stop
        // waiting if it failed.
        const st=await fetch('/api/app-update/status',{cache:'no-store'}).then(r=>r.ok?r.json():null).catch(()=>null);
        if(st?.status==='failed'){$('#app-update-message').textContent=st.message;toast('The update did not complete',true);await loadAppUpdate();$('#app-update-message').textContent=st.message;return}
        if(st?.status==='updating'&&st.message?.startsWith('Updating:')){$('#app-update-message').textContent=st.message;lastMessageAt=Math.floor((Date.now()-started)/1000)}
      }
    }catch{}
    const elapsed=Math.floor((Date.now()-started)/1000);
    if(elapsed-lastMessageAt>=15){
      $('#app-update-message').textContent='Waiting for v'+target+' to start… '+Math.floor(elapsed/60)+'m '+(elapsed%60)+'s elapsed. The dashboard may be unavailable briefly during container replacement.';
      lastMessageAt=elapsed;
    }
    // Back off gently while Docker pulls or recreates the container.
    delay=Math.min(10000,Math.round(delay*1.25));
  }
  $('#app-update-message').textContent='No new version detected after 15 minutes. The update status will be checked again; if it failed, the helper logs are retained in Docker so you can diagnose it and retry.';
  try{
    const response=await fetch('/api/app-update',{cache:'no-store'});
    if(response.ok){
      const info=await response.json();
      state.appUpdate=info;
      renderAppUpdate(info);
      $('#app-update-message').textContent=info.message||'The manager is reachable but has not reported the target version. Check the updater helper logs before retrying.';
      return;
    }
  }catch{}
  $('#app-update-apply').disabled=true;
}

function renderOnboarding(){const empty=state.servers.length===0;$('#app').classList.toggle('onboarding',empty);$('#first-run')?.classList.toggle('hidden',!empty);$('#fleet-content')?.classList.toggle('hidden',empty);$('.side-servers')?.classList.toggle('hidden',empty);if(state.current==='servers')$('#global-head').classList.toggle('hidden',empty)}
function renderFleet(){
  const grid=$('#fleet-grid');if(!grid)return;
  if(!state.servers.length){grid.innerHTML='<div class="empty-state">No servers configured.</div>';return}
  grid.innerHTML=state.servers.map(s=>{
    const transitioning=s.state==='starting'||s.state==='stopping';
    const action=s.running?'stop':s.installed?'start':'install';
    const actionLabel=s.state==='starting'?'Starting…':s.state==='stopping'?'Stopping…':s.installBusy?'Installing…':s.running?'Stop':s.installed?'Start':'Install';
    const actionClass=s.running?'danger':s.installed?'success':'primary';
    return `<article class="server-card ${s.id===state.serverId?'selected':''}">
      <button type="button" class="server-card-open" data-fleet-open="${esc(s.id)}" aria-label="Open ${esc(s.serverName)}">
        <span class="server-card-top"><span class="game-icon large" aria-hidden="true">${gameLogo(s.gameType)}</span><span class="server-card-title"><strong>${esc(s.serverName)}${s.primary?'<span class="primary-tag">primary</span>':''}</strong><small>${esc(gameLabel(s.gameType))}</small></span></span>
        <span class="status-badge ${statusDotClass(s)}"><span class="dot ${statusDotClass(s)}"></span>${esc(statusLabel(s))}</span>
        <span class="server-card-meta"><span><span>Address</span><b>${esc(s.publicHost||location.hostname)}:${s.port}</b></span><span><span>Protocol</span><b>${esc(serverProtocol(s))}</b></span><span><span>Uptime</span><b>${s.running?fmtUptime(s.uptimeSeconds):'—'}</b></span><span><span>Files</span><b>${s.installed?'Ready':'Not installed'}</b></span></span>
      </button>
      <div class="server-card-actions"><button type="button" class="btn small ${actionClass}" data-fleet-action="${action}" data-id="${esc(s.id)}" ${transitioning||s.installBusy?'disabled':''}>${actionLabel}</button><button type="button" class="btn small" data-fleet-open="${esc(s.id)}">Manage</button></div>
    </article>`}).join('');
  $$('[data-fleet-open]').forEach(b=>b.addEventListener('click',()=>selectServer(b.dataset.fleetOpen)));
  $$('[data-fleet-action]').forEach(b=>b.addEventListener('click',e=>{e.stopPropagation();fleetAction(b.dataset.id,b.dataset.fleetAction)}));
}
// The only place the selected server changes. Everything that belongs to the
// previous server (requests, live stream, cached data and rendered values) is
// dropped before anything for the next server is requested.
function setSelectedServer(id){
  state.selectionGeneration++;
  state.selectionAbort.abort();
  state.selectionAbort=new AbortController();
  disconnectEvents();
  state.serverId=id||null;
  if(id){try{localStorage.setItem('realmkeeper-server',id)}catch{}}
  state.logs=[];
  state.liveLogBuffers=new Set();
  state.settings=null;
  state.status=null;
  state.resources=null;
  state.players=null;
  state.workspaceError=null;
  managedPath='.';
  managedOpenPath='';
  managedDirty=false;
  resetWorkspaceView();
}
function resetWorkspaceView(){
  const summary=selectedSummary();
  $('#server-name').textContent=summary?.serverName||'Loading server…';
  renderServerHead();
  $('#hero-badge').className='status-badge';
  $('#top-status').innerHTML='<span class="dot"></span><span>Loading…</span>';
  $('#console-output').innerHTML='';
  $('#mini-log').innerHTML='<span class="muted">Loading server output…</span>';
  $('#managed-files').innerHTML='<div class="empty-state">Loading files…</div>';
  $('#file-breadcrumbs').innerHTML='';
  $('#file-editor').classList.add('hidden');
  $('#managed-file-content').value='';
  $('#server-config').value='';
  $('#config-note').textContent='';
  $('#mods-list').innerHTML='';
  $('#mc-properties-fields').innerHTML='';
  state.schedules=null;
  $('#schedule-list').innerHTML='<div class="empty-state">Loading schedules…</div>';
  $('#schedule-editor').classList.add('hidden');
  $('#native-backups').innerHTML='';
  $('#snapshots').innerHTML='';
  $('#install-state').textContent='Checking installation…';
  $('#players-list').innerHTML='<span class="muted">Waiting for server response…</span>';
  $('#players-message').textContent='';
  // Clear the settings form so one server's values can never be saved onto another.
  $('#settings-form').reset();
  $('#connect-address').textContent='—';
  $('#port-warning').classList.add('hidden');
  $('#metric-uptime').textContent='—';
  $('#metric-memory').textContent='—';
  $('#metric-memory-limit').textContent='—';
  $('#metric-port').textContent='—';
  $('#metric-installed').textContent='—';
  $('#metric-players').textContent='Unknown';
  $('#resource-cpu-value').textContent='—';
  $('#resource-memory-value').textContent='—';
  $('#resource-disk-value').textContent='—';
  $('#resource-sampled').textContent='Loading selected server…';
  $('#hero-dot').className='dot large';
  $('#hero-state').textContent='LOADING';
  $('#hero-detail').textContent='Loading details for '+(summary?.serverName||'the selected server')+'…';
  $('#hero-detail').classList.remove('hidden');
  for(const id of['start-btn','stop-btn','restart-btn'])$('#'+id).disabled=true;
  renderWorkspaceError();
}
async function selectServer(id){
  if(!id||!state.servers.some(s=>s.id===id))return;
  if(id===state.serverId){switchView('dashboard');return}
  if(managedDirty&&!confirm('Discard unsaved file changes?'))return;
  setSelectedServer(id);
  renderServerList();
  renderFleet();
  // Start on a page every game supports; tabs are recalculated for the new game.
  switchView('dashboard');
  await loadSelected();
}
async function loadSelected(){
  if(!state.serverId)return;
  const ctx=selectionContext();
  const caps=serverCapabilities();
  // Settings and status are essential; resources, logs and players are optional
  // and must not block or fail the rest of the workspace.
  const essential=Promise.allSettled([loadSettings(ctx),refreshStatus(ctx,false)]);
  const optional=Promise.allSettled([refreshResources(ctx),loadLogs(ctx),caps.playerList?refreshPlayers(ctx):Promise.resolve()]);
  const[results]=await Promise.all([essential,optional]);
  if(!ctx.current())return;
  const failed=results.find(result=>result.status==='rejected');
  setWorkspaceError(failed?(failed.reason?.message||'request failed'):null);
  connectEvents(ctx);
  loadCurrent();
}
async function fleetAction(id,action){if(action==='install'){await selectServer(id);switchView('setup');return}const server=state.servers.find(s=>s.id===id);if(action==='stop'&&!confirm(`Stop ${server?.serverName||'this server'}? Players may be disconnected.`))return;try{await api(`/api/servers/${encodeURIComponent(id)}/${action}`,{method:'POST',serverId:id});toast(`${action==='start'?'Start':'Stop'} requested`);setTimeout(async()=>{await loadServers(false);if(id===state.serverId)await refreshStatus()},450)}catch(e){toast(e.message,true)}}
async function refreshPlayers(ctx=selectionContext()){if(!ctx.id||!serverCapabilities().playerList)return;const button=$('#players-refresh');if(button)button.disabled=true;try{const players=await scopedApi(ctx,'/api/players');if(!ctx.current())return;state.players=players;renderPlayers()}catch(e){if(!ctx.current())return;state.players={known:false,players:[],message:e.message};renderPlayers()}finally{if(button&&ctx.current())button.disabled=false}}
function renderPlayers(){const p=state.players;if(!p)return;$('#metric-players').textContent=p.known?(p.count+' / '+p.maxPlayers):'Unknown';$('#meta-players-value').textContent=p.known?(p.count+' / '+p.maxPlayers+' players'):'Players unknown';$('#players-message').textContent=(p.message||(p.known?'Live player list':'Player count could not be verified'))+' · checked '+fmtTime(new Date());$('#players-list').innerHTML=p.known?(p.players&&p.players.length?p.players.map(name=>'<span class="player-chip">'+esc(name)+'</span>').join(''):'<span class="muted">No players online.</span>'):'<span class="muted">The server response was not recognized. Check the Console for the player-list output.</span>';if(state.current==='players')renderPmOnline()}
$('#players-refresh').addEventListener('click',()=>refreshPlayers());
async function refreshStatus(ctx=selectionContext(),quiet=true){if(!ctx.id)return;try{const status=await scopedApi(ctx,'/api/status');if(!ctx.current())return;state.status=status;renderStatus()}catch(e){if(!quiet&&ctx.current())throw e}}
async function refreshResources(ctx=selectionContext()){if(!ctx.id)return;try{const resources=await scopedApi(ctx,'/api/resources');if(!ctx.current())return;state.resources=resources;renderResources()}catch{}}
function renderResources(){
  const r=state.resources;if(!r)return;
  $('#resource-cpu-value').textContent=r.cpuAvailable?r.cpuPercent.toFixed(1)+'%':'Unavailable';
  $('#resource-memory-value').textContent=r.memoryAvailable?fmtBytes(r.containerMemoryBytes)+(r.containerMemoryLimitBytes>0?' / '+fmtBytes(r.containerMemoryLimitBytes):' used'):'Unavailable';
  $('#resource-disk-value').textContent=r.diskAvailable?fmtBytes(r.diskUsedBytes)+' / '+fmtBytes(r.diskTotalBytes):'Unavailable';
  $('#resource-sampled').textContent=r.sampledAt?'Updated '+fmtTime(r.sampledAt):'Waiting for metrics';
  // Container-wide samples, so the history is kept across server switches.
  const key=r.sampledAt||Date.now();
  if(usage.history[usage.history.length-1]?.key!==key){
    usage.history.push({key,time:r.sampledAt||new Date().toISOString(),
      cpu:r.cpuAvailable?Math.min(100,Math.max(0,r.cpuPercent)):null,
      memory:r.memoryAvailable&&r.containerMemoryLimitBytes>0?Math.min(100,r.containerMemoryBytes/r.containerMemoryLimitBytes*100):null,
      memoryBytes:r.containerMemoryBytes,disk:r.diskAvailable&&r.diskTotalBytes>0?r.diskUsedBytes/r.diskTotalBytes*100:null,diskBytes:r.diskUsedBytes});
    if(usage.history.length>USAGE_POINTS)usage.history.shift();
  }
  renderUsageChart();
}
// Usage chart: an SVG area chart of real samples, percentage on the y axis.
const USAGE_POINTS=90,usage={metric:'cpu',history:[],hover:null};
const USAGE_W=600,USAGE_H=220,USAGE_LEFT=34,USAGE_BOTTOM=200;
function usagePoints(){const pts=[];usage.history.forEach((h,i)=>{const v=h[usage.metric];if(v==null)return;pts.push({i,v,h,x:USAGE_LEFT+(USAGE_W-USAGE_LEFT)*(USAGE_POINTS<=1?0:(i+USAGE_POINTS-usage.history.length)/(USAGE_POINTS-1)),y:USAGE_BOTTOM-(USAGE_BOTTOM-10)*v/100})});return pts}
function renderUsageChart(){
  const svg=$('#usage-svg');if(!svg)return;
  const grid=[0,25,50,75,100].map(v=>{const y=USAGE_BOTTOM-(USAGE_BOTTOM-10)*v/100;return `<line class="usage-grid" x1="${USAGE_LEFT}" x2="${USAGE_W}" y1="${y}" y2="${y}"/><text class="usage-label" x="0" y="${y+4}">${v}%</text>`}).join('');
  const pts=usagePoints();
  let body='';
  if(pts.length<2){body=`<text class="usage-empty" x="${(USAGE_W+USAGE_LEFT)/2}" y="110" text-anchor="middle">${usage.metric==='memory'&&state.resources&&!(state.resources.containerMemoryLimitBytes>0)?'No container memory limit reported':'Collecting samples…'}</text>`}
  else{
    const line=pts.map((p,i)=>(i?'L':'M')+p.x.toFixed(1)+' '+p.y.toFixed(1)).join(' ');
    body=`<path class="usage-area" d="${line} L${pts[pts.length-1].x.toFixed(1)} ${USAGE_BOTTOM} L${pts[0].x.toFixed(1)} ${USAGE_BOTTOM} Z"/><path class="usage-line" d="${line}"/>`;
    const hp=usage.hover!=null&&pts.find(p=>p.i===usage.hover);
    if(hp)body+=`<line class="usage-cursor" x1="${hp.x}" x2="${hp.x}" y1="10" y2="${USAGE_BOTTOM}"/><circle cx="${hp.x}" cy="${hp.y}" r="5" fill="#fff" stroke="#4c8dff" stroke-width="3"/>`;
  }
  svg.innerHTML=`<defs><linearGradient id="usage-gradient" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#4c8dff" stop-opacity=".35"/><stop offset="1" stop-color="#4c8dff" stop-opacity="0"/></linearGradient></defs>${grid}${body}`;
}
function updateUsageTooltip(clientX){
  const chart=$('#usage-chart'),tip=$('#usage-tooltip'),pts=usagePoints();
  if(!pts.length){tip.classList.add('hidden');return}
  const rect=chart.getBoundingClientRect(),x=(clientX-rect.left)/rect.width*USAGE_W;
  const p=pts.reduce((a,b)=>Math.abs(b.x-x)<Math.abs(a.x-x)?b:a);
  usage.hover=p.i;renderUsageChart();
  const names={cpu:'CPU usage',memory:'RAM usage',disk:'Storage used'};
  const extra=usage.metric==='memory'?fmtBytes(p.h.memoryBytes):usage.metric==='disk'?fmtBytes(p.h.diskBytes):'';
  tip.innerHTML=`<span class="muted">${names[usage.metric]} · ${esc(fmtTime(p.h.time))}</span><strong>${p.v.toFixed(1)}%</strong>${extra?`<span class="muted">${esc(extra)}</span>`:''}`;
  tip.style.left=Math.min(Math.max(p.x/USAGE_W*rect.width,70),rect.width-70)+'px';
  tip.classList.remove('hidden');
}
$('#usage-chart').addEventListener('pointermove',e=>updateUsageTooltip(e.clientX));
$('#usage-chart').addEventListener('pointerleave',()=>{usage.hover=null;$('#usage-tooltip').classList.add('hidden');renderUsageChart()});
$$('[data-usage]').forEach(b=>b.addEventListener('click',()=>{usage.metric=b.dataset.usage;usage.hover=null;$('#usage-tooltip').classList.add('hidden');$$('[data-usage]').forEach(x=>{const on=x===b;x.classList.toggle('active',on);x.setAttribute('aria-selected',String(on))});renderUsageChart()}));
function renderStatus(){
  const s=state.status,on=!!s?.running,busy=!!s?.installBusy,transitioning=s?.state==='starting'||s?.state==='stopping',label=statusLabel(s),tone=statusDotClass(s);
  $('#top-status').innerHTML=`<span class="dot ${tone}"></span><span>${esc(label)}</span>`;
  $('#hero-dot').className='dot '+tone;
  $('#hero-badge').className='status-badge '+tone;
  $('#hero-state').textContent=label;
  const detail=s?.state==='crashed'?`Process exited with code ${s.exitCode}${s.lastExitAt?' at '+fmtDate(s.lastExitAt):''}. Check the console for the error.`:s?.state==='starting'?'The server process has launched; waiting for it to finish initializing.':s?.state==='stopping'?'A graceful shutdown was requested. Waiting for the process to exit.':s?.state==='not-installed'?'Install server files to start this server.':s?.lastExitAt?`Last exit: ${fmtDate(s.lastExitAt)}.`:'';
  $('#hero-detail').textContent=detail;$('#hero-detail').classList.toggle('hidden',!detail);
  $('#metric-uptime').textContent=fmtUptime(s?.uptimeSeconds);
  $('#metric-memory').textContent=fmtBytes(s?.memoryBytes);
  $('#metric-memory-limit').textContent=fmtBytes(s?.memoryLimitBytes);
  $('#metric-installed').textContent=s?.installed?'Ready':'Not installed';
  $('#memory-warning').classList.toggle('hidden',s?.meetsMinimumMemory!==false);
  // One primary lifecycle button: Start while stopped, Stop while running.
  const start=$('#start-btn'),stop=$('#stop-btn'),restart=$('#restart-btn');
  start.classList.toggle('hidden',on);stop.classList.toggle('hidden',!on);restart.classList.toggle('hidden',!on);
  start.disabled=on||busy||!s?.installed;stop.disabled=!on||s?.state==='stopping';restart.disabled=!on||transitioning||busy;
  start.lastChild.textContent=s&&!s.installed?'Install first':'Start Server';
  stop.lastChild.textContent=s?.state==='stopping'?'Stopping…':'Stop Server';
  if(state.settings){$('#metric-port').textContent=state.settings.port;updateAddress()}
  renderPortWarning();
  if(state.current==='config')setConfigEditState(!on);
}
async function loadSettings(ctx=selectionContext()){if(!ctx.id)return;const settings=await scopedApi(ctx,'/api/settings');if(!ctx.current())return;state.settings=settings;const s=settings;const protocol=serverProtocol(selectedSummary());$('#server-name').textContent=s.serverName;$('#set-name').value=s.serverName;$('#set-host').value=s.publicHost||'';$('#set-port').value=s.port;$('#set-port').min=s.portMin||1;$('#set-port').max=s.portMax||65535;$('#port-help').textContent='Published '+protocol+' range: '+(s.portMin||1)+'–'+(s.portMax||65535)+'. Each server must use a unique port.';const rules=selectedSummary()?.rules||rulesForGame(s.gameType);setModeOptions($('#set-mode'),rules.gameModes,s.gameMode);$('#set-mode-label').textContent=rules.modeLabel;$('#set-mode-help').textContent=rules.modeFixed?'Set when the world was created; it cannot be changed.':'Default mode for newly created worlds.';$('#set-players').value=s.maxPlayers||20;$('#set-motd').value=s.motd||'';$('#set-password').value='';$('#set-password').placeholder=s.passwordSet?'Password set · leave blank to keep it':'Leave blank for no password';$('#set-password-clear').checked=false;$('#set-password-clear-row').classList.toggle('hidden',!s.passwordSet||s.gameType==='runescape-dragonwilds');$('#set-whitelist').checked=!!s.whitelistEnabled;$('#hytale-patchline').value=s.hytalePatchline||'release';syncPatchlineButton();$('#set-xms').value=s.memoryMin;$('#set-xmx').value=s.memoryMax;$('#set-args').value=s.extraArgs||'';$('#set-autostart').checked=s.autoStart;$('#set-autorestart').checked=!!s.autoRestart;
const dragon=s.gameType==='runescape-dragonwilds';
$('#dragonwilds-settings').classList.toggle('hidden',!dragon);
$('#hytale-settings-note').classList.toggle('hidden',dragon);
$('#dragonwilds-player-limit').classList.toggle('hidden',!dragon);
$$('.hytale-settings-only').forEach(el=>{el.classList.toggle('hidden',dragon);el.querySelectorAll('input,select,textarea').forEach(input=>input.disabled=dragon)});
$('#set-players').max=rules.playerLimit;
$('#set-mode').disabled=dragon||!!rules.modeFixed;
[['#set-whitelist-row',rules.whitelist],['#set-xms-row',rules.jvmMemory],['#set-xmx-row',rules.jvmMemory]].forEach(([sel,on])=>{const row=$(sel),show=on&&!dragon;row.classList.toggle('hidden',!show);row.querySelectorAll('input').forEach(i=>i.disabled=!show)});
const terraria=s.gameType==='terraria';$('#terraria-settings-note').classList.toggle('hidden',!terraria);if(terraria)$('#terraria-settings-text').innerHTML='<b>World: '+esc(s.terrariaWorldName||'')+'</b> · '+esc(s.terrariaWorldSize||'')+' · '+esc(s.gameMode||'')+'. These were fixed when the world was created. Realmkeeper writes <code>serverconfig.txt</code> before each start.';
$('#set-dragonwilds-owner').value=s.dragonwildsOwnerId||'';
$('#set-dragonwilds-world').value=s.dragonwildsWorldName||'Dragonwilds World';
$('#set-dragonwilds-admin').value='';$('#set-dragonwilds-admin').placeholder=s.dragonwildsAdminPasswordSet?'Password set · leave blank to keep it':'Required';
$('#set-dragonwilds-password').value='';$('#set-dragonwilds-password').placeholder=s.dragonwildsWorldPasswordSet?'Password set · leave blank to keep it':'Leave empty to allow anyone to join';$('#set-dragonwilds-password-clear').checked=false;$('#set-dragonwilds-password-clear-row').classList.toggle('hidden',!(dragon&&s.dragonwildsWorldPasswordSet));
['set-dragonwilds-owner','set-dragonwilds-world','set-dragonwilds-admin','set-dragonwilds-password'].forEach(id=>{const input=$('#'+id);input.disabled=!dragon;input.required=dragon&&(id==='set-dragonwilds-owner'||id==='set-dragonwilds-world'||(id==='set-dragonwilds-admin'&&!s.dragonwildsAdminPasswordSet))});$('#metric-port').textContent=s.port;const sel=selectedSummary();$('#delete-server').disabled=!sel||!!sel.running;$('#delete-server').title=sel?.running?'Stop this server before deleting it.':'';updateAddress()}
// The app picks ports, but only Docker/CasaOS can publish them; say so when
// the selected server's port is not published by the container.
function renderPortWarning(){const sum=selectedSummary(),show=sum?.portPublished===false&&!!sum.protocol;$('#port-warning').classList.toggle('hidden',!show);if(!show)return;const p=serverProtocol(sum).toLowerCase();$('#port-warning-text').innerHTML=`<b>Port ${esc(String(sum.port))}/${esc(p.toUpperCase())} is not published.</b> Players can't connect until you add <code>${esc(String(sum.port))}:${esc(String(sum.port))}/${esc(p)}</code> to the ports in the CasaOS or Docker Compose app settings.`}
function updateAddress(){const s=state.settings;if(!s)return;$('#connect-address').textContent=`${s.publicHost||location.hostname}:${s.port}`}
$('#copy-address').addEventListener('click',async()=>{try{await navigator.clipboard.writeText($('#connect-address').textContent);toast('Address copied')}catch{toast('Could not copy address',true)}});
for(const id of['start-btn','setup-start'])$('#'+id).addEventListener('click',()=>serverAction('start'));$('#stop-btn').addEventListener('click',()=>serverAction('stop'));$('#restart-btn').addEventListener('click',()=>serverAction('restart'));
// Lifecycle actions name the server in the URL, captured before any confirmation.
async function serverAction(action){const id=state.serverId;if(!id)return;if(action==='start'&&state.status&&!state.status.installed){switchView('setup');return}const name=selectedSummary()?.serverName||'this server';if(action==='stop'&&!confirm(`Stop ${name}? Players may be disconnected.`))return;if(action==='restart'&&!confirm(`Restart ${name}? Players will be disconnected briefly.`))return;try{await api(`/api/servers/${encodeURIComponent(id)}/${action}`,{method:'POST',serverId:id});toast(`${action[0].toUpperCase()+action.slice(1)} requested for ${name}`);setTimeout(async()=>{await loadServers(false);if(id===state.serverId)await refreshStatus()},450)}catch(e){toast(e.message,true)}}
const logKey=x=>x.time+'\u0000'+x.source+'\u0000'+x.line;
// Lines that stream in while a snapshot request is in flight are buffered and
// merged without duplicates, so reconnecting never drops or repeats output.
async function loadLogs(ctx=selectionContext()){if(!ctx.id)return;const buffer=[];state.liveLogBuffers.add(buffer);try{const logs=await scopedApi(ctx,'/api/logs');if(!ctx.current())return;const seen=new Set(logs.slice(-500).map(logKey));state.logs=logs.concat(buffer.filter(x=>!seen.has(logKey(x)))).slice(-2500);renderLogs()}catch{}finally{state.liveLogBuffers.delete(buffer)}}
function renderLogs(){const out=$('#console-output');out.removeAttribute('data-cleared');out.innerHTML=state.logs.map(logHTML).join('');if($('#auto-scroll').checked)out.scrollTop=out.scrollHeight;renderMiniLog()}
function renderMiniLog(){const recent=state.logs.slice(-40);$('#mini-log').innerHTML=recent.map(x=>`<div class="mini-line ${esc(x.source)}"><span class="time">${fmtTime(x.time)}</span>${esc(x.line)}</div>`).join('')||'<span class="muted">No server output yet.</span>';const mini=$('#mini-log');mini.scrollTop=mini.scrollHeight}
function logHTML(x){return`<div class="log-line ${esc(x.source)}"><span class="stamp">${fmtTime(x.time)}</span><span class="source">${esc(x.source)}</span>${esc(x.line)}</div>`}
function disconnectEvents(){state.events?.close();state.events=null;state.eventsOpen=false}
function connectEvents(ctx=selectionContext()){
  disconnectEvents();
  if(!ctx.id)return;
  const es=new EventSource(`/api/events?server=${encodeURIComponent(ctx.id)}`);
  state.events=es;
  const live=()=>state.events===es&&ctx.current();
  // Each (re)connection resynchronises from the server's log snapshot to cover
  // lines emitted before the stream subscribed or while it was disconnected.
  es.onopen=()=>{if(!live())return;state.eventsOpen=true;loadLogs(ctx)};
  es.onerror=()=>{if(state.events===es)state.eventsOpen=false};
  es.onmessage=e=>{if(!live())return;let line;try{line=JSON.parse(e.data)}catch{return}appendLogLine(line)};
}
function appendLogLine(line){for(const buffer of state.liveLogBuffers)buffer.push(line);state.logs.push(line);if(state.logs.length>2500)state.logs=state.logs.slice(-2500);const out=$('#console-output');out.removeAttribute('data-cleared');out.insertAdjacentHTML('beforeend',logHTML(line));while(out.childElementCount>2500)out.firstElementChild.remove();if($('#auto-scroll').checked)out.scrollTop=out.scrollHeight;renderMiniLog()}
$('#console-form').addEventListener('submit',async e=>{e.preventDefault();const id=state.serverId,i=$('#console-command'),command=i.value.trim();if(!id||!command)return;try{await api('/api/console',{method:'POST',serverId:id,body:JSON.stringify({command})});if(id===state.serverId)i.value=''}catch(e){toast(e.message,true)}});$('#clear-console').addEventListener('click',()=>{state.logs=[];const out=$('#console-output');out.innerHTML='';out.dataset.cleared='true';renderMiniLog()});
$('#copy-logs').addEventListener('click',async()=>{const text=state.logs.map(x=>`[${fmtTime(x.time)}] [${x.source}] ${x.line}`).join('\n');if(!text){toast('There are no logs to copy');return}try{if(navigator.clipboard?.writeText){await navigator.clipboard.writeText(text)}else{throw new Error('Clipboard API unavailable')}}catch{const ta=document.createElement('textarea');ta.value=text;ta.style.position='fixed';ta.style.opacity='0';document.body.appendChild(ta);ta.select();const ok=document.execCommand('copy');ta.remove();if(!ok){toast('Could not copy logs. Select and copy the console text instead.',true);return}}toast('Console logs copied')});
$$('[data-action]').forEach(b=>b.addEventListener('click',async()=>{const id=state.serverId;if(!id)return;try{if(b.dataset.action==='auth'){await api('/api/auth/device',{method:'POST',serverId:id});if(id===state.serverId)switchView('console');toast('Device login started')}if(b.dataset.action==='backup'){if(serverCapabilities().nativeBackups){await api('/api/backups/native',{method:'POST',serverId:id});toast('Backup requested')}else{toast('Creating snapshot…');await api('/api/backups/snapshot',{method:'POST',serverId:id});toast('Snapshot created')}}}catch(e){toast(e.message,true)}}));
// Hytale release channel: switching takes a snapshot of a stopped server
// first; the Game updates buttons then move it to that channel's build.
function syncPatchlineButton(){const current=state.settings?.hytalePatchline||'release';$('#hytale-patchline-switch').disabled=$('#hytale-patchline').value===current}
$('#hytale-patchline').addEventListener('change',syncPatchlineButton);
$('#hytale-patchline-switch').addEventListener('click',async()=>{const id=state.serverId,patchline=$('#hytale-patchline').value;if(!id)return;
 const warning=patchline==='pre-release'?'Switch this server to the pre-release channel? Pre-release builds can be unstable, players must switch their launcher to pre-release to join, and a world opened on pre-release may not load on release again.':'Switch this server back to the release channel? A world opened on pre-release may not load on the release build; restore a snapshot from before the switch if it does not.';
 if(!confirm(warning))return;const b=$('#hytale-patchline-switch');b.disabled=true;
 try{const r=await api('/api/hytale/patchline',{method:'POST',serverId:id,body:JSON.stringify({patchline})});if(id!==state.serverId)return;if(state.settings)state.settings.hytalePatchline=patchline==='release'?'':patchline;toast(r.message)}catch(e){toast(e.message,true)}finally{syncPatchlineButton()}});
$$('[data-update]').forEach(b=>b.addEventListener('click',async()=>{const id=state.serverId,action=b.dataset.update;if(!id)return;if(action==='apply'&&!confirm('Apply the staged Hytale update to this server now?'))return;try{await api(`/api/update/${action}`,{method:'POST',serverId:id});if(id===state.serverId)switchView('console');toast(`Update ${action} requested`)}catch(e){toast(e.message,true)}}));
$('#setup-auth').addEventListener('click',async()=>{const id=state.serverId;if(!id)return;try{await api('/api/auth/device',{method:'POST',serverId:id});if(id===state.serverId)switchView('console');toast('Device login started')}catch(e){toast(e.message,true)}});
function setConfigEditState(editable){$('#server-config').disabled=!editable;$('#save-config').disabled=!editable;$('#config-running-note').classList.toggle('hidden',editable)}
async function loadServerConfig(){const ctx=selectionContext();if(!ctx.id)return;state.configFile=$('#config-file').value;if(!state.configFile)return;try{const d=await scopedApi(ctx,`/api/server/file/${encodeURIComponent(state.configFile)}`);if(!ctx.current())return;let c=d.content;try{c=JSON.stringify(JSON.parse(c),null,2)}catch{}$('#server-config').value=c;$('#config-note').textContent=d.exists?`Loaded ${d.name}`:`${d.name} does not exist yet`;$('#config-mode-note').textContent=d.running?'The server is running. Stop it before editing and saving configuration files.':'The server is stopped. Changes are validated as JSON before saving.';setConfigEditState(d.editable)}catch(e){if(ctx.current())toast(e.message,true)}}
$('#config-file').addEventListener('change',loadServerConfig);$('#reload-config').addEventListener('click',loadServerConfig);$('#save-config').addEventListener('click',async()=>{const id=state.serverId,name=$('#config-file').value;if(!id||!name)return;try{JSON.parse($('#server-config').value);await api(`/api/server/file/${encodeURIComponent(name)}`,{method:'PUT',serverId:id,body:JSON.stringify({content:$('#server-config').value})});toast('Configuration saved');if(id===state.serverId)await loadServerConfig()}catch(e){toast(e.message,true)}});
// Paper calls its add-ons plugins; other games call them mods.
const MINECRAFT_VARIANTS={vanilla:{name:'Vanilla',help:"Mojang's official server, without plugins or mods."},paper:{name:'Paper',help:'High-performance server with Bukkit/Paper plugins (.jar) in the plugins folder.'},fabric:{name:'Fabric',help:'Fabric loader with mods (.jar) in the mods folder. Add Fabric API for most mods.'},quilt:{name:'Quilt',help:'Quilt loader with mods (.jar) in the mods folder. Runs most Fabric mods; add Quilted Fabric API.'},forge:{name:'Forge',help:'Minecraft Forge with mods (.jar) in the mods folder. Minecraft 1.17 or newer; the recommended build is used when there is one.'},neoforge:{name:'NeoForge',help:'NeoForge with mods (.jar) in the mods folder. Minecraft 1.20.2 or newer; the newest stable build is used.'}};
function modsKind(summary=selectedSummary()){return serverCapabilities(summary).modsKind==='plugins'?'plugins':'mods'}
const TERRARIA_VARIANTS={vanilla:{name:'vanilla',help:'The official dedicated server, without mods. x86-64 hosts only.',placeholder:'latest or 1.4.4.9',version:/^(latest|\d+(\.\d+){0,4})$/},tmodloader:{name:'tModLoader',help:'Terraria with mods (.tmod) from the Mods tab. Downloads .NET on first start; runs on x86-64 and ARM.',placeholder:'latest or v2025.08.3.0',version:/^(latest|v\d{4}\.\d{1,2}\.\d{1,3}\.\d{1,4})$/}};
function renderModsLabels(summary){const plugins=modsKind(summary)==='plugins',word=plugins?'Plugins':'Mods',game=summary?.gameType;$('#mods-tab-label').textContent=word;$('#mods-title').textContent=word;$('#mods-add-label').textContent=plugins?'Add plugin':'Add mod';$('#mod-file').accept=game==='hytale'?'.jar,.zip':game==='terraria'?'.tmod':'.jar';$('#mods-help').textContent=game==='terraria'?'Upload .tmod files; Realmkeeper enables them in Mods/enabled.json. Restart the server to load changes.':'Installed for the selected server only. Restart the server to load changes.'}
// Modrinth: search for mods or plugins that fit the server's loader and
// Minecraft version, install them with their required dependencies, and
// update jars Modrinth knows.
async function modrinthSearch(q,offset=0,ctx=selectionContext()){if(!ctx.id)return;
 if(!offset){state.mr={server:ctx.id,q,hits:[],total:0};$('#modrinth-results').innerHTML='<div class="empty-state">Searching Modrinth…</div>';$('#modrinth-more').classList.add('hidden')}
 let d;try{d=await scopedApi(ctx,'/api/modrinth/search?q='+encodeURIComponent(q)+'&offset='+offset)}catch(e){if(ctx.current()&&state.mr?.q===q)$('#modrinth-results').innerHTML='<div class="empty-state">'+esc(e.message)+'</div>';return}
 if(!ctx.current()||state.mr?.server!==ctx.id||state.mr.q!==q)return;
 state.mr.hits=state.mr.hits.concat(d.hits);state.mr.total=d.total;
 const kind=modsKind()==='plugins'?'Plugins':'Mods',loaders=(d.loaders||[]).join(', ');
 $('#modrinth-help').textContent=(d.gameVersion?`${kind} for ${loaders} on Minecraft ${d.gameVersion}`:`${kind} for ${loaders}. Install the server to also filter by Minecraft version`)+', limited to ones that run on servers. Required dependencies are installed too.';
 renderModrinthResults()}
function modrinthRow(h){const icon=h.icon_url?`<img src="${esc(h.icon_url)}" alt="" loading="lazy">`:'<svg class="icon"><use href="#i-puzzle"/></svg>';
 return `<div class="mod-row" data-project="${esc(h.project_id)}"><span class="schedule-icon">${icon}</span><div><strong>${esc(h.title)}${h.installed?'<span class="world-badge">Installed</span>':''}</strong><span class="when">by ${esc(h.author||'unknown')} · ${fmtCount(h.downloads)} downloads · <a href="https://modrinth.com/project/${encodeURIComponent(h.slug||h.project_id)}" target="_blank" rel="noreferrer">Modrinth page</a></span><span class="desc">${esc(h.description||'')}</span></div><span></span><div class="actions"><button type="button" class="btn small" data-mr-versions>Choose version</button><button type="button" class="btn small primary" data-mr-install>${h.installed?'Reinstall':'Install'}</button></div></div>`}
function renderModrinthResults(){const mr=state.mr;if(!mr)return;$('#modrinth-results').innerHTML=mr.hits.map(modrinthRow).join('')||'<div class="empty-state">Nothing on Modrinth matches for this server.</div>';$('#modrinth-more').classList.toggle('hidden',mr.hits.length>=mr.total)}
async function modrinthInstall(body,button){const id=state.serverId;if(!id)return;const label=button.textContent;button.disabled=true;button.textContent='Installing…';
 try{const r=await api('/api/modrinth/install',{method:'POST',serverId:id,body:JSON.stringify(body)});toast(r.message);if(id!==state.serverId)return;
  for(const i of r.installed||[]){const hit=state.mr?.hits.find(h=>h.project_id===i.projectId);if(hit)hit.installed=true}
  renderModrinthResults();loadMods();return true}
 catch(e){toast(e.message,true);button.disabled=false;button.textContent=label}}
$('#modrinth-results').addEventListener('click',async e=>{const b=e.target.closest('[data-mr-install],[data-mr-versions]');if(!b||b.disabled)return;const row=b.closest('[data-project]'),project=row.dataset.project;
 if(b.matches('[data-mr-install]')){modrinthInstall({projectId:project,versionId:row.querySelector('select')?.value||''},b);return}
 const id=state.serverId;b.disabled=true;try{const d=await api('/api/modrinth/projects/'+encodeURIComponent(project)+'/versions',{serverId:id});if(id!==state.serverId)return;
  if(!d.versions.length){toast('No version of this project fits the server',true);return}
  const select=document.createElement('select');select.setAttribute('aria-label','Version');select.innerHTML=d.versions.map(v=>`<option value="${esc(v.id)}">${esc(v.versionNumber)}${v.versionType!=='release'?' ('+esc(v.versionType)+')':''} · ${esc(fmtDate(v.published))}</option>`).join('');b.replaceWith(select)}
 catch(err){toast(err.message,true);b.disabled=false}});
$('#modrinth-search').addEventListener('submit',e=>{e.preventDefault();modrinthSearch($('#modrinth-query').value.trim(),0)});
$('#modrinth-more').addEventListener('click',()=>{if(state.mr)modrinthSearch(state.mr.q,state.mr.hits.length)});
$('#modrinth-updates').addEventListener('click',async()=>{const id=state.serverId;if(!id)return;const b=$('#modrinth-updates');b.disabled=true;
 try{const d=await api('/api/modrinth/updates',{serverId:id});if(id!==state.serverId)return;
  $('#modrinth-updates-list').innerHTML=d.updates.length?d.updates.map(u=>`<div class="mod-row" data-update-file="${esc(u.file)}" data-project="${esc(u.projectId)}" data-version="${esc(u.versionId)}"><span class="schedule-icon"><svg class="icon"><use href="#i-download"/></svg></span><div><strong>${esc(u.title)}</strong><span class="when">${esc(u.file)} → ${esc(u.versionNumber)}</span></div><span></span><div class="actions"><button type="button" class="btn small primary" data-mr-update>Update</button></div></div>`).join(''):`<div class="empty-state">Checked ${d.checked} file${d.checked===1?'':'s'}: everything Modrinth knows is up to date.</div>`}
 catch(e){toast(e.message,true)}finally{b.disabled=false}});
$('#modrinth-updates-list').addEventListener('click',async e=>{const b=e.target.closest('[data-mr-update]');if(!b||b.disabled)return;const row=b.closest('[data-update-file]');
 if(await modrinthInstall({projectId:row.dataset.project,versionId:row.dataset.version,replace:row.dataset.updateFile},b))row.remove()});
// Minecraft players: whitelist, operators and bans. A running server is changed
// through its console; a stopped server's files are edited and read on start.
async function loadMinecraftPlayers(ctx=selectionContext()){if(!ctx.id)return;state.pm=null;let d;try{d=await scopedApi(ctx,'/api/minecraft/players')}catch(e){if(ctx.current())toast(e.message,true);return}if(!ctx.current())return;renderMinecraftPlayers(d);if(d.running)refreshPlayers(ctx);else renderPmOnline()}
function pmButton(action,name,label,cls=''){return `<button type="button" class="btn small ${cls}" data-pm-action="${action}" data-pm-name="${esc(name)}">${label}</button>`}
function pmRow(name,sub,actions){return `<div class="player-row"><span class="schedule-icon"><svg class="icon"><use href="#i-users"/></svg></span><div><strong>${esc(name)}</strong><span class="when">${sub}</span></div><span></span><div class="actions">${actions}</div></div>`}
function pmHas(list,name){return (list||[]).some(p=>(p.name||'').toLowerCase()===name.toLowerCase())}
function renderMinecraftPlayers(d){state.pm=d;
 $('#pm-mode').classList.toggle('warning',!d.running);
 $('#pm-mode-text').innerHTML=d.running?'<b>The server is running.</b> Changes apply straight away through its console.':'<b>The server is stopped.</b> Changes are saved to its files and apply when it starts. Names are checked with Mojang.';
 $('#pm-whitelist-on').checked=!!d.whitelistEnabled;$('#pm-whitelist-state').textContent=d.whitelistEnabled?'On: players not listed below are refused.':'Off: anyone who is not banned can join.';
 $('#pm-whitelist').innerHTML=d.whitelist.map(p=>pmRow(p.name,'Whitelisted',pmButton('whitelist-remove',p.name,'Remove'))).join('')||'<div class="empty-state">Nobody is on the whitelist.</div>';
 $('#pm-ops').innerHTML=d.ops.map(p=>pmRow(p.name,'Level '+(p.level||4),pmButton('deop',p.name,'Remove operator'))).join('')||'<div class="empty-state">No operators.</div>';
 const since=b=>[b.reason?esc(b.reason):'',b.created?'since '+esc(b.created.slice(0,10)):'',b.expires&&b.expires!=='forever'?'until '+esc(b.expires.slice(0,10)):''].filter(Boolean).join(' · ');
 $('#pm-bans').innerHTML=d.bans.map(b=>pmRow(b.name,since(b),pmButton('pardon',b.name,'Unban'))).concat(d.ipBans.map(b=>pmRow(b.ip,'Address · '+since(b),pmButton('pardon-ip',b.ip,'Unban')))).join('')||'<div class="empty-state">Nobody is banned.</div>';
 renderPmOnline()}
function renderPmOnline(){const d=state.pm,p=state.players;if(!d)return;
 if(!d.running){$('#pm-online-help').textContent='Start the server to see who is online and to kick players.';$('#pm-online').innerHTML='<div class="empty-state">The server is stopped.</div>';$('#pm-online-refresh').disabled=true;return}
 $('#pm-online-refresh').disabled=false;$('#pm-online-help').textContent=p?.known?p.players.length+' of '+p.maxPlayers+' players online.':'Players connected to the server.';
 if(!p?.known){$('#pm-online').innerHTML='<div class="empty-state">'+(p?'The player list could not be read. Check the Console.':'Checking…')+'</div>';return}
 $('#pm-online').innerHTML=p.players.map(name=>pmRow(name,pmHas(d.ops,name)?'Operator':'Player',(d.whitelistEnabled&&!pmHas(d.whitelist,name)?pmButton('whitelist-add',name,'Whitelist'):'')+(pmHas(d.ops,name)?pmButton('deop',name,'Remove operator'):pmButton('op',name,'Make operator'))+pmButton('kick',name,'Kick')+pmButton('ban',name,'Ban','danger'))).join('')||'<div class="empty-state">No players online.</div>'}
async function pmAction(action,name){const id=state.serverId;if(!id)return;let reason='';
 if(action==='ban'||action==='ban-ip'||action==='kick'){const verb=action==='kick'?'Kick':'Ban';reason=prompt(`${verb} ${name}? Reason (optional):`,'');if(reason===null)return}
 try{const d=await api('/api/minecraft/players',{method:'POST',serverId:id,body:JSON.stringify({action,name,reason})});if(id!==state.serverId)return;toast(d.message||'Done');renderMinecraftPlayers(d);
  if(action==='whitelist-on'||action==='whitelist-off'){if(state.settings)state.settings.whitelistEnabled=d.whitelistEnabled;$('#set-whitelist').checked=d.whitelistEnabled}
  if(d.running&&(action==='kick'||action==='ban'))setTimeout(()=>{if(id===state.serverId)refreshPlayers()},800);return true}
 catch(e){toast(e.message,true);if(id===state.serverId)loadMinecraftPlayers()}}
$('#view-players').addEventListener('click',async e=>{const b=e.target.closest('[data-pm-action],[data-pm-add]');if(!b||b.disabled)return;
 if(b.dataset.pmAdd){const name=$('#pm-name').value.trim();if(!name){toast('Enter a username',true);return}b.disabled=true;if(await pmAction(b.dataset.pmAdd,name))$('#pm-name').value='';b.disabled=false;return}
 b.disabled=true;await pmAction(b.dataset.pmAction,b.dataset.pmName);b.disabled=false});
$('#pm-whitelist-on').addEventListener('change',e=>pmAction(e.target.checked?'whitelist-on':'whitelist-off',''));
$('#pm-ban-ip').addEventListener('click',async()=>{const ip=$('#pm-ip').value.trim();if(!ip){toast('Enter an IP address',true);return}if(await pmAction('ban-ip',ip))$('#pm-ip').value=''});
$('#pm-online-refresh').addEventListener('click',()=>refreshPlayers());
// Dragonwilds worlds: .sav files in the server's save folder. Changes need the
// server stopped, because the game rewrites saves while it runs.
async function loadWorlds(ctx=selectionContext()){if(!ctx.id)return;let d;try{d=await scopedApi(ctx,'/api/worlds')}catch(e){if(ctx.current())toast(e.message,true);return}if(!ctx.current())return;
 const running=!!selectedSummary()?.running;$('#worlds-running').classList.toggle('hidden',!running);$('#world-upload-btn').classList.toggle('disabled',running);$('#world-file').disabled=running;$('#new-world-btn').disabled=running;$('#new-world-name').placeholder=d.defaultWorldName?'e.g. '+d.defaultWorldName:'World name';
 const rows=d.worlds.map(w=>{const badges=(w.loadsNext?'<span class="world-badge">Loads next</span>':'')+(w.name===d.defaultWorldName?'<span class="world-badge">Default world name</span>':'');const current=w.loadsNext&&w.name===d.defaultWorldName;return `<div class="world-row"><span class="schedule-icon"><svg class="icon"><use href="#i-globe"/></svg></span><div><strong>${esc(w.name)}${badges}</strong><span class="when">${fmtBytes(w.size)} · saved ${esc(fmtDate(w.modified))}${w.hasBackup?' · has .backup':''}</span></div><span></span><div class="actions">${current?'':`<button type="button" class="btn small" data-world-action="use" data-world="${esc(w.name)}" ${running?'disabled':''}>Use this world</button>`}<button type="button" class="btn small" data-world-action="download" data-path="${esc(w.path)}">Download</button><button type="button" class="btn small" data-world-action="archive" data-world="${esc(w.name)}" ${running?'disabled':''}>Move to backups</button></div></div>`}).join('');
 $('#world-list').innerHTML=rows||'<div class="empty-state">No saved worlds yet. The next start creates a new Standard world named '+esc(d.defaultWorldName||'from Settings')+', or upload one.</div>';
 $('#world-archive-card').classList.toggle('hidden',!d.archives.length);
 $('#world-archives').innerHTML=d.archives.map(a=>`<div class="world-row"><span class="schedule-icon"><svg class="icon"><use href="#i-archive"/></svg></span><div><strong>${esc(a.id.replace(/^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2}).*$/,'$1-$2-$3 $4:$5:$6 UTC'))}</strong><span class="when">${esc(a.files.join(', '))}</span></div><span></span><div class="actions">${a.files.filter(f=>/\.sav$/i.test(f)).map(f=>`<button type="button" class="btn small" data-world-action="restore" data-world="${esc(f.replace(/\.sav$/i,''))}" data-archive="${esc(a.id)}" ${running?'disabled':''}>Restore ${esc(f.replace(/\.sav$/i,''))}</button>`).join('')}</div></div>`).join('');
}
async function worldRequest(path,body,message){const id=state.serverId;if(!id)return;try{await api(path,{method:'POST',serverId:id,body:JSON.stringify(body)});toast(message);if(id===state.serverId)loadWorlds()}catch(e){toast(e.message,true)}}
$('#view-worlds').addEventListener('click',e=>{const b=e.target.closest('[data-world-action]');if(!b||b.disabled)return;const name=b.dataset.world,action=b.dataset.worldAction;
 if(action==='download')downloadManagedFile(b.dataset.path);
 if(action==='use')worldRequest('/api/worlds/use',{name},name+' will load on the next start');
 if(action==='archive'&&confirm(`Move ${name} to world backups? You can restore it later.`))worldRequest('/api/worlds/archive',{name},name+' moved to world backups');
 if(action==='restore')worldRequest('/api/worlds/restore',{name,archive:b.dataset.archive},name+' restored and will load on the next start')});
$('#new-world-btn').addEventListener('click',()=>{const name=$('#new-world-name').value.trim();if(!name){toast('Enter a name for the new world',true);return}if(confirm(`Move every current save to world backups and start a new world called ${name}?`))worldRequest('/api/worlds/new',{name},'The next start creates '+name)});
$('#world-file').addEventListener('change',async e=>{const id=state.serverId,files=[...e.target.files];e.target.value='';if(!id||!files.length)return;const save=files.find(f=>/\.sav$/i.test(f.name)),backup=files.find(f=>/\.backup$/i.test(f.name));if(!save){toast('Choose the world .sav file (and optionally its .backup file)',true);return}const fd=new FormData();fd.append('file',save);if(backup)fd.append('backup',backup);try{const r=await api('/api/worlds/upload',{method:'POST',serverId:id,body:fd});toast('Uploaded '+r.name+'; it will load on the next start');if(id===state.serverId)loadWorlds()}catch(err){toast(err.message,true)}});
// Export streams a .tar.gz of the server (files, world, settings, schedules);
// import uploads one to create a new server, on this or another install.
$('#export-server').addEventListener('click',()=>{const s=selectedSummary();if(!s)return;if(s.running){toast('Stop the server before exporting it',true);return}const url='/api/servers/'+encodeURIComponent(s.id)+'/export'+($('#export-backups').checked?'?backups=1':'');const a=document.createElement('a');a.href=url;a.download='';document.body.appendChild(a);a.click();a.remove();toast('Export started. It includes this server\'s passwords, so keep the file private.')});
$$('[data-import-server]').forEach(b=>b.addEventListener('click',()=>$('#import-file').click()));
$('#import-file').addEventListener('change',e=>{const f=e.target.files[0];e.target.value='';if(!f)return;const xhr=new window.XMLHttpRequest();xhr.open('POST','/api/servers/import');xhr.setRequestHeader('Content-Type','application/gzip');xhr.upload.onprogress=ev=>{if(ev.lengthComputable)toast('Importing '+f.name+': '+Math.round(ev.loaded*100/ev.total)+'%')};xhr.onload=async()=>{let body={};try{body=JSON.parse(xhr.responseText)}catch{}if(xhr.status!==201){toast(body.error||'Import failed',true);return}toast('Imported '+body.serverName+(body.port?' on port '+body.port:''));await loadServers(false);await selectServer(body.id)};xhr.onerror=()=>toast('Import failed: the connection was interrupted',true);toast('Importing '+f.name+'…');xhr.send(f)});
async function loadMods(){const ctx=selectionContext();if(!ctx.id)return;try{const mods=await scopedApi(ctx,'/api/mods');if(!ctx.current())return;const el=$('#mods-list');el.classList.toggle('empty-state',!mods.length);el.innerHTML=mods.map(m=>`<div class="file-row"><span class="name">${esc(m.name)}</span><span class="meta">${fmtBytes(m.size)}</span><span class="meta">${fmtDate(m.modified)}</span><button data-del-mod="${encodeURIComponent(m.name)}">Remove</button></div>`).join('')||(modsKind()==='plugins'?'No plugins installed.':'No mods installed.');$$('[data-del-mod]').forEach(b=>b.addEventListener('click',()=>deleteMod(decodeURIComponent(b.dataset.delMod))));const browse=!!serverCapabilities().modBrowser;$$('.modrinth-only').forEach(el=>el.classList.toggle('hidden',!browse));if(browse&&state.mr?.server!==ctx.id){$('#modrinth-query').value='';$('#modrinth-updates-list').innerHTML='';modrinthSearch('',0,ctx)}}catch(e){if(ctx.current())toast(e.message,true)}}
$('#mod-file').addEventListener('change',async e=>{const id=state.serverId,f=e.target.files[0];if(!id||!f)return;const fd=new FormData();fd.append('file',f);try{await api('/api/mods/upload',{method:'POST',serverId:id,body:fd});toast('Mod uploaded');if(id===state.serverId)loadMods()}catch(err){toast(err.message,true)}finally{e.target.value=''}});async function deleteMod(name){const id=state.serverId;if(!id)return;if(!confirm(`Remove ${name} from this server?`))return;try{await api(`/api/mods/${encodeURIComponent(name)}`,{method:'DELETE',serverId:id});toast('Mod removed');if(id===state.serverId)loadMods()}catch(e){toast(e.message,true)}}
async function loadBackups(){const ctx=selectionContext();if(!ctx.id)return;try{if(state.settings){const freq=String(state.settings.backupFrequency||30);if(!Array.from($('#backup-frequency').options).some(o=>o.value===freq))$('#backup-frequency').add(new Option('Every '+freq+' minutes',freq));$('#backup-frequency').value=freq;$('#backup-retention').value=state.settings.backupRetention||10;$('#snapshot-retention').value=state.settings.snapshotRetention||5}const d=await scopedApi(ctx,'/api/backups');if(!ctx.current())return;renderFiles($('#native-backups'),d.native||[]);const snaps=d.snapshots||[],el=$('#snapshots');el.classList.toggle('empty-state',!snaps.length);el.innerHTML=snaps.map(m=>`<div class="file-row"><span class="name">${esc(m.name)}</span><span class="meta">${fmtBytes(m.size)}</span><span class="meta">${fmtDate(m.modified)}</span><span><button data-restore="${encodeURIComponent(m.name)}">Restore</button> <button data-del-snap="${encodeURIComponent(m.name)}">Delete</button></span></div>`).join('')||'No snapshots found.';$$('[data-restore]').forEach(b=>b.addEventListener('click',()=>restoreSnapshot(decodeURIComponent(b.dataset.restore))));$$('[data-del-snap]').forEach(b=>b.addEventListener('click',()=>deleteSnapshot(decodeURIComponent(b.dataset.delSnap))))}catch(e){if(ctx.current())toast(e.message,true)}}
function renderFiles(el,files){el.classList.toggle('empty-state',!files.length);el.innerHTML=files.map(m=>`<div class="file-row"><span class="name">${esc(m.name)}</span><span class="meta">${fmtBytes(m.size)}</span><span class="meta">${fmtDate(m.modified)}</span><span></span></div>`).join('')||'No backups found.'}
$('#save-backup-settings').addEventListener('click',async()=>{const id=state.serverId;if(!id||state.settings?.id!==id)return;const frequency=Number($('#backup-frequency').value),retention=Number($('#backup-retention').value),snapshots=Number($('#snapshot-retention').value);if(!Number.isInteger(retention)||retention<1||retention>100||!Number.isInteger(snapshots)||snapshots<1||snapshots>100){toast('Retention must be between 1 and 100 backups.',true);return}const button=$('#save-backup-settings');button.disabled=true;try{const payload={...state.settings,backupFrequency:frequency,backupRetention:retention,snapshotRetention:snapshots};const saved=await api('/api/settings',{method:'PUT',serverId:id,body:JSON.stringify(payload)});toast('Backup schedule and retention saved. Restart the server if the interval changed.');if(id!==state.serverId)return;state.settings=saved;await loadSettings();await loadBackups()}catch(e){toast(e.message,true)}finally{button.disabled=false}});
$('#native-backup').addEventListener('click',async()=>{const id=state.serverId;if(!id)return;try{await api('/api/backups/native',{method:'POST',serverId:id});toast('Backup requested');setTimeout(()=>{if(id===state.serverId)loadBackups()},1800)}catch(e){toast(e.message,true)}});$('#snapshot-backup').addEventListener('click',async()=>{const id=state.serverId;if(!id)return;try{await api('/api/backups/snapshot',{method:'POST',serverId:id});toast('Snapshot created');if(id===state.serverId)loadBackups()}catch(e){toast(e.message,true)}});async function restoreSnapshot(name){const id=state.serverId;if(!id)return;if(!confirm(`Restore ${name} to ${selectedSummary()?.serverName||'this server'}? Realmkeeper will create a safety snapshot first.`))return;try{const r=await api(`/api/backups/${encodeURIComponent(name)}/restore`,{method:'POST',serverId:id});toast(`Restored. Safety copy: ${r.safetySnapshot}`);if(id===state.serverId)loadBackups()}catch(e){toast(e.message,true)}}async function deleteSnapshot(name){const id=state.serverId;if(!id)return;if(!confirm(`Delete ${name}?`))return;try{await api(`/api/backups/${encodeURIComponent(name)}`,{method:'DELETE',serverId:id});toast('Snapshot deleted');if(id===state.serverId)loadBackups()}catch(e){toast(e.message,true)}}
async function loadInstallStatus(){
 const ctx=selectionContext();if(!ctx.id)return;
 try{
  const d=await scopedApi(ctx,'/api/install/status');if(!ctx.current())return;
  const dragon=d.game==='runescape-dragonwilds',minecraft=d.game==='minecraft-java',terraria=d.game==='terraria';
  $('#hytale-install-controls').classList.toggle('hidden',dragon||minecraft||terraria);
  $('#dragonwilds-install-controls').classList.toggle('hidden',!dragon);
  $('#minecraft-install-controls').classList.toggle('hidden',!minecraft);
  $('#terraria-install-controls').classList.toggle('hidden',!terraria);
  if(terraria){
   const tml=d.variant==='tmodloader',label=tml?'tModLoader':'Terraria';
   $('#install-state').innerHTML=d.installed?'<b style="color:var(--green)">'+label+' server installed</b><br><span class="muted">'+(d.installedVersion?'Release '+esc(d.installedVersion)+' · ':'')+esc(d.paths.game)+'</span>':'<b>'+label+' server not installed</b><br><span class="muted">Version: '+esc(d.version||'latest')+(d.platformSupported?'':' · needs an x86-64 (amd64) host')+(d.busy?' · installation running':'')+'</span>';
   $('#terraria-install-help').textContent=tml?'Fetches the tModLoader release from GitHub. The first start downloads the .NET runtime and generates the world, which can take several minutes.':'Fetches the official Terraria dedicated server from terraria.org and installs its Linux build. The world is generated on the first start, which can take a few minutes.';
   $('#terraria-install-note').innerHTML=tml?'Terraria uses TCP. Worlds live in <code>game/Worlds</code> and mods in <code>game/Mods</code>; add <code>.tmod</code> files from the Mods tab.':'Terraria uses TCP. The official Linux server runs only on x86-64 (amd64) hosts. World files live in <code>game/Worlds</code>.';
   $('#terraria-install').disabled=!!d.busy||d.installed||!d.platformSupported;$('#terraria-install').textContent=d.installed?'Server files installed':'Download '+(tml?'tModLoader':'vanilla')+' server';return;
  }
  if(dragon){
   $('#install-state').innerHTML=d.installed?'<b style="color:var(--green)">Dragonwilds server files installed</b><br><span class="muted">'+esc(d.paths.game)+'</span>':'<b>Dragonwilds server files not installed</b><br><span class="muted">SteamCMD: managed by the installer'+(d.busy?' · installation running':'')+'</span>';
   $('#dragonwilds-install').disabled=!!d.busy;$('#dragonwilds-install').textContent=d.installed?'Update dedicated server':'Install dedicated server';return;
  }
  if(minecraft){
   const variant=MINECRAFT_VARIANTS[d.variant]||MINECRAFT_VARIANTS.vanilla;
   $('#install-state').innerHTML=d.installed?'<b style="color:var(--green)">Minecraft Java ('+esc(variant.name)+') server files installed</b><br><span class="muted">'+esc(d.paths.game)+'</span>':'<b>Minecraft Java ('+esc(variant.name)+') server files not installed</b><br><span class="muted">Version: '+esc(d.version||'latest')+(d.busy?' · installation running':'')+'</span>';
   $('#minecraft-install-help').textContent=variant.help+' The first launch generates the remaining files.';
   $('#minecraft-install').disabled=!!d.busy||d.installed;$('#minecraft-install').textContent=d.installed?'Server files installed':(['forge','neoforge','quilt'].includes(d.variant)?'Install '+variant.name+' server':'Download '+variant.name+' server');return;
  }
  $('#install-state').innerHTML=d.installed?'<b style="color:var(--green)">Server files installed</b><br><span class="muted">'+esc(d.paths.game)+'</span>':'<b>Server files not installed</b><br><span class="muted">Shared downloader: '+(d.downloaderPresent?'ready':'not uploaded')+' · Instance game.zip: '+(d.bundlePresent?'found':'not found')+(d.busy?' · installer running':'')+'</span>';
  $('#run-downloader').disabled=d.busy||!d.downloaderPresent||d.installed;$('#import-local').disabled=d.busy||!d.bundlePresent||d.installed;
 }catch(e){if(ctx.current())toast(e.message,true)}
}
async function startInstall(url,message,opts={}){const id=state.serverId;if(!id)return;try{await api(url,{method:'POST',serverId:id,...opts});toast(message);if(id===state.serverId)switchView('console')}catch(e){toast(e.message,true)}}
$('#minecraft-install').addEventListener('click',()=>startInstall('/api/install/minecraft','Minecraft server download started',{body:'{}'}));
$('#terraria-install').addEventListener('click',()=>startInstall('/api/install/terraria','Terraria server download started',{body:'{}'}));
$('#dragonwilds-install').addEventListener('click',()=>startInstall('/api/install/dragonwilds','Dragonwilds installation started',{body:'{}'}));
$('#downloader-file').addEventListener('change',async e=>{const f=e.target.files[0];if(!f)return;const fd=new FormData();fd.append('file',f);try{await api('/api/install/downloader',{method:'POST',body:fd});toast('Shared Hytale downloader ready');loadInstallStatus()}catch(err){toast(err.message,true)}finally{e.target.value=''}});$('#run-downloader').addEventListener('click',()=>startInstall('/api/install/run-downloader','Downloader started'));$('#import-local').addEventListener('click',()=>startInstall('/api/install/import-local','Import started'));
// Settings are only saved when the form holds the selected server's loaded
// values; a form cleared by a server switch can never overwrite another server.
$('#settings-form').addEventListener('submit',async e=>{e.preventDefault();const id=state.serverId,loaded=state.settings;if(!id||loaded?.id!==id){toast('Server settings are still loading. Try again in a moment.',true);return}const s={id,serverName:$('#set-name').value.trim(),publicHost:$('#set-host').value.trim(),port:+$('#set-port').value,gameMode:$('#set-mode').value,maxPlayers:+$('#set-players').value,motd:$('#set-motd').value.trim(),password:$('#set-password').value,whitelistEnabled:$('#set-whitelist').checked,backupFrequency:loaded.backupFrequency,backupRetention:loaded.backupRetention,snapshotRetention:loaded.snapshotRetention,memoryMin:$('#set-xms').value.trim(),memoryMax:$('#set-xmx').value.trim(),extraArgs:$('#set-args').value.trim(),autoStart:$('#set-autostart').checked,autoRestart:$('#set-autorestart').checked,storageDir:loaded.storageDir||'',dragonwildsOwnerId:$('#set-dragonwilds-owner').value.trim(),dragonwildsWorldName:$('#set-dragonwilds-world').value.trim(),dragonwildsAdminPassword:$('#set-dragonwilds-admin').value,dragonwildsWorldPassword:$('#set-dragonwilds-password').value,clearPassword:$('#set-password-clear').checked,clearDragonwildsWorldPassword:$('#set-dragonwilds-password-clear').checked};if(loaded.gameType==='runescape-dragonwilds'){if(!s.dragonwildsOwnerId||!s.dragonwildsWorldName||(!s.dragonwildsAdminPassword&&!loaded.dragonwildsAdminPasswordSet)){toast('Owner ID, default world name, and admin password are required',true);return}if(s.dragonwildsWorldName.length>32){toast('Dragonwilds world name must be 32 characters or fewer',true);return}}try{const saved=await api('/api/settings',{method:'PUT',serverId:id,body:JSON.stringify(s)});toast(saved.gameType==='runescape-dragonwilds'?'Dragonwilds settings saved. Restart the server to apply changes.':state.status?.running?'Settings saved. Restart the server to apply launch-option changes.':'Server settings saved');await loadServers(false);if(id!==state.serverId)return;state.settings=saved;await loadSettings();renderStatus()}catch(e){toast(e.message,true)}});
$('#delete-server').addEventListener('click',async()=>{const s=selectedSummary();if(!s||s.running)return;const detail=s.storageDir===''||s.storageDir==='.'?' Its server-owned folders will be removed while manager configuration, shared tools, and other servers are preserved.':' Its server files, world, mods and snapshots will be permanently removed.';if(!confirm(`Delete ${s.serverName}?${detail}`))return;try{await api(`/api/servers/${encodeURIComponent(s.id)}`,{method:'DELETE',serverId:s.id});toast('Server deleted');setSelectedServer(null);await loadServers(true);if(state.serverId)await loadSelected();switchView('servers')}catch(e){toast(e.message,true)}});

const dialog=$('#server-dialog');let wizardStep=0;const wizardTitles=['Choose your game','The basics','Game settings','Resources & review'];function setWizardStep(step){wizardStep=Math.max(0,Math.min(3,step));$$('[data-wizard-step]').forEach(el=>el.classList.toggle('hidden',Number(el.dataset.wizardStep)!==wizardStep));$('#wizard-step-label').textContent='STEP '+(wizardStep+1)+' OF 4';$('#wizard-step-title').textContent=wizardTitles[wizardStep];$('#wizard-progress-fill').style.width=((wizardStep+1)*25)+'%';$('#wizard-back').classList.toggle('hidden',wizardStep===0);$('#wizard-next').classList.toggle('hidden',wizardStep===3);$('#wizard-create').classList.toggle('hidden',wizardStep!==3);if(wizardStep===3)renderWizardReview()}
const defaultServerNames={hytale:'My Hytale Server','minecraft-java':'My Minecraft Server',terraria:'My Terraria Server','runescape-dragonwilds':'My Dragonwilds Server'};
function syncGameSpecificFields(){
 const game=state.selectedGame,dragon=game==='runescape-dragonwilds',minecraft=game==='minecraft-java',terraria=game==='terraria';
 const rules=rulesForGame(game),entry=state.games.find(g=>g.id===game);
 $('#dragonwilds-fields')?.classList.toggle('hidden',!dragon);
 $('#minecraft-fields')?.classList.toggle('hidden',!minecraft);
 $('#terraria-fields')?.classList.toggle('hidden',!terraria);
 setModeOptions($('#new-server-mode'),rules.gameModes,rules.gameModes[0]);$('#new-mode-label').textContent=rules.modeLabel;$('#new-mode-help').classList.toggle('hidden',!rules.modeFixed);
 $$('.hytale-only').forEach(el=>el.classList.toggle('hidden',dragon));
 // Rows that only some games use are hidden and disabled so they are not validated.
 const nativeBackups=entry?entry.capabilities?.nativeBackups:game==='hytale';
 [['new-whitelist-row',rules.whitelist],['new-xms-row',rules.jvmMemory],['new-xmx-row',rules.jvmMemory],['new-backup-row',nativeBackups],['new-patchline-row',nativeBackups]].forEach(([id,on])=>{const row=$('#'+id),show=!!on&&!dragon;row.classList.toggle('hidden',!show);row.querySelectorAll('input').forEach(i=>{i.disabled=!show;if(i.type!=='checkbox')i.required=show})});
 ['new-dragonwilds-owner','new-dragonwilds-world','new-dragonwilds-admin','new-dragonwilds-password'].forEach(id=>{const input=$('#'+id);if(input){input.disabled=!dragon;input.required=dragon&&id!=='new-dragonwilds-password'}});
 const note=$('#wizard-config-note');
 if(note)note.textContent=dragon?'Dragonwilds requires an Owner ID, server name, default world name and admin password. World password is optional. Realmkeeper writes the INI values before launch.':minecraft?'Vanilla Minecraft Java will be downloaded from Mojang. You must accept the EULA. Use the TCP port shown in your server details for player connections.':terraria?'Vanilla Terraria is downloaded from terraria.org on the Install tab. The world is generated on the first start.':'When enabled, Hytale requires join permission for players. You can manage player permissions from the server console.';
 const name=$('#new-server-name');if(Object.values(defaultServerNames).includes(name.value)||name.value==='New Hytale Server')name.value=game==='hytale'&&state.servers.length?'New Hytale Server':defaultServerNames[game]||name.value;
 $('#new-server-players').value=rules.defaultMaxPlayers;$('#new-server-players').max=rules.playerLimit;
 $('#new-server-motd').value=dragon?'':rules.defaultMotd;
 const proto=(entry?.protocol||'').toUpperCase();
 $('#new-port-help').textContent=(rules.portMin&&proto?gameLabel(game)+' uses '+proto+' '+rules.portMin+'–'+rules.portMax+'. ':'')+'Leave blank to use the first available port.';
}
function renderGameChoices(){const el=$('#game-choice-list');if(!el||!state.games.length)return;el.innerHTML=state.games.map(g=>{const ready=!!g.available;const selected=state.selectedGame===g.id&&ready;const detail=ready?'Dedicated server · Ready to configure':'Planned · Not available yet';return '<button type="button" class="game-choice'+(selected?' selected':'')+'" data-game-choice="'+esc(g.id)+'" '+(ready?'':'disabled')+'><span class="game-icon">'+gameLogo(g.id)+'</span><span><strong>'+esc(g.name)+'</strong><small>'+esc(detail)+'</small></span><span class="choice-check">'+(selected?'✓':'')+'</span></button>'}).join('');$$('[data-game-choice]:not(:disabled)').forEach(b=>b.addEventListener('click',()=>{state.selectedGame=b.dataset.gameChoice;syncGameSpecificFields();populateCloneSources();renderGameChoices()}))}
async function loadGameChoices(){try{const data=await api('/api/games');state.games=data.games||[];const owner=data.defaults?.dragonwildsOwnerId||'';if(owner&&!$('#new-dragonwilds-owner').value){$('#new-dragonwilds-owner').value=owner;$('#new-dragonwilds-owner-help').textContent='Filled in from your last Dragonwilds server.'}state.games.forEach(g=>rememberGameArt(g.id,g.art));if(!state.games.some(g=>g.id===state.selectedGame&&g.available))state.selectedGame=state.games.find(g=>g.available)?.id||'hytale';renderGameChoices();syncGameSpecificFields()}catch(e){toast('Could not load game catalog: '+e.message,true)}}
function openServerDialog(){wizardStep=0;state.selectedGame='hytale';setWizardStep(0);loadGameChoices();$('#new-server-name').value=state.servers.length?'New Hytale Server':'My Hytale Server';$('#new-server-port').value='';$('#new-server-host').value='';$('#new-server-mode').value='Adventure';$('#new-server-players').value=20;$('#new-server-motd').value='Welcome to your Hytale server!';$('#new-server-password').value='';$('#new-server-whitelist').checked=false;$('#new-server-xms').value='2G';$('#new-server-xmx').value='4G';$('#new-server-backup').value=30;$('#new-server-autostart').checked=false;$('#new-minecraft-version').value='latest';$('#new-minecraft-variant').value='vanilla';$('#new-minecraft-variant-help').textContent=MINECRAFT_VARIANTS.vanilla.help;$('#new-minecraft-eula').checked=false;$('#new-terraria-world').value='';$('#new-terraria-size').value='medium';$('#new-terraria-version').value='latest';$('#new-terraria-variant').value='vanilla';syncTerrariaVariant();$('#new-dragonwilds-owner').value='';$('#new-dragonwilds-owner-help').textContent='';$('#new-dragonwilds-world').value='Dragonwilds World';$('#new-dragonwilds-admin').value='';$('#new-dragonwilds-password').value='';syncGameSpecificFields();const ports=state.settings||{};$('#new-port-help').textContent=ports.portMin?'Leave blank for the next available port in '+ports.portMin+'–'+ports.portMax+'.':'Leave blank for the next available published port.';populateCloneSources();dialog.showModal()}
function syncTerrariaVariant(){const v=TERRARIA_VARIANTS[$('#new-terraria-variant').value]||TERRARIA_VARIANTS.vanilla;$('#new-terraria-variant-help').textContent=v.help;$('#new-terraria-version').placeholder=v.placeholder}
$('#new-terraria-variant').addEventListener('change',syncTerrariaVariant);
$('#new-minecraft-variant').addEventListener('change',e=>{$('#new-minecraft-variant-help').textContent=(MINECRAFT_VARIANTS[e.target.value]||MINECRAFT_VARIANTS.vanilla).help});
function closeServerDialog(){dialog.close()}$('#add-server-side').addEventListener('click',openServerDialog);$('#add-server-main').addEventListener('click',openServerDialog);$('#first-create').addEventListener('click',openServerDialog);$('#cancel-server-dialog').addEventListener('click',closeServerDialog);$('#wizard-back').addEventListener('click',()=>setWizardStep(wizardStep-1));$('#wizard-next').addEventListener('click',()=>{if(wizardStep===1){const name=$('#new-server-name').value.trim();if(name.length<2||name.length>64){toast('Server name must be between 2 and 64 characters',true);return}const p=$('#new-server-port').value;if(p&&(+p<1||+p>65535)){toast('Enter a valid game port',true);return}}if(wizardStep===2){const players=+$('#new-server-players').value;const limit=rulesForGame(state.selectedGame).playerLimit;if(players<1||players>limit){toast(limit<1000?gameLabel(state.selectedGame)+' supports up to '+limit+' players':'Maximum players must be between 1 and 1000',true);return}if(state.selectedGame==='terraria'){const world=$('#new-terraria-world').value.trim(),version=$('#new-terraria-version').value.trim();if(world&&!/[a-z0-9]/i.test(world)){toast('The world name needs at least one letter or digit',true);return}const tv=TERRARIA_VARIANTS[$('#new-terraria-variant').value]||TERRARIA_VARIANTS.vanilla;if(version&&!tv.version.test(version)){toast('Enter '+tv.placeholder,true);return}}if(state.selectedGame==='minecraft-java'){if(!$('#new-minecraft-eula').checked){toast('Accept the Minecraft EULA before continuing',true);return}const version=$('#new-minecraft-version').value.trim();if(version.length>32||/[\\r\\n\\x00]/.test(version)){toast('Enter a valid Minecraft version or latest',true);return}}if(state.selectedGame==='runescape-dragonwilds'){if(!$('#new-dragonwilds-owner').value.trim()){toast('Enter your Dragonwilds Owner ID',true);return}if(!$('#new-dragonwilds-admin').value){toast('Set a Dragonwilds admin password',true);return}}}setWizardStep(wizardStep+1)});
function renderWizardReview(){const chosen=state.games.find(g=>g.id===state.selectedGame);const dragon=state.selectedGame==='runescape-dragonwilds';const items=[['Game',chosen?.name||'Hytale'],['Server name',$('#new-server-name').value.trim()],['Game port',$('#new-server-port').value||'Automatic']];if(state.selectedGame==='minecraft-java'){items.push(['Server type',(MINECRAFT_VARIANTS[$('#new-minecraft-variant').value]||MINECRAFT_VARIANTS.vanilla).name],['Version',$('#new-minecraft-version').value.trim()||'latest'],['Minecraft EULA',$('#new-minecraft-eula').checked?'Accepted':'Not accepted'],['Network','TCP'])}if(dragon){items.push(['Owner ID',$('#new-dragonwilds-owner').value.trim()],['Default world',$('#new-dragonwilds-world').value.trim()],['Admin password',$('#new-dragonwilds-admin').value?'Configured':'Missing'],['World password',$('#new-dragonwilds-password').value?'Set':'Not set'],['Capacity','Up to 6 players (game limit)'])}else{const rules=rulesForGame(state.selectedGame);if(state.selectedGame==='terraria')items.push(['Server type',(TERRARIA_VARIANTS[$('#new-terraria-variant').value]||TERRARIA_VARIANTS.vanilla).name],['World',($('#new-terraria-world').value.trim()||$('#new-server-name').value.trim())+' · '+$('#new-terraria-size').value],['Version',$('#new-terraria-version').value.trim()||'latest'],['Network','TCP']);items.push(['Players',$('#new-server-players').value],[rules.modeLabel,$('#new-server-mode').value]);if(rules.whitelist)items.push(['Whitelist',$('#new-server-whitelist').checked?'Enabled':'Disabled']);if(rules.jvmMemory)items.push(['Memory',$('#new-server-xms').value+' – '+$('#new-server-xmx').value]);items.push(['Access',$('#new-server-password').value?'Password protected':'No password']);if(state.selectedGame==='hytale')items.push(['Backups','Every '+$('#new-server-backup').value+' minutes'])}$('#wizard-review').innerHTML=items.map(([label,value])=>'<div class="review-item"><small>'+esc(label)+'</small><strong>'+esc(value)+'</strong></div>').join('')}
function populateCloneSources(){const sel=$('#new-server-source'),current=sel.value;sel.innerHTML='<option value="">Fresh install — set up game files next</option>'+state.servers.filter(s=>s.installed&&s.gameType===state.selectedGame).map(s=>'<option value="'+esc(s.id)+'">Clone '+esc(s.serverName)+' (new world)</option>').join('');if([...sel.options].some(o=>o.value===current))sel.value=current}
function validMemoryInput(v){return /^\d+(?:K|M|G)$/i.test(v.trim())&&Number(v.trim().slice(0,-1))>0}
$('#server-create-form').addEventListener('submit',async e=>{e.preventDefault();const body={game:state.selectedGame||'hytale',minecraftVariant:state.selectedGame==='minecraft-java'?$('#new-minecraft-variant').value:'',minecraftVersion:state.selectedGame==='minecraft-java'?($('#new-minecraft-version').value.trim()||'latest'):'',minecraftEulaAccepted:state.selectedGame==='minecraft-java'&&$('#new-minecraft-eula').checked,name:$('#new-server-name').value.trim(),port:+$('#new-server-port').value||0,publicHost:$('#new-server-host').value.trim(),gameMode:$('#new-server-mode').value,maxPlayers:+$('#new-server-players').value,motd:$('#new-server-motd').value.trim(),password:$('#new-server-password').value,whitelistEnabled:$('#new-server-whitelist').checked,memoryMin:$('#new-server-xms').value.trim(),memoryMax:$('#new-server-xmx').value.trim(),backupFrequency:+$('#new-server-backup').value,hytalePatchline:$('#new-hytale-patchline').value,autoStart:$('#new-server-autostart').checked,sourceServerId:$('#new-server-source').value,dragonwildsOwnerId:$('#new-dragonwilds-owner').value.trim(),dragonwildsWorldName:$('#new-dragonwilds-world').value.trim(),dragonwildsAdminPassword:$('#new-dragonwilds-admin').value,dragonwildsWorldPassword:$('#new-dragonwilds-password').value};if(state.selectedGame==='terraria'){body.terrariaVariant=$('#new-terraria-variant').value;body.terrariaWorldName=$('#new-terraria-world').value.trim();body.terrariaWorldSize=$('#new-terraria-size').value;body.terrariaVersion=$('#new-terraria-version').value.trim()||'latest'}if(rulesForGame(state.selectedGame).jvmMemory&&(!validMemoryInput(body.memoryMin)||!validMemoryInput(body.memoryMax))){toast('Memory values must look like 2G, 4096M or 1024K',true);setWizardStep(3);return}try{const s=await api('/api/servers',{method:'POST',serverId:null,body:JSON.stringify(body)});closeServerDialog();toast('Server created');await loadServers(false);await selectServer(s.id);if(!body.sourceServerId&&s.id===state.serverId)switchView('setup')}catch(err){toast(err.message,true)}});
// Minecraft game rules: a form built from the fields the API describes.
async function loadMinecraftProperties(){const ctx=selectionContext();if(!ctx.id||!serverCapabilities().serverProperties)return;const box=$('#mc-properties-fields');box.innerHTML='<div class="empty-state span-two">Loading game rules…</div>';try{const d=await scopedApi(ctx,'/api/minecraft/properties');if(!ctx.current())return;renderMinecraftProperties(d)}catch(e){if(ctx.current())box.innerHTML='<div class="empty-state span-two">'+esc(e.message)+'</div>'}}
function renderMinecraftProperties(d){
  $('#mc-properties-fields').innerHTML=d.fields.map(f=>{
    const v=d.values[f.key]??f.default,id='mcp-'+esc(f.key),key=esc(f.key),help=f.help?'<small>'+esc(f.help)+'</small>':'';
    if(f.kind==='bool')return `<label class="toggle-row"><span><b>${esc(f.label)}</b>${help}</span><input type="checkbox" id="${id}" data-mc-prop="${key}" data-kind="bool" ${v==='true'?'checked':''}></label>`;
    if(f.kind==='choice')return `<label>${esc(f.label)}<select id="${id}" data-mc-prop="${key}">${f.choices.map(c=>`<option value="${esc(c)}" ${c===v?'selected':''}>${esc(c[0].toUpperCase()+c.slice(1))}</option>`).join('')}</select>${help}</label>`;
    if(f.kind==='int')return `<label>${esc(f.label)}<input id="${id}" type="number" min="${f.min??0}" max="${f.max??0}" value="${esc(v)}" data-mc-prop="${key}">${help}</label>`;
    return `<label>${esc(f.label)}<input id="${id}" value="${esc(v)}" maxlength="100" autocomplete="off" data-mc-prop="${key}">${help}</label>`;
  }).join('');
  $('#mc-properties-note').textContent=d.running?'Saved to server.properties. The server is running, so changes apply after a restart.':'Saved to server.properties. Changes apply the next time the server starts.';
}
$('#mc-properties-save').addEventListener('click',async()=>{const id=state.serverId;if(!id)return;const values={};$$('[data-mc-prop]').forEach(el=>{values[el.dataset.mcProp]=el.dataset.kind==='bool'?String(el.checked):el.value});const btn=$('#mc-properties-save');btn.disabled=true;try{const d=await api('/api/minecraft/properties',{method:'PUT',serverId:id,body:JSON.stringify({values})});toast(d.running?'Game rules saved. Restart the server to apply them.':'Game rules saved');if(id===state.serverId)renderMinecraftProperties(d)}catch(e){toast(e.message,true)}finally{btn.disabled=false}});
// Schedules: the whole list is edited client-side and saved with one PUT.
const SCHEDULE_ACTIONS={restart:'Restart server',start:'Start server',stop:'Stop server',backup:'Back up',command:'Run console command'};
const DAY_NAMES=['Sun','Mon','Tue','Wed','Thu','Fri','Sat'];
let editingSchedule=null;
async function loadSchedules(){const ctx=selectionContext();if(!ctx.id)return;try{const d=await scopedApi(ctx,'/api/schedules');if(!ctx.current())return;state.schedules=d;renderSchedules()}catch(e){if(ctx.current())$('#schedule-list').innerHTML='<div class="empty-state">'+esc(e.message)+'</div>'}}
function describeSchedule(x){
  if(x.kind==='interval'){const m=x.everyMinutes;return 'Every '+(m%60===0?(m/60===1?'hour':m/60+' hours'):m+' minutes')}
  const days=(x.days&&x.days.length)?x.days.map(d=>DAY_NAMES[d]).join(', '):'Every day';
  return days+' at '+x.time;
}
function renderSchedules(){
  const d=state.schedules;if(!d)return;
  $('#schedules-note').textContent='Automatic restarts, backups and commands. Times use the server time zone: '+d.timezone+'.';
  const list=$('#schedule-list');
  if(!d.schedules.length){list.innerHTML='<div class="empty-state">No schedules yet. Add one to restart, back up or run commands automatically.</div>';return}
  list.innerHTML=d.schedules.map(x=>{
    const failed=x.lastResult&&x.lastResult!=='Done'&&!x.lastResult.startsWith('Skipped');
    const action=SCHEDULE_ACTIONS[x.action]||x.action;
    const extra=x.action==='command'?' · <code>'+esc(x.command)+'</code>':(x.warnMinutes?' · warns '+x.warnMinutes+' min before':'');
    const next=!x.enabled?'Disabled':x.running?'Running now…':(x.nextRun?'Next: '+esc(fmtDate(x.nextRun)):'Not scheduled');
    const last=x.lastRun?'<div class="result'+(failed?' failed':'')+'">Last: '+esc(fmtDate(x.lastRun))+' · '+esc(x.lastResult)+'</div>':'';
    return '<div class="schedule-row'+(x.enabled?'':' disabled')+'" data-schedule="'+esc(x.id)+'"><span class="schedule-icon" aria-hidden="true"><svg class="icon"><use href="#i-clock"/></svg></span><div><strong>'+esc(x.name)+'</strong><div class="when">'+esc(action)+' · '+esc(describeSchedule(x))+extra+'</div></div><div class="next"><div class="when">'+next+'</div>'+last+'</div><div class="actions"><label class="check" title="Enabled"><input type="checkbox" data-schedule-toggle '+(x.enabled?'checked':'')+' aria-label="Enabled"></label><button type="button" class="btn small ghost" data-schedule-edit>Edit</button><button type="button" class="btn small danger-ghost" data-schedule-delete>Delete</button></div></div>';
  }).join('');
}
function scheduleListForSave(){return(state.schedules?.schedules||[]).map(({nextRun,lastRun,lastResult,running,...rest})=>rest)}
async function saveSchedules(list,message){const id=state.serverId;if(!id)return false;try{const d=await api('/api/schedules',{method:'PUT',serverId:id,body:JSON.stringify({schedules:list})});if(id===state.serverId){state.schedules=d;renderSchedules()}toast(message);return true}catch(e){toast(e.message,true);return false}}
function syncScheduleForm(){
  const action=$('#sch-action').value,kind=$('#sch-kind').value,warn=!!state.schedules?.warnings&&(action==='restart'||action==='stop');
  $('#sch-command-row').classList.toggle('hidden',action!=='command');
  $('#sch-time-row').classList.toggle('hidden',kind!=='daily');
  $('#sch-days-row').classList.toggle('hidden',kind!=='daily');
  $('#sch-every-row').classList.toggle('hidden',kind!=='interval');
  $('#sch-warn-row').classList.toggle('hidden',!warn);
}
function openScheduleEditor(x){
  const d=state.schedules;if(!d)return;
  editingSchedule=x?.id||null;
  $('#schedule-editor-title').textContent=x?'Edit schedule':'New schedule';
  $('#sch-action').innerHTML=d.actions.map(a=>'<option value="'+esc(a)+'">'+esc(SCHEDULE_ACTIONS[a]||a)+'</option>').join('');
  $('#sch-days-row').innerHTML='<legend>On these days <small class="muted">(none selected = every day)</small></legend>'+DAY_NAMES.map((n,i)=>'<label><input type="checkbox" value="'+i+'" '+((x?.days||[]).includes(i)?'checked':'')+'> '+n+'</label>').join('');
  $('#sch-name').value=x?.name||'';
  $('#sch-action').value=x?.action||'restart';
  $('#sch-command').value=x?.command||'';
  $('#sch-kind').value=x?.kind||'daily';
  $('#sch-time').value=x?.time||'04:00';
  const every=x?.everyMinutes||360;
  if(every%60===0){$('#sch-every').value=every/60;$('#sch-every-unit').value='60'}else{$('#sch-every').value=every;$('#sch-every-unit').value='1'}
  $('#sch-warn').value=x?.warnMinutes??5;
  $('#sch-enabled').checked=x?x.enabled:true;
  syncScheduleForm();
  $('#schedule-editor').classList.remove('hidden');
  $('#schedule-editor').scrollIntoView({behavior:'smooth',block:'nearest'});
  $('#sch-name').focus();
}
$('#schedule-add').addEventListener('click',()=>openScheduleEditor(null));
$('#sch-cancel').addEventListener('click',()=>{editingSchedule=null;$('#schedule-editor').classList.add('hidden')});
$('#sch-action').addEventListener('change',syncScheduleForm);
$('#sch-kind').addEventListener('change',syncScheduleForm);
$('#schedule-form').addEventListener('submit',async e=>{
  e.preventDefault();
  const kind=$('#sch-kind').value,action=$('#sch-action').value;
  const item={id:editingSchedule||'',name:$('#sch-name').value.trim(),enabled:$('#sch-enabled').checked,action,kind,
    command:action==='command'?$('#sch-command').value.trim():'',
    time:kind==='daily'?$('#sch-time').value:'',
    days:kind==='daily'?$$('#sch-days-row input:checked').map(i=>+i.value):[],
    everyMinutes:kind==='interval'?Math.round(+$('#sch-every').value*+$('#sch-every-unit').value):0,
    warnMinutes:$('#sch-warn-row').classList.contains('hidden')?0:+$('#sch-warn').value};
  const list=scheduleListForSave();
  const i=list.findIndex(x=>x.id===editingSchedule);
  if(i>=0)list[i]=item;else list.push(item);
  if(await saveSchedules(list,editingSchedule?'Schedule updated':'Schedule added')){editingSchedule=null;$('#schedule-editor').classList.add('hidden')}
});
$('#schedule-list').addEventListener('click',async e=>{
  const row=e.target.closest('[data-schedule]');if(!row)return;
  const id=row.dataset.schedule,current=(state.schedules?.schedules||[]).find(x=>x.id===id);if(!current)return;
  if(e.target.closest('[data-schedule-edit]'))openScheduleEditor(current);
  if(e.target.closest('[data-schedule-delete]')){if(!confirm('Delete the schedule "'+current.name+'"?'))return;await saveSchedules(scheduleListForSave().filter(x=>x.id!==id),'Schedule deleted')}
});
$('#schedule-list').addEventListener('change',async e=>{
  const toggle=e.target.closest('[data-schedule-toggle]');if(!toggle)return;
  const id=toggle.closest('[data-schedule]').dataset.schedule;
  const list=scheduleListForSave().map(x=>x.id===id?{...x,enabled:toggle.checked}:x);
  if(!await saveSchedules(list,toggle.checked?'Schedule enabled':'Schedule disabled'))toggle.checked=!toggle.checked;
});
boot().catch(e=>{document.body.innerHTML=`<pre style="padding:30px;color:#e29a96">${esc(e.stack||e.message)}</pre>`});
