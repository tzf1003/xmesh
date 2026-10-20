import "./control-ui.css";
import { render } from "preact";
import { App } from "./app";

// The window frame differs by platform (see main/chrome.ts), so the page sizes its top bar to match.
const ua = navigator.userAgent;
document.documentElement.classList.add(/Mac OS X/.test(ua) ? "mac" : /Windows/.test(ua) ? "win" : "linux");

const root = document.getElementById("root");
if (!root) throw new Error("missing #root");
render(<App />, root);
