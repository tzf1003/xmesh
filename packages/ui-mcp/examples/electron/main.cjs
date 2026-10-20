const { app, BrowserWindow } = require("electron");
globalThis.__xmeshUiBackgroundTest = true;
if (process.platform === "darwin") app.setActivationPolicy("prohibited");
app.whenReady().then(async () => {
  const window = new BrowserWindow({
    show: false,
    focusable: false,
    width: 800,
    height: 600,
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      backgroundThrottling: false,
      // This dedicated hidden test app needs a rendering surface without a
      // mapped X11 window so actionability checks receive animation frames.
      offscreen: process.platform === "linux",
    },
  });
  await window.loadFile(require("node:path").join(__dirname, "page.html"));
});
app.on("window-all-closed", () => app.quit());
