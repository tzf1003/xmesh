import assert from "node:assert/strict";
import { test } from "node:test";
import { assignSlots, linkKind, peerKey, MAX_TOPOLOGY_PEERS, callBelongsTo, layoutTopology, toolName, topologyPeers } from "../../src/renderer/lib/topology";
import { peersFixture, statusFixture } from "../../testserver/fixtures";

test("peer positions survive order, online state and insertion; slots do not collide", () => {
  const peers = peersFixture();
  const first = assignSlots(peers, new Map());
  const added = { ...peers[1]!, name: "added", node_id: "added" };
  const next = assignSlots([added, ...peers.slice().reverse().map((p) => ({ ...p, online: !p.online }))], first);
  for (const [id, slot] of first) assert.equal(next.get(id), slot);
  assert.equal(new Set(next.values()).size, next.size);
  const overflow = assignSlots(Array.from({ length: 30 }, (_, i) => ({ ...added, node_id: `p${i}` })), new Map());
  assert.equal(overflow.size, MAX_TOPOLOGY_PEERS);
});

test("offline is distinct from pending, and sys.info failure does not change a direct path", () => {
  const p = peersFixture()[1]!;
  assert.equal(linkKind({ ...p, online: false }), "offline");
  assert.equal(linkKind({ ...p, connection: "unknown" }), "unknown");
  assert.equal(linkKind({ ...p, info: { refreshed_at: null, error: "timeout" } }), "direct");
});

test("self is derived from the current kernel rather than stale peer data", () => {
  const status = statusFixture();
  const self = topologyPeers(status, [])[0]!;
  assert.equal(self.name, status.hostname);
  assert.equal(self.self, true);
  assert.equal(topologyPeers(status, peersFixture()).filter((p) => p.self).length, 1);
  assert.equal(peerKey(self), status.network.node_id);
});

test("device calls match caller identity and execution device independently", () => {
  const peer = {...peersFixture()[1]!, device_id:"dev-source"};
  const call = { tool: "dev-target/sys.info", device: "cloud-vm", caller: "dev-source/session", oauth_client: null, status: "ok" as const, error: null, duration_ms: 1 };
  assert.equal(callBelongsTo(call, peer, "initiated"), true);
  assert.equal(callBelongsTo(call, peer, "executed"), false);
  assert.equal(callBelongsTo(call, peersFixture()[2]!, "executed"), true);
  assert.equal(callBelongsTo({...call,caller:"unknown/session"}, peer, "all"), false);
  assert.equal(toolName(call.tool), "sys.info");
  assert.equal(toolName("sys.info"), "sys.info");
  const status=statusFixture();
  assert.equal(topologyPeers(status,[{...peersFixture()[0]!,device_id:"dev-local"}])[0]!.device_id,"dev-local");
});

function layout(peers=peersFixture(), width=800) {
  return layoutTopology(topologyPeers(statusFixture(),peers),assignSlots(peers,new Map()),width);
}

test("route bands place direct devices above aligned relay groups", () => {
  const peers=[peersFixture()[0]!,{...peersFixture()[1]!,name:"beta",node_id:"beta"},
    {...peersFixture()[2]!,name:"worker-03",node_id:"worker-03",derp_region:"hkg"},
    {...peersFixture()[2]!,name:"build-agent",node_id:"build-agent",derp_region:"sin"}];
  const result=layout(peers);
  const node=(id:string)=>result.nodes.find(n=>n.id===id)!;
  assert.ok(node("beta").y<node("worker-03").y);
  assert.ok(node("worker-03").y<node("build-agent").y);
  assert.equal(node("worker-03").y,node("relay:hkg").y);
  assert.equal(node("build-agent").y,node("relay:sin").y);
  assert.ok(node("win-dev-01").x<node("relay:hkg").x);
  assert.ok(node("relay:hkg").x<node("worker-03").x);
  assert.deepEqual(result,layout(peers.slice().reverse()));
});

test("single node centers and direct-only graphs center the two sides", () => {
  const alone=layout([peersFixture()[0]!]);
  assert.equal(alone.nodes[0]!.x,alone.width/2);
  assert.equal(alone.nodes[0]!.y,alone.height/2);
  const pair=layout(peersFixture().slice(0,2));
  const [local,device]=pair.nodes;
  assert.equal(local!.y,device!.y);
  assert.equal(local!.x-local!.width/2,pair.width-device!.x-device!.width/2);
});

test("orthogonal routes clear every unrelated card at multiple viewport widths", () => {
  const template=peersFixture()[1]!;
  for(const width of [560,700,800,1200]) {
    for(const count of [1,3,8,20]) {
      const peers=[peersFixture()[0]!,...Array.from({length:count},(_,i)=>({...template,name:`p${i}`,node_id:`p${i}`,connection:i%3===0?"direct" as const:"derp" as const,derp_region:i%2?"hkg":"sin"}))];
      const result=layout(peers,width);
      assert.ok(result.nodes.filter(n=>n.peer&&!n.peer.self).length<=MAX_TOPOLOGY_PEERS);
      for(const edge of result.edges) {
        assert.doesNotMatch(edge.path,/[CQAZ]/);
        for(let i=1;i<edge.points.length;i++) {
          const a=edge.points[i-1]!,b=edge.points[i]!;
          assert.ok(a.x===b.x||a.y===b.y);
          for(const node of result.nodes.filter(n=>n.id!==edge.from&&n.id!==edge.to)) {
            const l=node.x-node.width/2,r=node.x+node.width/2,t=node.y-node.height/2,bottom=node.y+node.height/2;
            const intersects=a.x===b.x?a.x>l&&a.x<r&&Math.max(a.y,b.y)>t&&Math.min(a.y,b.y)<bottom:a.y>t&&a.y<bottom&&Math.max(a.x,b.x)>l&&Math.min(a.x,b.x)<r;
            assert.equal(intersects,false,`${width}: ${edge.id} intersects ${node.id}`);
          }
        }
      }
    }
  }
});

test("independent relay groups do not cross each other's routes", () => {
  const template = peersFixture()[1]!;
  const peers = [peersFixture()[0]!, ...Array.from({ length: 12 }, (_, i) => ({
    ...template,
    name: `peer-${i}`,
    node_id: `peer-${i}`,
    connection: i % 3 === 0 ? "direct" as const : "derp" as const,
    derp_region: i % 2 ? "hkg" : "sin",
  }))];
  const result = layout(peers, 900);
  for (let i = 0; i < result.edges.length; i++) {
    for (let j = i + 1; j < result.edges.length; j++) {
      const first = result.edges[i]!;
      const second = result.edges[j]!;
      if (first.from === second.from) continue;
      for (let a = 1; a < first.points.length; a++) {
        const p = first.points[a - 1]!, q = first.points[a]!;
        for (let b = 1; b < second.points.length; b++) {
          const r = second.points[b - 1]!, s = second.points[b]!;
          const crossing = p.y === q.y && r.x === s.x
            ? r.x > Math.min(p.x, q.x) && r.x < Math.max(p.x, q.x) && p.y > Math.min(r.y, s.y) && p.y < Math.max(r.y, s.y)
            : p.x === q.x && r.y === s.y
              ? p.x > Math.min(r.x, s.x) && p.x < Math.max(r.x, s.x) && r.y > Math.min(p.y, q.y) && r.y < Math.max(p.y, q.y)
              : false;
          assert.equal(crossing, false, `${first.id} crosses ${second.id}`);
        }
      }
    }
  }
});
