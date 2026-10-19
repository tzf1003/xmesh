"use strict";
const data=JSON.parse(document.getElementById("release-data").textContent);
const os=document.getElementById("os"), arch=document.getElementById("arch"), link=document.getElementById("download-link"), offline=document.getElementById("offline-link");
function update(){
  const asset=data.assets.find(a=>a.os===os.value&&a.arch===arch.value&&a.kind==="desktop");
  for(const a of [link,offline]){a.setAttribute("aria-disabled",String(!asset));a.removeAttribute("href");}
  if(!asset){link.textContent="选择系统与架构";return;}
  link.href=asset.url;link.textContent="下载 "+(os.value==="darwin"?"macOS":"Windows")+" · "+arch.value;
  offline.href=data.release+"/xmesh-offline-"+data.version+"-"+os.value+"-"+arch.value+".zip";
  const t=asset.trust||{};
  document.getElementById("trust").textContent=t.signed===true ? (os.value==="darwin" ? (t.notarized===true&&t.stapled===true ? "此包的发布记录：已签名、公证并装订；安装时再次验证系统信任。" : "此测试包已签名；公证或装订尚未验证，请确认 macOS 系统信任要求。") : "此包的发布记录：已签名；安装时再次验证 Authenticode。") : "此测试包的发布记录：未签名。请阅读发布说明并确认系统信任要求。";
}
os.addEventListener("change",update);arch.addEventListener("change",update);
const ua=navigator.userAgent;
if(/Windows/.test(ua))os.value="windows";else if(/Macintosh|Mac OS X/.test(ua))os.value="darwin";
// User-agent strings often omit the real architecture; keep explicit manual selection.
if(navigator.userAgentData&&navigator.userAgentData.getHighEntropyValues){navigator.userAgentData.getHighEntropyValues(["architecture","bitness"]).then(h=>{if(h.architecture==="arm"&&h.bitness==="64")arch.value="arm64";else if(h.architecture==="x86"&&h.bitness==="64")arch.value="amd64";document.getElementById("detection").textContent=arch.value?"已检测到架构，请确认后下载。":"浏览器未提供可靠架构，请手动选择。";update();}).catch(()=>{});}
update();
document.getElementById("copy").addEventListener("click",async()=>{const field=document.getElementById("install-prompt");try{await navigator.clipboard.writeText(field.value);document.getElementById("copy-status").textContent="已复制";}catch(_){field.focus();field.select();document.getElementById("copy-status").textContent="请选择并复制提示词";}});
