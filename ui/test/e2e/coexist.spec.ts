import {expect,test} from "@playwright/test";
import {type Harness,launch,navigate} from "./helpers";
import {fixture} from "../../testserver/fixtures";
import type {Capability,PeersResponse} from "../../src/shared/control";

const capabilities:Capability[]=["status","events","peers","sessions","pair","config.read","config.write","gateway","pats","ingress","audit","drain"];
let h:Harness;
test.afterEach(async()=>{await h?.close()});

test("one device shows its active LAN and standby Tailnet path, then revokes trust",async()=>{
 const peers=fixture<PeersResponse>("PeersResponse.coexist").peers;
 h=await launch({kernel:{capabilities,peers,status:{network:{mode:"coexist",node_id:peers[0]!.node_id,tags:["tag:peer"]}}}});
 const page=await h.openPanel();const errors:string[]=[];page.on("pageerror",error=>errors.push(error.message));page.on("console",message=>{if(message.type()==="error")errors.push(message.text())});
 await expect(page).toHaveTitle(/xmesh/i);await expect(page).toHaveURL(/index\.html/);
 await navigate(page,"devices");
 await expect(page.getByTestId("peers-table").locator("tbody tr")).toHaveCount(2);
 const row=page.getByTestId(`peer-${peers[1]!.name}`);
 await expect(row).toContainText("workstation");
 await expect(row).toContainText("局域网");
 await expect(row).toContainText("备用：Tailnet");
 await expect(page.getByTestId("pair-manual")).toBeVisible();
 await page.getByTestId("pair-manual-form-path").selectOption("tailnet");
 await expect(page.getByTestId("pair-manual-form-path")).toHaveValue("tailnet");
 await page.screenshot({path:"/tmp/xmesh-coexist-devices.png",fullPage:false});
 await page.setViewportSize({width:1000,height:800});
 await expect(row).toBeVisible();
 await page.screenshot({path:"/tmp/xmesh-coexist-devices-compact.png",fullPage:false});
 await row.getByTestId(`device-revoke-${peers[1]!.name}`).click();
 await expect(page.getByTestId("confirm-ok")).toBeVisible();
 await page.getByTestId("confirm-ok").click();
 await expect(row).toHaveCount(0);
 expect(h.kernel.requests.some(r=>r.method==="DELETE" && r.path===`/v2/pair/trust/${peers[1]!.device_id}`)).toBe(true);
 expect(errors).toEqual([]);
});

test("an active Tailnet path stays visible when LAN is available and reports reconnecting",async()=>{
 const peers=fixture<PeersResponse>("PeersResponse.coexist").peers;
 peers[1]!.active_path="tailnet";peers[1]!.connection="direct";
 h=await launch({kernel:{capabilities,peers,status:{network:{mode:"coexist",node_id:peers[0]!.node_id,tags:["tag:peer"]}}}});
 const page=await h.openPanel();await navigate(page,"devices");
 const row=page.getByTestId(`peer-${peers[1]!.name}`);
 await expect(row).toContainText("备用：局域网");
 peers[1]!.connection_state="reconnecting";h.kernel.setPeers(peers);
 await expect(row).toContainText("重连中");
 await expect(page.getByTestId("peers-table").locator("tbody tr")).toHaveCount(2);
});
