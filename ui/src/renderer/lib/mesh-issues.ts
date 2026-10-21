import type { Locale } from "../../shared/i18n";
import type { MeshIssue } from "../../shared/mesh";

/** Keep unknown diagnostics verbatim so newer kernels remain explainable. */
export function meshIssueText(issue: MeshIssue, locale: Locale): string {
  if (locale === "en") return issue.message;
  switch (issue.code) {
    case "no-tags":
      return "旧版节点尚未设置网络标签，请核对旧版访问规则与角色配置。";
    case "unauthorized":
      return "设备尚未获得控制面授权，请在控制面中确认授权。";
    case "unexpected-device-tag":
      return "设备带有 tag:device，但 xmesh.yaml 尚未启用 Agent 角色，请核对配置。";
    case "missing-role-tag": {
      const match = /^xmesh\.yaml declares role "([^"]+)" but the node lacks (.+) \(it will not appear in discovery or will be denied\)$/.exec(issue.message);
      if (match) return `配置已启用 ${match[1]} 角色，设备还需设置 ${match[2]!.split(" or ").join(" 或 ")}。请核对标签与访问规则，否则可能影响设备发现或访问。`;
      break;
    }
    case "key-expired":
      if (issue.message.startsWith("node key expired ")) return `设备密钥已过期（${issue.message.slice(17)}），请重新认证。`;
      break;
    case "key-expiring":
      if (issue.message.startsWith("node key expires ")) return `设备密钥即将过期（${issue.message.slice(17)}），请及时重新认证。`;
      break;
  }
  return issue.message;
}
