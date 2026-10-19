// Isolated target for native input acceptance; serves metadata and observed DOM state only.
const { app, BrowserWindow, screen, Menu } = require("electron");
const http = require("node:http");
const token = process.env.XMESH_DESKTOP_FIXTURE_TOKEN;
if (!token) throw new Error("fixture token missing");
app.whenReady().then(async () => {
  Menu.setApplicationMenu(Menu.buildFromTemplate([{ role: "editMenu" }]));
  const display = screen.getPrimaryDisplay();
  const window = new BrowserWindow({ x: display.workArea.x + 40, y: display.workArea.y + 40, width: 640, height: 520, alwaysOnTop: true, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
  await window.loadURL("data:text/html;charset=utf-8," + encodeURIComponent(`<!doctype html><title>XmeSh native desktop acceptance</title>
<style>body{margin:20px;font:16px sans-serif;background:#f2f6f8}textarea{display:block;width:520px;height:80px;margin:15px 0}#drag{width:520px;height:110px;background:#4ac8a0}#scroll{width:520px;height:100px;overflow:auto;margin-top:15px;background:white}</style>
<h1>XmeSh desktop input test</h1><button id="click">Click target</button><textarea id="text" aria-label="Text target"></textarea><div id="drag">Drag target</div><div id="scroll"><div style="height:600px">Scroll target</div></div>
<script>window.observed={clicks:0,dragComplete:false,keys:[],wheels:[]};window.addEventListener('keydown',e=>observed.keys.push([e.key,e.code,e.metaKey,e.ctrlKey]));window.addEventListener('wheel',e=>observed.wheels.push([e.deltaX,e.deltaY]));document.querySelector('#click').onclick=()=>observed.clicks++;let start;document.querySelector('#drag').onpointerdown=e=>{start=e.clientX};document.querySelector('#drag').onpointerup=e=>{observed.dragComplete=!!start&&e.clientX-start>80};</script>`));
  window.show(); window.focus();
  const server = http.createServer(async (request, response) => {
    if (request.headers.authorization !== `Bearer ${token}` || request.method !== "GET") { response.writeHead(403); response.end(); return; }
    try {
      const data = await window.webContents.executeJavaScript(`(() => {const point=(id,dx=0)=>{const r=document.querySelector(id).getBoundingClientRect();return {x:r.x+r.width/2+dx,y:r.y+r.height/2}};return {click:point('#click'),text:point('#text'),dragPoint:point('#drag',-100),dragTo:point('#drag',100),scroll:point('#scroll'),value:document.querySelector('#text').value,scrollTop:document.querySelector('#scroll').scrollTop,...observed}})()`);
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify({ ...data, content: window.getContentBounds(), display: screen.getDisplayMatching(window.getBounds()).bounds, scale: screen.getDisplayMatching(window.getBounds()).scaleFactor }));
    } catch { response.writeHead(500); response.end(); }
  });
  server.listen(0, "127.0.0.1", () => console.log(JSON.stringify({ endpoint: `http://127.0.0.1:${server.address().port}` })));
  app.on("before-quit", () => server.close());
});
app.on("window-all-closed", () => app.quit());
