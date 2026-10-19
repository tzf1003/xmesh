// Opt-in real DMG replacement and Computer Host recovery in a temporary bundle.
// Requires an absent user Computer Host installation. The application entry
// point is a windowless receipt fixture; the kernel and native helper are real.
import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { createRequire } from "node:module";
import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require=createRequire(import.meta.url);
const { computerHostServices }=require("../dist/src/main/computer-host.js");
const { ApplyError,macApplier }=require("../dist/src/main/update/apply.js");
const { Updater }=require("../dist/src/main/update/updater.js");
const { UpdateStore }=require("../dist/src/main/update/store.js");
const { installerName, CHECKSUM_FILE }=require("../dist/src/main/update/releases.js");
const { parseVersion }=require("../dist/src/shared/version.js");
const exec=promisify(execFile);
const rootRepo=path.resolve(path.dirname(fileURLToPath(import.meta.url)),"../..");
const args=process.argv.slice(2);
const option=(name)=>{const i=args.indexOf("--"+name);return i<0?undefined:args[i+1];};
assert.equal(process.platform,"darwin");
assert.equal(process.env.XMESH_TEST_ALLOW_COMPUTER_UPGRADE,"1","explicit isolated upgrade acceptance opt-in required");
const inputs={oldBinary:option("old-binary"),newBinary:option("new-binary"),oldHelper:option("old-helper"),newHelper:option("new-helper")};
for(const value of Object.values(inputs))assert.ok(value&&path.isAbsolute(value)&&fs.statSync(value).isFile(),"four absolute binary paths required");
const version=async(binary,native=false)=>{
  const {stdout}=await exec(binary,[native?"--version":"version"],{timeout:10000});
  const match=stdout.trim().match(native?/^xmesh-computer (\S+) protocol=1$/:/^xmesh (\S+)$/);
  assert.ok(match,"unexpected executable version");return match[1].replace(/^v/,"");
};
const beforeVersion=await version(inputs.oldBinary),afterVersion=await version(inputs.newBinary);
assert.notEqual(beforeVersion,afterVersion,"the test requires different kernel versions");
assert.equal(await version(inputs.oldHelper,true),beforeVersion);
assert.equal(await version(inputs.newHelper,true),afterVersion);
const label="xmesh-computer-host";
const serviceFile=path.join(os.homedir(),"Library/LaunchAgents",label+".plist");
assert.equal(fs.existsSync(serviceFile),false,"an existing user desktop installation must be preserved");
let registered=false;
try{await exec("launchctl",["print",`gui/${process.getuid()}/${label}`],{timeout:5000});registered=true;}
catch(error){assert.match(String(error.stderr),/Could not find service/,"desktop service absence could not be established");}
assert.equal(registered,false,"a loaded user desktop service must be preserved");

