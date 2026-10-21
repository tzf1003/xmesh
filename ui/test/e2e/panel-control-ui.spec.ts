import { expect, test } from "@playwright/test";
import { launch, navigate, openModuleSettings, type Harness } from "./helpers";
import type { Capability, ToolExecution } from "../../src/shared/control";
import { parse } from "yaml";
const CAPS:Capability[]=["status","events","peers","sessions","drain","config.read","config.write","pair","pats","ingress","gateway","audit","ci"];
let h:Harness;
test.afterEach(async()=>{await h?.close()});

test("settings owns appearance, about and low-frequency setup; role menus are reachable",async()=>{
 h=await launch({kernel:{capabilities:CAPS}});const page=await h.openPanel();
 await expect(page.getByTestId("nav-advanced")).toHaveCount(0);
 await expect(page.getByTestId("nav-config")).toHaveText("节点与网络");
 await expect(page.getByTestId("nav-config").locator("xpath=following-sibling::button[1]")).toHaveAttribute("data-testid","nav-authz");
 await page.getByTestId("nav-config").click();
 await expect(page.getByTestId("nav-config")).toHaveAttribute("aria-current","page");
 await expect(page.getByTestId("page-config")).toBeVisible();
 await expect(page.getByTestId("settings-node")).toBeVisible();
 await expect(page.getByTestId("config-editor")).toHaveCount(0);
 await navigate(page,"kernel-config");
 await expect(page.getByTestId("config-editor")).toBeVisible();
 await expect(page.getByTestId("settings-tab-configuration")).toHaveAttribute("aria-current","page");
 await expect(page.getByTestId("nav-about")).toHaveCount(0);
 await expect(page.getByTestId("nav-setup")).toHaveCount(0);
 await expect(page.getByTestId("nav-ci")).toBeVisible();await expect(page.getByTestId("nav-mesh")).toBeVisible();
 await navigate(page,"settings");await page.getByTestId("theme-dark").click();
 await expect(page.locator("html")).toHaveAttribute("data-theme","dark");
 await page.getByTestId("settings-tab-about").click();await expect(page.getByTestId("page-about")).toBeVisible();
 await page.getByTestId("settings-tab-general").click();await expect(page.getByTestId("settings-open-setup")).not.toBeVisible();
 await page.getByTestId("settings-maintenance").locator("summary").click();await page.getByTestId("settings-open-setup").click();await expect(page.getByTestId("page-setup")).toBeVisible();
});

test("kernel configuration formats legacy JSON and saves YAML under Settings",async({},info)=>{
 h=await launch({kernel:{capabilities:CAPS}});
 const original={schema_version:2,node:{name:"config-test",roles:["agent","gateway"]},mesh:{lan:{enabled:true,mdns:true},tailnet:{enabled:false}},agent:{default_cwd:"/tmp",cwd_policy:{allow:["/tmp"]}},gateway:{listen:"127.0.0.1:8080"},audit:{retention:"24h"}};
 h.kernel.panel.setContent(JSON.stringify(original));
 const page=await h.openPanel();const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
 await navigate(page,"kernel-config");
 await expect(page.getByRole("heading",{name:"内核配置",exact:true})).toBeVisible();
 const editor=page.getByTestId("config-editor");
 await expect(editor).toHaveValue(/\n  "name": "config-test"/);
 expect(parse(await editor.inputValue())).toEqual(original);expect(h.kernel.panel.writes).toEqual([]);
 await editor.fill(JSON.stringify({...original,audit:{retention:"48h"}}));
 await page.getByTestId("config-format").click();
 await expect(editor).toHaveValue(/\n  "retention": "48h"/);
 await page.getByTestId("settings-tab-general").click();await expect(page.getByTestId("confirm")).toBeVisible();await page.getByTestId("confirm-cancel").click();
 await expect(editor).toHaveValue(/48h/);
 await page.getByTestId("config-review").click();await page.getByTestId("confirm-ok").click();
 await expect.poll(()=>h.kernel.panel.writes.length).toBe(1);expect(parse(h.kernel.panel.content).audit.retention).toBe("48h");expect(h.kernel.panel.content.trimStart().startsWith("{")).toBe(false);
 await page.setViewportSize({width:1440,height:1000});await page.screenshot({path:info.outputPath("kernel-config-desktop.png")});
 await page.setViewportSize({width:640,height:900});const size=await page.locator("main").evaluate(e=>({client:e.clientWidth,scroll:e.scrollWidth}));expect(size.scroll).toBeLessThanOrEqual(size.client+1);await page.screenshot({path:info.outputPath("kernel-config-compact.png")});
 expect(errors).toEqual([]);
});

