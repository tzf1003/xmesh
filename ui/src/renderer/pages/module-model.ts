import type { CiBinding } from "../../shared/control";
import type { MeshDeviceReport } from "../../shared/mesh";

export type CiFilter = "all" | "busy" | "attention" | "paused";
export function bindingNeedsAttention(binding: CiBinding): boolean {
  return binding.instances.some((instance) => instance.state === "broken" || instance.problem !== undefined);
}
export function filterBindings(bindings: readonly CiBinding[], query: string, state: CiFilter): CiBinding[] {
  const search = query.trim().toLowerCase();
  return bindings.filter((binding) => `${binding.target} ${binding.labels.join(" ")}`.toLowerCase().includes(search)
    && (state === "all" || state === "paused" && binding.paused || state === "attention" && bindingNeedsAttention(binding) || state === "busy" && binding.instances.some((instance) => instance.state === "busy" || instance.job !== undefined)));
}
export type MeshFilter = "all" | "online" | "offline" | "attention";
export function deviceNeedsAttention(report: MeshDeviceReport): boolean {
  return !report.device.authorized || report.issues.some((issue) => issue.severity === "error" || issue.severity === "warn");
}
export function filterMeshDevices(devices: readonly MeshDeviceReport[], query: string, state: MeshFilter): MeshDeviceReport[] {
  const search = query.trim().toLowerCase();
  return devices.filter((report) => `${report.device.name} ${report.device.hostname ?? ""} ${report.device.addresses.join(" ")} ${report.device.tags.join(" ")} ${report.roles.join(" ")}`.toLowerCase().includes(search)
    && (state === "all" || state === "online" && report.device.online || state === "offline" && !report.device.online || state === "attention" && deviceNeedsAttention(report)));
}
