import type { AuditEvent, SessionsResponse, ToolExecution, ComputerState } from "../../shared/control";
export type ExecutionStatus = "queued" | "running" | "async" | "unknown" | "success" | "failed" | "denied" | "accepted" | "cancelled" | "active" | "held" | "orphaned" | "closed" | "notice";
export type ExecutionCategory = "command" | "file" | "terminal" | "desktop" | "ui" | "backend" | "system";
export interface ExecutionRow { id:string; tool:string; category:ExecutionCategory; device:string; caller:string; client:string; status:ExecutionStatus; time?:string; duration?:number; layer?:string; target?:string; execution?:ToolExecution; audit?:AuditEvent; fields?:[string,string][] }
export function executionCategory(tool:string, event?:string):ExecutionCategory {
 if (event && !["tool_call","tool_task","task_complete","task_untracked","authz_deny"].includes(event) && !event.startsWith("pty_")) return "system";
 const local=tool.slice(tool.lastIndexOf("/")+1);
 if(local.startsWith("exec."))return "command";if(local.startsWith("fs."))return "file";if(local.startsWith("pty."))return "terminal";if(local.startsWith("computer."))return "desktop";if(local.startsWith("ui."))return "ui";return "backend";
}
export function auditExecutionStatus(event:AuditEvent):ExecutionStatus {
 if(event.event==="authz_deny" || ["unauthorized","unauthenticated","policy_denied"].includes(event.error??""))return "denied";
 if(event.task_status==="accepted" || event.task_status==="running")return "accepted";
 if(event.task_status==="cancelled" || event.task_status==="interrupted")return "cancelled";
 if(event.task_status==="unknown")return "unknown";
 if(event.status==="error")return "failed";
 if(event.event.startsWith("device_link_")&&!event.event.endsWith("online"))return "notice";
 return "success";
}
export function auditRow(event:AuditEvent,index:number):ExecutionRow {
 const d=event.args_digest;return {id:`audit:${event.execution_id??event.time}:${event.layer??""}:${event.event}:${index}`,tool:event.tool,category:executionCategory(event.tool,event.event),device:event.device,caller:event.caller,client:event.oauth_client??event.caller,status:auditExecutionStatus(event),time:event.time,duration:event.duration_ms,layer:event.layer,target:[d?.cmd,d?.resolved_path,d?.backend,d?.session_id].filter(Boolean).join(" · "),audit:event};
}
export function liveRows(sessions:SessionsResponse,hostname:string):ExecutionRow[]{
 if(sessions.executions!==undefined)return sessions.executions.map(e=>({id:e.id,tool:e.tool,category:executionCategory(e.tool),device:e.device,caller:e.caller,client:e.oauth_client??e.caller,status:e.state,time:e.started_at,layer:e.layer,target:e.run_id,execution:e}));
 return sessions.exec.map(e=>({id:e.id,tool:"exec.run",category:"command",device:hostname,caller:e.caller,client:e.oauth_client??e.caller,status:e.state,time:e.started_at,target:e.cmd}));
}
export function resourceRows(s:SessionsResponse,hostname:string,computer?:ComputerState):ExecutionRow[]{
 return [...s.pty.map(p=>({id:p.id,tool:p.name||p.id,category:"terminal" as const,device:hostname,caller:p.owner,client:p.oauth_client??(p.owner||"—"),status:p.state as ExecutionStatus,time:p.created_at,target:p.cwd,fields:[["ID",p.id],["PID",String(p.pid)],["Size",`${p.cols} × ${p.rows}`],["Idle",`${p.idle_s}s`],["Directory",p.cwd]] as [string,string][]})),...s.leases.map(l=>({id:l.resource,tool:l.resource,category:"backend" as const,device:hostname,caller:l.owner,client:l.owner,status:"held" as const,target:l.resource,fields:[["Resource",l.resource],["Owner",l.owner],["Sequence",String(l.token)],["Expires",l.expires_at]] as [string,string][]})),...(computer?.sessions??[]).map(cs=>({id:cs.session_id,tool:"computer.session",category:"desktop" as const,device:hostname,caller:cs.owner,client:cs.owner,status:"active" as const,target:cs.mode,fields:[["ID",cs.session_id],["Mode",cs.mode],["Expires",cs.expires_at]] as [string,string][]}))];
}
export function executionMatches(row:ExecutionRow,query:string,type:string,status:string,device:string,layer:string):boolean {
 return (type==="all"||row.category===type)&&(status==="all"||(status==="issues"?["failed","denied"].includes(row.status):row.status===status))&&(device==="all"||row.device===device)&&(layer==="all"||row.layer===layer)&&[row.tool,row.caller,row.client,row.target,row.device,row.id].filter(Boolean).join(" ").toLowerCase().includes(query.trim().toLowerCase());
}