test("live tools and asynchronous tasks retain their states; history paginates and exposes audit metadata",async()=>{
 h=await launch({kernel:{capabilities:CAPS}});const start=new Date().toISOString();
 const calls:ToolExecution[]=[{id:"x-call-001",tool:"fs.read",device:"node-a",caller:"gateway/a",oauth_client:"client-a",layer:"agent",state:"running",started_at:start,updated_at:start},{id:"x-call-002",tool:"ui.run",device:"node-b",caller:"gateway/a",oauth_client:"client-a",layer:"gateway",state:"async",started_at:start,updated_at:start,run_id:"run-ui-1",task_status:"running"}];
 h.kernel.panel.sessions.executions=calls;
 h.kernel.panel.audit=Array.from({length:105},(_,i)=>({time:new Date(Date.now()-i*1000).toISOString(),event:"tool_call" as const,tool:"fs.read",device:"node-a",caller:"gateway/a",status:i===0?"error" as const:"ok" as const,error:i===0?"permission_denied":null,duration_ms:i+1,execution_id:`execution-${i}`,layer:"agent" as const}));
 const page=await h.openPanel();await navigate(page,"sessions");await expect(page.getByTestId("execution-row")).toHaveCount(2);
 await page.getByTestId("execution-row").filter({hasText:"ui.run"}).click();
 const detail=page.getByTestId("execution-details-dialog");if(await detail.isVisible())await expect(detail).toContainText("后台任务仍在执行");else await expect(page.locator(".execution-inspector")).toContainText("后台任务仍在执行");
 if(await detail.isVisible())await page.keyboard.press("Escape");
 h.kernel.panel.sessions.executions=[calls[0]!];await expect(page.getByTestId("execution-row")).toHaveCount(1);
 await page.getByTestId("sessions-tab-history").click();await expect(page.getByTestId("execution-row")).toHaveCount(100);
 await page.getByTestId("audit-more").click();await expect(page.getByTestId("execution-row")).toHaveCount(105);
 await page.getByTestId("execution-status").selectOption("failed");await expect(page.getByTestId("execution-row")).toHaveCount(1);
 await page.getByTestId("execution-row").click();const audit=page.getByTestId("execution-audit").filter({visible:true});await audit.locator("summary").click();await expect(audit).toContainText("execution_id");
});

test("module drawers preserve scoped drafts and keyboard focus while nested review is open",async()=>{
 h=await launch({kernel:{capabilities:CAPS}});h.kernel.panel.setContent(JSON.stringify({node:{name:"control-test",roles:["agent","gateway"]},mesh:{mode:"lan"},gateway:{listen:"127.0.0.1:8080"}}));const page=await h.openPanel();await openModuleSettings(page,"gateway");
 const listen=page.getByTestId("settings-gateway-listen");await listen.fill("127.0.0.1:9090");await page.getByTestId("settings-review").click();
 await page.keyboard.press("Escape");await expect(page.getByTestId("confirm")).toHaveCount(0);await expect(listen).toHaveValue("127.0.0.1:9090");
 await page.getByTestId("module-settings-dialog").locator("input:visible:enabled,select:visible:enabled,textarea:visible:enabled,button:visible:enabled,a[href]:visible").last().focus();await page.keyboard.press("Tab");await expect(page.getByTestId("module-settings-dialog").getByRole("button",{name:"Close",exact:true})).toBeFocused();
 await page.getByTestId("module-settings-dialog").getByRole("button",{name:"Close",exact:true}).click();await expect(page.getByTestId("confirm")).toBeVisible();await page.getByTestId("confirm-cancel").click();await expect(listen).toHaveValue("127.0.0.1:9090");
});

for(const width of [390,640,1440])test(`control UI renders at ${width}px with visible role controls`,async({},info)=>{
 h=await launch({kernel:{capabilities:CAPS}});const page=await h.openPanel();await page.setViewportSize({width,height:900});const errors:string[]=[];page.on("pageerror",e=>errors.push(e.message));
 for(const id of ["devices","sessions","settings","ci","mesh"]){await navigate(page,id);await expect(page.getByTestId(id === "mesh" ? "page-mesh" : id === "ci" ? "page-ci" : `page-${id}`)).toBeVisible();await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>resolve())));const size=await page.locator("main").evaluate(e=>({client:e.clientWidth,scroll:e.scrollWidth}));expect(size.scroll, `${id} at ${width}px`).toBeLessThanOrEqual(size.client+1);}
 await navigate(page,"settings");await page.screenshot({path:info.outputPath(`settings-${width}.png`)});for(const role of ["agent","gateway","ingress"]){await expect(page.getByTestId(`role-${role}`)).toBeVisible()}
 expect(errors).toEqual([]);
});

test("retired asynchronous tracking remains an unknown audit outcome",async()=>{
 h=await launch({kernel:{capabilities:CAPS}});h.kernel.panel.audit=[{time:new Date().toISOString(),event:"task_untracked",tool:"ui.run",device:"node-a",caller:"node-a/session",status:"error",error:"authorization_lost",duration_ms:1000,execution_id:"x-retired",run_id:"run-retired",task_status:"unknown",layer:"agent"}];
 const page=await h.openPanel();await navigate(page,"sessions");await page.getByTestId("sessions-tab-history").click();await expect(page.getByTestId("execution-row")).toContainText("状态未知");await page.getByTestId("execution-more-filters").click();await page.getByTestId("audit-event").selectOption("task_untracked");await page.getByTestId("audit-search").click();await expect(page.getByTestId("execution-row")).toHaveCount(1);await page.getByTestId("execution-row").click();const audit=page.getByTestId("execution-audit").filter({visible:true});await audit.locator("summary").click();await expect(audit).toContainText("authorization_lost");await expect(audit).toContainText("x-retired");
});
