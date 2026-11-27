# 设计提案与验证笔记

本目录收录开发过程中形成的方案草稿、代码梳理和实测笔记。它们是**提案与过程记录**，不是规范：以 [`docs/design/`](../) 下的编号方案、[约束](../../constraints/README.md) 和 [契约](../../contracts/README.md) 为准。提案与已落地行为不一致时，以契约和代码为准；文中的提交号、分支名是写作时的基线。

## 方案（`plans/`）

| 文档 | 内容 |
| --- | --- |
| [agent-to-agent-delegation.md](plans/agent-to-agent-delegation.md) | Agent 委托（delegate）的设计与实现规格 |
| [agent-browser-desktop-integration.html](plans/agent-browser-desktop-integration.html) | 接入 agent-browser 与 agent-desktop 的方案（P0 验证见下方笔记） |
| [agent-desktop-design/update-and-tool-sync.md](plans/agent-desktop-design/update-and-tool-sync.md) | agent-desktop 接入现状、更新检测与工具同步 |
| [progressive-tool-disclosure.html](plans/progressive-tool-disclosure.html) | 工具列表渐进式披露 |
| [smart-path-selection.md](plans/smart-path-selection.md) | 智能选路（LAN / Tailnet / Cloudflare） |
| [bidirectional-paths-and-direct.md](plans/bidirectional-paths-and-direct.md) | 双向最优连接与直连途径 |
| [preferred-ip-ui.md](plans/preferred-ip-ui.md) | 优选 IP 的界面与内核方案 |

## 笔记（`notes/`）

| 文档 | 内容 |
| --- | --- |
| [cloud-to-mac-auth-chain.md](notes/cloud-to-mac-auth-chain.md) | 云端 Claude → ingress → Mac 的鉴权链路梳理，用于排查设备 stale |
| [p0-validation-2026-11-01.md](notes/p0-validation-2026-11-01.md) | agent-browser 与 agent-desktop P0 对照验证 |
| [smart-path-selection-design.md](notes/smart-path-selection-design.md) | 智能选路的早期设计笔记（与 `plans/smart-path-selection.md` 同一主题，后者为后续草案） |

## 参考与原型

| 路径 | 内容 |
| --- | --- |
| [agent-desktop-upstream/pin-1.0.2.json](agent-desktop-upstream/pin-1.0.2.json) | agent-desktop 1.0.2 的上游锁定快照（历史版本，当前清单规则见 [agent-desktop 清单契约](../../contracts/agent-desktop-manifest.md)） |
| [mac-background-session/probe.swift](mac-background-session/probe.swift) | macOS 后台会话输入探针原型（实验代码，不参与构建） |
