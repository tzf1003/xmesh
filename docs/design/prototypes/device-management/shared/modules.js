
const $=id=>document.getElementById(id);
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const moduleDrawer=$('module-drawer');let moduleToast;
function notify(text){$('toast').textContent=text;$('toast').classList.add('visible');clearTimeout(moduleToast);moduleToast=setTimeout(()=>$('toast').classList.remove('visible'),2800)}
function openPanel(title,html,kicker){$('module-drawer-title').textContent=title;$('module-drawer-content').innerHTML=html;if(kicker)$('module-drawer-kicker').textContent=kicker;hydrate(moduleDrawer);if(!moduleDrawer.open)moduleDrawer.showModal()}
function closePanel(){moduleDrawer.close();$('module-drawer-content').replaceChildren()}
function field(label,id,value='',opts={}){return `<label class="field"><span>${label}</span><input id="${id}" type="${opts.type||'text'}" value="${esc(value)}" ${opts.placeholder?`placeholder="${esc(opts.placeholder)}"`:''} ${opts.min?`min="${opts.min}"`:''} ${opts.max?`max="${opts.max}"`:''} ${opts.required?'required':''} ${opts.disabled?'disabled':''} ${opts.type==='password'?'autocomplete="off"':''}>${opts.hint?`<small>${opts.hint}</small>`:''}</label>`}
function selectField(label,id,choices,value){return `<label class="field"><span>${label}</span><select id="${id}">${choices.map(([v,t])=>`<option value="${v}" ${value===v?'selected':''}>${t}</option>`).join('')}</select></label>`}
const state=(label,tone='ok')=>`<span class="state ${tone}"><i></i>${label}</span>`;
const chip=(text,tone='')=>`<span class="tag ${tone}">${esc(text)}</span>`;
const facts=entries=>`<dl class="facts">${entries.map(([k,v])=>`<div><dt>${k}</dt><dd>${v}</dd></div>`).join('')}</dl>`;
const actions=(label,action,extra='')=>`<div class="form-actions"><button type="button" class="button quiet" data-base-action="close">取消</button><button type="button" class="button primary" data-action="${action}" ${extra}>${label}</button></div>`;
const empty=(title,copy,action,label)=>`<div class="module-empty"><span class="empty-symbol">${icon('plus')}</span><h3>${title}</h3><p>${copy}</p>${action?`<button class="button primary" data-action="${action}">${icon('plus')}${label}</button>`:''}</div>`;
function sectionTabs(tabs,current){return `<div class="module-tabs" role="tablist" aria-label="模块内容">${tabs.map(([id,name,n])=>`<button type="button" role="tab" id="tab-${id}" data-tab="${id}" aria-selected="${id===current}" tabindex="${id===current?0:-1}" aria-controls="module-panel">${name}${n!==undefined?`<span>${n}</span>`:''}</button>`).join('')}</div>`}
function renderModule(html){$('module-root').innerHTML=html;hydrate($('module-root'))}
function showConfirm(title,body,label,action,extra=''){openPanel(title,`<p class="drawer-description">${body}</p>${actions(label,action,extra)}`)}
document.addEventListener('click',async e=>{const theme=e.target.closest('button[data-theme]');if(theme){document.documentElement.dataset.theme=theme.dataset.theme;document.querySelectorAll('button[data-theme]').forEach(b=>{b.classList.toggle('selected',b===theme);b.setAttribute('aria-pressed',String(b===theme))})}if(e.target.closest('[data-base-action="close"]'))closePanel();const copy=e.target.closest('[data-copy]');if(copy){try{await navigator.clipboard.writeText(copy.dataset.copy);notify('已复制')}catch{notify('可选择文本后复制')}}});
moduleDrawer.addEventListener('close',()=>{$('module-drawer-content').replaceChildren()});
document.addEventListener('keydown',e=>{const tabs=e.target.closest('.module-tabs');if(!tabs)return;const all=[...tabs.querySelectorAll('[data-tab]')];let index=all.indexOf(e.target);if(e.key==='ArrowRight')index=(index+1)%all.length;else if(e.key==='ArrowLeft')index=(index+all.length-1)%all.length;else if(e.key==='Home')index=0;else if(e.key==='End')index=all.length-1;else return;e.preventDefault();const id=all[index].id;all[index].click();$(id)?.focus()});
hydrate();