const root=fs.mkdtempSync("/tmp/xmesh-upgrade-accept-");
const app=path.join(root,"installed","Xmesh Upgrade Fixture.app");
const receipt=path.join(root,"launch.json"),state=path.join(root,"host"),data=path.join(root,"updates");
const mounts=path.join(root,"mounts");fs.mkdirSync(mounts);
const appID="app.xmesh.upgrade-fixture."+randomBytes(8).toString("hex");
const events=[];
const ownedProcesses=new Set();
const owner=randomBytes(24).toString("base64url");
let installed=false,oldApp,installerProcess,heartbeat;
let interrupted,cleaning=false;
for(const signal of ["SIGINT","SIGTERM"])process.on(signal,()=>{interrupted=new Error("upgrade acceptance interrupted");});
const gone=(pid)=>{try{process.kill(pid,0);return false;}catch(error){return error.code==="ESRCH";}};
const delay=(ms)=>new Promise(resolve=>setTimeout(resolve,ms));
async function waitFor(check,label,timeout=30000){
  const deadline=Date.now()+timeout;
  do{if(interrupted&&!cleaning)throw interrupted;if(await check())return;await delay(50);}while(Date.now()<deadline);
  throw new Error("timed out: "+label);
}
async function writePlist(destination,value){
  fs.writeFileSync(destination,JSON.stringify(value));
  await exec("plutil",["-convert","xml1",destination]);
}
async function bundle(destination,binary,helper,bundleVersion,wait){
  const macos=path.join(destination,"Contents/MacOS");
  const bin=path.join(destination,"Contents/Resources/bin");
  fs.mkdirSync(macos,{recursive:true});fs.mkdirSync(bin,{recursive:true});
  await exec("xcrun",["swiftc","-swift-version","5","-parse-as-library","-o",path.join(macos,"fixture"),path.join(rootRepo,"computer-native/macos/Tests/UpgradeFixture.swift")]);
  fs.copyFileSync(binary,path.join(bin,"xmesh"));fs.chmodSync(path.join(bin,"xmesh"),0o755);
  await exec("python3",[path.join(rootRepo,"scripts/package_computer.py"),"--binary",helper,"--output",path.join(bin,"xmesh-computer.app"),"--platform","darwin","--version",bundleVersion]);
  await writePlist(path.join(destination,"Contents/Info.plist"),{CFBundleIdentifier:appID,CFBundleExecutable:"fixture",CFBundleName:"Xmesh Upgrade Fixture",CFBundlePackageType:"APPL",CFBundleVersion:bundleVersion,LSBackgroundOnly:true,XmeshUpgradeReceipt:receipt,XmeshUpgradeWait:wait});
  await exec("codesign",["--force","--deep","--sign","-",destination]);
}
const installedBinary=path.join(app,"Contents/Resources/bin/xmesh");
const installedHelper=path.join(app,"Contents/Resources/bin/xmesh-computer.app/Contents/MacOS/xmesh-computer");
async function command(command){
  if(interrupted&&!cleaning)throw interrupted;
  const {stdout}=await exec(installedBinary,["computer",command,"--state-dir",state,...(command==="status"?[]:["--helper",installedHelper,"--json"])],{timeout:35000,maxBuffer:65536});
  const result=JSON.parse(stdout);if(command!=="status")assert.equal(result.ok,true);return result;
}
async function host(method,body={}){
  return new Promise((resolve,reject)=>{
    const request=http.request({socketPath:path.join(state,"control.sock"),path:"/v1/"+method,method:"POST",headers:{"Content-Type":"application/json","X-Xmesh-Computer-Owner":owner}},response=>{
      let raw="";response.setEncoding("utf8");response.on("data",chunk=>{raw+=chunk;});response.on("end",()=>{
        try{assert.equal(response.statusCode,200,raw);resolve(JSON.parse(raw));}catch(error){reject(error);}
      });
    });
    request.setTimeout(15000,()=>request.destroy(new Error("host request timeout")));
    request.on("error",reject);request.end(JSON.stringify(body));
  });
}
const services=(expected)=>computerHostServices({version:expected,pollMs:50,run:command,gone});
let outcome;
try{
  await bundle(app,inputs.oldBinary,inputs.oldHelper,beforeVersion,true);
  const staging=path.join(root,"image-source"),newApp=path.join(staging,path.basename(app));
  await bundle(newApp,inputs.newBinary,inputs.newHelper,afterVersion,false);
  const dmg=path.join(root,"installer.dmg");
  await exec("hdiutil",["create","-quiet","-format","UDZO","-srcfolder",staging,dmg],{timeout:120000});
  const installer=fs.readFileSync(dmg),sha=createHash("sha256").update(installer).digest("hex");
  const name=installerName(parseVersion(afterVersion),{platform:"mac",arch:process.arch});
  const source={
    releases:async()=>[{tag_name:"v"+afterVersion,draft:false,prerelease:afterVersion.includes("-"),published_at:"2026-10-19T00:00:00Z",body:"Isolated upgrade fixture",assets:[{name,size:installer.length,digest:"sha256:"+sha},{name:CHECKSUM_FILE,size:sha.length+name.length+3}]}],
    text:async()=>`${sha}  ${name}\n`,
    download:async(_tag,_name,destination,progress,signal)=>{signal.throwIfAborted();fs.writeFileSync(destination,installer);progress({received:installer.length,total:installer.length});return{sha256:sha,bytes:installer.length};},
  };
  await command("install");installed=true;await command("start");
  await waitFor(async()=>{try{return(await command("status")).running;}catch{return false;}},"old host start");
  let beforeInfo=await host("info",{protocol_version:1});
  let before=await command("status");assert.ok(before.native_pid>0);assert.equal(before.version.replace(/^v/,""),beforeVersion);
  ownedProcesses.add(before.pid);ownedProcesses.add(before.native_pid);
  heartbeat=setInterval(()=>void host("heartbeat").catch(()=>{}),2000);
  oldApp=spawn(path.join(app,"Contents/MacOS/fixture"),[],{stdio:"ignore"});
  await waitFor(()=>fs.existsSync(receipt),"old app receipt");
  const oldServices=services(beforeVersion),newServices=services(afterVersion);
  const applier=macApplier(path.join(app,"Contents/MacOS/fixture"),(command,args,options)=>{
    installerProcess=spawn(command,args,{...options,env:{...process.env,TMPDIR:mounts}});return installerProcess;
  });
  const store=new UpdateStore(path.join(root,"preferences.json"));store.set({prefs:{auto:false,prerelease:true}});
  const env={version:beforeVersion,packaged:true,platform:"darwin",arch:process.arch,kernelBundled:true,execPath:path.join(app,"Contents/MacOS/fixture"),pid:oldApp.pid,dataDir:data,source,applier,store,
    runningNodes:()=>[],stopNode:async()=>{throw new Error("unexpected node stop");},startNode:async()=>{throw new Error("unexpected node start");},
    stopComputer:async()=>{const stopped=await oldServices.stop();assert.equal(stopped,"stopped");assert.ok(gone(before.pid)&&gone(before.native_pid));events.push("old-host-and-native-exited");return stopped;},
    startComputer:()=>oldServices.start(),quit:()=>{events.push("old-app-quit");oldApp.kill("SIGTERM");},openPath:async()=>{throw new Error("unexpected manual installation");},notify:()=>{}};
  const failed=new Updater({...env,applier:{blocked:()=>"",start:async()=>{throw new ApplyError("spawn_failed","isolated handover rejection");}},quit:()=>{throw new Error("failed handover quit the application");}});
  await failed.check();const failure=await failed.install();assert.equal(failure.ok,false);assert.equal(failure.error.code,"install_failed");
  const recoveredInfo=await host("info",{protocol_version:1}),recovered=await command("status");
  assert.ok(recovered.running&&recovered.pid!==before.pid&&recovered.native_pid>0&&recovered.native_pid!==before.native_pid);
  ownedProcesses.add(recovered.pid);ownedProcesses.add(recovered.native_pid);
  assert.equal(recovered.version.replace(/^v/,""),beforeVersion);assert.notEqual(recoveredInfo.desktop_epoch,beforeInfo.desktop_epoch);
  assert.equal(fs.existsSync(path.join(data,"pending.json")),false);
  assert.equal(oldApp.exitCode,null);events.push("failed-handover-restored-old-host-and-native");
  before=recovered;beforeInfo=recoveredInfo;
  const updater=new Updater(env);await updater.check();assert.equal(updater.state.phase,"available");
  assert.deepEqual(await updater.install(),{ok:true});
  const pending=JSON.parse(fs.readFileSync(path.join(data,"pending.json"),"utf8"));assert.equal(pending.computer,true);
  await waitFor(()=>{try{return JSON.parse(fs.readFileSync(receipt,"utf8")).version===afterVersion;}catch{return false;}},"new app launch",45000);
  await waitFor(()=>installerProcess.exitCode!==null||installerProcess.signalCode!==null,"installer exit");assert.equal(installerProcess.exitCode,0);
  events.push("dmg-replaced-bundle-and-launched-new-entry");
  assert.equal(await version(installedBinary),afterVersion);assert.equal(await version(installedHelper,true),afterVersion);
  await exec("codesign",["--verify","--deep","--strict",app]);
  const after=new Updater({...env,version:afterVersion,startComputer:async()=>{await newServices.start();events.push("new-host-version-verified");},quit:()=>{}});
  await after.finishPending();assert.equal(after.state.outcome?.ok,true);
  const afterInfo=await host("info",{protocol_version:1}),afterStatus=await command("status");
  ownedProcesses.add(afterStatus.pid);ownedProcesses.add(afterStatus.native_pid);
  assert.notEqual(afterStatus.pid,before.pid);assert.ok(afterStatus.native_pid>0&&afterStatus.native_pid!==before.native_pid);
  assert.notEqual(afterInfo.desktop_epoch,beforeInfo.desktop_epoch);assert.equal(afterStatus.version.replace(/^v/,""),afterVersion);
  assert.equal(fs.existsSync(app+".update-old"),false);
  assert.equal(fs.existsSync(path.join(data,"pending.json")),false);
  outcome={result:"PASS",from:beforeVersion,to:afterVersion,checks:events,host_pid_changed:true,native_pid_changed:true,desktop_epoch_changed:true,restored_native_state:afterInfo.state,application_entry:"windowless-fixture",installer:"real-hdiutil-dmg"};
}finally{
  cleaning=true;
  if(heartbeat)clearInterval(heartbeat);
  if(oldApp&&oldApp.exitCode===null&&oldApp.signalCode===null)oldApp.kill("SIGTERM");
  if(installerProcess&&installerProcess.exitCode===null&&installerProcess.signalCode===null)installerProcess.kill("SIGTERM");
  if(fs.existsSync(receipt)){
    const launched=JSON.parse(fs.readFileSync(receipt,"utf8"));
    try{
      const {stdout}=await exec("ps",["-p",String(launched.pid),"-o","comm="]);
      if(stdout.trim()===path.join(app,"Contents/MacOS/fixture"))process.kill(launched.pid,"SIGTERM");
    }catch{}
  }
  if(installed||fs.existsSync(serviceFile)){
    const plist=JSON.parse((await exec("plutil",["-convert","json","-o","-",serviceFile])).stdout);
    if(plist.ProgramArguments?.[0]===installedBinary){
      await command("uninstall");
      assert.equal(fs.existsSync(serviceFile),false);
      assert.equal((await command("status")).running,false);
      let loaded=false;
      try{await exec("launchctl",["print",`gui/${process.getuid()}/${label}`]);loaded=true;}
      catch(error){assert.match(String(error.stderr),/Could not find service/);}
      assert.equal(loaded,false,"temporary desktop service remained registered");
      await waitFor(()=>[...ownedProcesses].every(gone),"owned Host/native process cleanup");
    }else assert.equal(installed,false,"service ownership changed; preserve it");
  }
  for(const mount of fs.readdirSync(mounts)){try{await exec("hdiutil",["detach",path.join(mounts,mount),"-quiet","-force"]);}catch{}}
  fs.rmSync(root,{recursive:true,force:true});
}
if(interrupted)throw interrupted;
console.log(JSON.stringify({...outcome,cleanup:"PASS"}));
