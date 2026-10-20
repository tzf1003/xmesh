import assert from "node:assert/strict";
import {test} from "node:test";
import {kernelGone} from "../../src/main/discovery";
test("process creation time distinguishes a reused PID from the original kernel",()=>{
 const published="2026-10-19T12:00:00.000Z";
 assert.equal(kernelGone(process.pid,published,()=>Date.parse(published)+3000),true);
 assert.equal(kernelGone(process.pid,published,()=>Date.parse(published)-1000),false);
 assert.equal(kernelGone(process.pid,published,()=>undefined),false);
});
