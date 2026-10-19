import assert from "node:assert/strict";
import {test} from "node:test";
import {applySaved} from "../../src/renderer/lib/reload";
import type {PanelApi} from "../../src/shared/ipc";

test("config reload failures require restart and preserve the actionable reason",async()=>{
 const global=globalThis as typeof globalThis & {xmesh?:PanelApi};
 const previous=global.xmesh;
 try {
  global.xmesh={reloadConfig:async()=>({ok:false,error:{code:"io",message:"cannot read config"}})} as unknown as PanelApi;
  assert.deepEqual(await applySaved(false),{restart:true,paths:[],applyErrors:["cannot read config"]});
  global.xmesh={reloadConfig:async()=>({ok:true,data:{restart_required:false,restart_paths:[],load_error:{message:"invalid config",line:3},apply_errors:[]}})} as unknown as PanelApi;
  assert.equal((await applySaved(false)).loadError,"invalid config");
 }finally {global.xmesh=previous;}
});
