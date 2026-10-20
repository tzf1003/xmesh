
(()=>{
 const menu=[
  {id:'overview',label:'网络拓扑',icon:'topology',group:'network'},
  {id:'device-management',label:'设备',icon:'device',group:'network',page:true},
  {id:'sessions',label:'会话',icon:'activity',group:'network',page:true},
  {id:'authz',label:'访问与授权',icon:'shield',group:'access'},
  {id:'ingress',label:'公网入口',icon:'globe',group:'access'},
  {id:'runtime',label:'接入控制',icon:'sliders',group:'runtime'},
  {id:'terminal',label:'远程终端',icon:'terminal',group:'runtime'},
  {id:'transfers',label:'文件传输',icon:'folder',group:'runtime'},
  {id:'playground',label:'工具调试台',icon:'code',group:'runtime'},
  {id:'computer',label:'桌面控制',icon:'device',group:'runtime'},
  {id:'ci',label:'CI 运行器',icon:'runner',group:'modules',page:true},
  {id:'mesh',label:'Tailscale 管理',icon:'network',group:'modules',page:true},
  {id:'settings',label:'设置',icon:'sliders',group:'settings',page:true}
 ];
 const current=document.body.dataset.previewPage||'device-management';
 const pageURL=(id,hash='')=>new URL(`../${id}/index.html${hash}`,location.href).href;
 const item=m=>m.page?`<a class="nav-item" data-nav-id="${m.id}" href="${pageURL(m.id)}" ${m.id===current?'aria-current="page"':''} title="${m.label}">${icon(m.icon)}<span>${m.label}</span></a>`:`<button class="nav-item" data-nav-id="${m.id}" data-nav-placeholder="${m.id}" title="${m.label}">${icon(m.icon)}<span>${m.label}</span></button>`;
 const group=(key,label)=>`<div class="nav-group"><p class="nav-group-label">${label}</p>${menu.filter(m=>m.group===key).map(item).join('')}</div>`;
 const disclosure=(key,label,glyph)=>`<details class="nav-disclosure" id="preview-nav-${key}" ${key==='runtime'?'open':''}><summary aria-controls="preview-nav-${key}-children" aria-expanded="${key==='runtime'}" title="${label}">${icon(glyph)}<span>${label}</span>${icon('chevron')}</summary><div class="nav-submenu" id="preview-nav-${key}-children">${menu.filter(m=>m.group===key).map(item).join('')}</div></details>`;
 const sidebar=document.querySelector('.sidebar');sidebar.classList.add('preview-navigation');sidebar.innerHTML=group('network','网络')+group('access','安全与接入')+`<div class="nav-modules">${menu.filter(m=>m.group==='modules').map(item).join('')}</div>`+disclosure('runtime','运行','runner')+`<div class="nav-footer">${menu.filter(m=>m.group==='settings').map(item).join('')}<span class="sidebar-foot">xmesh <span>v0.1.0-beta.6</span></span></div>`;
 sidebar.querySelectorAll('details').forEach(d=>{d.addEventListener('toggle',()=>d.querySelector('summary').setAttribute('aria-expanded',String(d.open)))});
 const searchButton=document.createElement('button');searchButton.className='nav-search-trigger';searchButton.type='button';searchButton.setAttribute('aria-label','搜索功能');searchButton.innerHTML=icon('search')+'<span>搜索功能</span><kbd>⌘ K</kbd>';document.querySelector('.top-spacer').after(searchButton);
 const searchDialog=document.createElement('dialog');searchDialog.className='nav-search-dialog';searchDialog.setAttribute('aria-label','搜索功能');searchDialog.innerHTML=`<div class="nav-search-input">${icon('search')}<input type="search" aria-label="搜索页面或功能" placeholder="搜索页面或功能…"><button type="button" class="icon-button" data-close-nav-search aria-label="关闭搜索">${icon('close')}</button></div><div class="nav-search-results"></div><div class="nav-search-foot">↑ ↓ 选择 <span>Enter 打开 · Esc 关闭</span></div>`;document.body.append(searchDialog);
 const placeholder=document.createElement('dialog');placeholder.className='nav-destination-preview';placeholder.setAttribute('aria-labelledby','nav-destination-title');placeholder.innerHTML=`<div class="drawer-head"><div><p class="eyebrow">导航预览</p><h2 id="nav-destination-title"></h2></div><button class="icon-button" data-close-nav-destination aria-label="关闭导航预览">${icon('close')}</button></div><p class="drawer-description" id="nav-destination-copy"></p><button class="button full" data-close-nav-destination>返回${menu.find(m=>m.id===current)?.label||'预览'}</button>`;document.body.append(placeholder);
 const descriptions={runtime:'在这里管理设备的接入方式与运行配置。',terminal:'在设备上启动、恢复和操作终端会话。',transfers:'在已连接的设备之间上传和下载文件。',playground:'选择设备，查看工具并执行调试调用。',computer:'建立桌面会话，查看并操作远程设备。',ci:'管理 CI 运行器与执行状态。',config:'查看与编辑本机配置。',mesh:'管理 Tailscale 控制面的设备、策略与中继。',about:'查看应用信息和更新。',settings:'管理应用偏好设置。',overview:'查看设备网络及连接关系。',authz:'管理访问权限与授权策略。',ingress:'配置公网入口。'};
 function restoreNav(){sidebar.querySelectorAll('[aria-current]').forEach(e=>e.removeAttribute('aria-current'));sidebar.querySelector(`[data-nav-id="${current}"]`)?.setAttribute('aria-current','page');sidebar.querySelectorAll('[data-active]').forEach(e=>e.removeAttribute('data-active'))}
 function destination(id){const m=menu.find(x=>x.id===id);if(!m)return;if(m.page){location.href=pageURL(id);return}const d=sidebar.querySelector(`#preview-nav-${m.group}`);if(d){d.open=true;d.dataset.active='true'}sidebar.querySelectorAll('[aria-current]').forEach(e=>e.removeAttribute('aria-current'));sidebar.querySelector(`[data-nav-id="${id}"]`).setAttribute('aria-current','page');placeholder.querySelector('h2').textContent=m.label;placeholder.querySelector('.drawer-description').textContent=descriptions[id];if(!placeholder.open)placeholder.showModal()}
 sidebar.addEventListener('click',e=>{const target=e.target.closest('[data-nav-placeholder]');if(target)destination(target.dataset.navPlaceholder)});
 placeholder.addEventListener('close',restoreNav);placeholder.addEventListener('click',e=>{if(e.target.closest('[data-close-nav-destination]'))placeholder.close()});
 let results=[],searchIndex=0;const searchInput=searchDialog.querySelector('input');
 const searches=[...menu,...['agent','gateway','ingress'].map(key=>({id:'role-'+key,label:key[0].toUpperCase()+key.slice(1)+' 设置',icon:'sliders',group:'roles'})),{id:'settings-about',label:'关于与更新',icon:'info',group:'settings'},{id:'audit-history',label:'历史记录与审计',icon:'activity',group:'network',keywords:'审计 执行 历史'}];
 function renderSearch(){const q=searchInput.value.trim().toLowerCase();results=searches.filter(m=>(m.label+' '+m.id+' '+(m.keywords||'')).toLowerCase().includes(q));searchIndex=Math.min(searchIndex,Math.max(0,results.length-1));searchDialog.querySelector('.nav-search-results').innerHTML=results.length?results.map((m,i)=>`<button class="nav-search-result ${i===searchIndex?'highlighted':''}" data-nav-result="${i}">${icon(m.icon)}<span>${m.label}</span><small>${m.group==='runtime'?'运行':m.group==='roles'?'角色设置':m.group==='settings'?'设置':''}</small>${icon('chevron')}</button>`).join(''):'<p class="nav-no-results">没有匹配的功能</p>'}
 function choose(i){const m=results[i];if(!m)return;searchDialog.close();if(m.id.startsWith('role-')){window.xmeshRolePreview?.open(m.id.slice(5));return}if(m.id==='settings-about'){location.href=pageURL('settings','#about');return}if(m.id==='audit-history'){if(current==='sessions'){if(location.hash==='#history')window.dispatchEvent(new HashChangeEvent('hashchange'));else location.hash='history'}else location.href=pageURL('sessions','#history');return}destination(m.id)}
 function openSearch(){searchInput.value='';searchIndex=0;renderSearch();if(!searchDialog.open)searchDialog.showModal();searchInput.focus()}
 searchButton.addEventListener('click',openSearch);searchInput.addEventListener('input',()=>{searchIndex=0;renderSearch()});searchDialog.addEventListener('click',e=>{if(e.target.closest('[data-close-nav-search]'))searchDialog.close();const result=e.target.closest('[data-nav-result]');if(result)choose(Number(result.dataset.navResult))});searchInput.addEventListener('keydown',e=>{if(['ArrowDown','ArrowUp'].includes(e.key)){e.preventDefault();searchIndex=results.length?(searchIndex+(e.key==='ArrowDown'?1:results.length-1))%results.length:0;renderSearch()}if(e.key==='Enter'){e.preventDefault();choose(searchIndex)}});
 document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();openSearch()}});
})();
