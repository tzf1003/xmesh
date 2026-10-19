// Background-only Electron target. No call activates it or positions the system
// pointer; the acceptance driver observes its own DOM state via a private file.
const { app, BrowserWindow, Menu } = require("electron");
const fs = require("node:fs");
const path = require("node:path");
const directory = process.env.XMESH_BACKGROUND_FIXTURE_DIR;
if (!directory || process.env.XMESH_TEST_ALLOW_BACKGROUND_INPUT !== "1") throw new Error("background acceptance authorization missing");
app.setActivationPolicy("accessory");
app.whenReady().then(async () => {
  Menu.setApplicationMenu(Menu.buildFromTemplate([{ role: "editMenu" }]));
  const window = new BrowserWindow({ show: false, focusable: true, x: 80, y: 80, width: 640, height: 560, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  await window.loadURL("data:text/html;charset=utf-8," + encodeURIComponent(`<!doctype html><title>Xmesh background Electron acceptance</title>
<style>body{margin:20px;font:16px sans-serif;background:#f2f6f8}textarea{display:block;width:520px;height:80px;margin:15px 0}#drag{width:520px;height:110px;background:#4ac8a0}#scroll{width:520px;height:100px;overflow:auto;margin-top:15px;background:white}</style>
<h1>Background acceptance</h1><button id="click">Click target</button><textarea id="text" aria-label="Text target"></textarea><div id="drag">Drag target</div><div id="scroll"><div style="height:600px">Scroll target</div></div>
<script>window.observed={clicks:0,drag_complete:false,events:[]};for(const type of ['pointerdown','pointermove','pointerup','wheel'])document.addEventListener(type,e=>{observed.events.push([type,e.clientX,e.clientY,e.buttons]);if(observed.events.length>100)observed.events.shift()});document.querySelector('#click').onclick=()=>observed.clicks++;let start;document.querySelector('#drag').onpointerdown=e=>{start=e.clientX};document.querySelector('#drag').onpointerup=e=>{observed.drag_complete=!!start&&e.clientX-start>80};</script>`));
  window.showInactive();
  let updating = false;
  const timer = setInterval(async () => {
    if (updating || window.isDestroyed()) return;
    updating = true;
    try {
      const points = await window.webContents.executeJavaScript(`(()=>{const point=(id,dx=0)=>{const r=document.querySelector(id).getBoundingClientRect();return{x:r.x+r.width/2+dx,y:r.y+r.height/2}};return{button:point('#click'),text:point('#text'),drag:point('#drag',-100),drag_to:point('#drag',100),scroll:point('#scroll'),value:document.querySelector('#text').value,scroll_y:document.querySelector('#scroll').scrollTop,...observed}})()`);
      const bounds = window.getBounds(), content = window.getContentBounds();
      for (const name of ["button", "text", "drag", "drag_to", "scroll"]) { points[name].x += content.x - bounds.x; points[name].y += content.y - bounds.y; }
      const state = { ...points, pid: process.pid, window_bounds: bounds, focused: window.isFocused() };
      fs.writeFileSync(path.join(directory, "state.pending"), JSON.stringify(state), { mode: 0o600 });
      fs.renameSync(path.join(directory, "state.pending"), path.join(directory, "state.json"));
    } finally { updating = false; }
  }, 30);
  app.on("before-quit", () => clearInterval(timer));
});
app.on("window-all-closed", () => app.quit());
