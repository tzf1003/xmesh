"use strict";

const api = window.xmesh;
const $ = (id) => document.getElementById(id);

function render(snap) {
  const c = snap.connection;
  $("state").textContent = c.kind;
  $("node").textContent = c.kind === "connected" ? c.status.hostname : "";
  $("peers").textContent = c.kind === "connected" ? String(c.status.peers.online) : "";
  $("drain").disabled = c.kind !== "connected" || c.status.drain.draining;
  $("resume").disabled = c.kind !== "connected" || !c.status.drain.draining;
}

async function run(call) {
  const r = await call();
  $("error").textContent = r.ok ? "" : r.error.message;
}

$("drain").addEventListener("click", () => run(() => api.drain()));
$("resume").addEventListener("click", () => run(() => api.resume()));

api.getSnapshot().then(render);
api.onSnapshot(render);
