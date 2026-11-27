# agent-desktop 接入现状、更新检测与工具同步设计

日期：2026-11-03　状态：设计草案，未改动代码
依据：仓库 `tzf1003/xmesh`，分支 `claude/project-thread-a16dzh`（HEAD `bbba375`）；npm 包 `agent-desktop` 的 1.0.2 与 1.1.0 两个版本的 README、`bin/agent-desktop`、`lib/platform.js`、`scripts/postinstall.js`。上游二进制没有在沙箱中运行（执行被拒绝），所以第 2 节和第 7 节中关于上游 CLI 行为的内容只来自上述文本，需要在有权限的机器上核对。

---

## 0. 结论

1. **仓库里没有任何代码分发、固定或校验 agent-desktop。** 它只能人工安装到 xmesh 旁边，或用 `--agent-desktop` 指定，Host 只检查它是不是绝对路径下的普通文件。`docs/contracts/computer.md:68` 写的“固定版本”，以及 `plans/agent-browser-desktop-integration.html` 中计划的“随包分发、记录版本与 SHA-256”，目前都没有实现。
2. **上游的最新版本是 1.1.0**（npm `latest`，2026-11-03 04:15 UTC）。我们 P0 验证用的是 1.0.2（2026-10-30）。从 1.0.0 到 1.1.0 只隔了 6 天，发了 4 个版本。
3. **1.1.0 的命令表与 1.0.2 完全相同**（60 个命令名），README 里没有看到新增的工具。可见的变化有三处：macOS 上每次 CLI 调用改为独立进程（原来复用 command host）、信封版本从 2.5 变为 2.6、平台说明更新。其中第一处最要紧：`ref` 失效的判定依赖上游的 command host 语义，升级前必须在 Mac 上重新验证。
4. **我们目前只映射了上游约 20 个命令**，其余的（如 `find`、`get`、`list-apps`、`launch`、剪贴板、通知、`wait`、`hover` 等）都没有暴露。所以“接入新工具”应该是一个显式的、由映射清单驱动、默认拒绝的过程，而不是把 CLI 自动或原样暴露给 MCP 和云端。
5. **推荐路线**：固定版本并随包分发（阶段一）→ 更新检测（CI 每日探测加设备端只读状态）→ 手动升级（PR 或本机命令，带 SHA-256 校验与回滚）→ 自动升级（默认只提示，仅补丁版本、空闲时、非 macOS 或经用户确认才应用）→ 新工具经清单映射后，再按能力与 scope 暴露。

---

## 1. 现状：agent-desktop 如何接入

### 1.1 分发与发现

| 环节 | 现状 | 位置 |
|---|---|---|
| 打包 | 只打包 `xmesh-computer`（macOS 为 `xmesh-computer.app`），没有 agent-desktop | `scripts/package_computer.py`，`ui/electron-builder.config.mjs:42,89`；`.github/workflows/release.yml` 中没有 agent-desktop |
| 路径 | `--agent-desktop` 显式指定；缺省为 xmesh 旁边的 `agent-desktop`，macOS 优先 `xmesh-computer.app/Contents/MacOS/`，使辅助功能授权记在 xmesh 名下 | `cmd/xmesh/computer.go:81-87`，`internal/computer/engine_factory.go:24-39` |
| 校验 | 只要求绝对路径且是普通文件，没有版本、SHA-256、签名检查 | `internal/computer/engine_factory.go:56-58` |
| 版本 | 没有探测。`computer.info` 里的 `protocol_version` 是 xmesh 自己的 IPC 协议版本，不是 agent-desktop 的版本 | `internal/computer/types.go:26`，`internal/computer/agentdesktop.go:220` |

### 1.2 引擎选择与切换

- 每台设备有一个状态文件 `engine`，缺省为 native。`xmesh computer engine agent-desktop` 写入该文件，重启 Host 后生效。`--engine` 参数优先于该文件。（`internal/computer/engine.go:22-55`，`cmd/xmesh/computer.go`）
- 切换只有 CLI，没有 UI 入口。我用 `engine` 加桌面相关词在 `ui/src` 中检索，没有结果。

### 1.3 进程模型与适配器

- `ExecRunner` 每次调用都启动一个子进程，`TMPDIR` 设为 `state/tmp`（0700），stdout 上限 16 MiB，stderr 丢弃。agent-desktop 本身没有长驻子进程；macOS 上的原生回退进程（见下一条）除外。（`internal/computer/agentdesktop.go:38-66`，`engine_factory.go:59-67`）
- macOS 上同时保留原生 `xmesh-computer`，作为后台坐标输入的回退。（`engine_factory.go:70-76`）
- 适配器 `AgentDesktop` 实现 `computer.Provider`（`internal/computer/types.go:169`），调用映射如下：

| Provider 方法 | agent-desktop 调用 | 位置 |
|---|---|---|
| Info | `permissions`、`list-displays` | `agentdesktop.go:219-279` |
| Capture | `screenshot --screen N <png>`，裁剪和缩放在 Go 中完成 | `agentdesktop.go:304-391` |
| Execute（前台） | `--headed mouse-move / mouse-click / mouse-wheel / drag`；按键和 ASCII 文本用 `press` 逐字发送 | `agentdesktop.go:540-600` |
| Snapshot | `snapshot -i --compact [--app] [--window-id] [--skeleton] [--root]` | `agentdesktop_refs.go:43-125` |
| RefAct | `click / right-click / … / scroll-to <@sid:eN> …`，不带 `--headed` | `agentdesktop_refs.go:128-175` |

- 信封只解析 `{ok, data, error}`，不检查 `version` 字段。只有 `disposition.delivery == not_delivered` 才报 failed，其余一律是 unknown。（`agentdesktop.go:82-100`，`464-469`）

### 1.4 工具怎样进入 MCP 与云端

**设备侧（Agent）**

- 工具表是手写的。`computerSpecs()`（`internal/role/agent/computer.go:126-279`）定义了名称、描述、必填字段和 schema，再由 `buildSpecs()` 合并（`internal/role/agent/specs.go:98-100`）。
- `installBuiltins` 把这些工具注册到每个 MCP 会话。`agent.computer.enabled=false` 时会从视图中移除 `computer.*`。（`internal/role/agent/agent.go:420-440`）
- 调用路径：`callComputer`（`internal/role/agent/computer.go:21-116`）→ `Manager`（`internal/computer/manager.go`）→ Provider。
- **缺口**：`computer.snapshot` 和 `computer.ref_act` 在任何引擎下都会被列出。原生引擎调用它们时，`refsReady` 返回 `refs_unsupported`（`manager.go:904-914`，调用点为 940、1019、1081）。也就是说，工具列表不随引擎能力变化。

**云端（Gateway）**

- Gateway 聚合各在线设备的工具表（`internal/role/gateway/surface.go:236-259`），再按 `gateway.tool_surface` 呈现：
  - `full`：每个设备的工具都带 `设备/` 前缀。
  - `unified`：每个工具只出现一次，可带可选的 `device` 参数。只列出 `ListedGroups` 中的组（默认 `fs/exec/pty/sys`）以及收藏的后端组，最多 40 个（`surface.go:37,40,275-352`）。不在列表里的工具仍可经 `tools.call` 调用。
  - `compact`：只有网关自己的工具和 `PinnedTools`。
- `computer` 不在默认的 `ListedGroups` 中，所以默认的 unified 模式下，桌面工具不会出现在 `tools/list` 里。
- 变化如何传播：Agent 用 RemoveTools/AddTool 热更新视图（`agent.go:425-429`，`backends.go:77,120`）。Gateway 每 10 秒，或收到 `Changed` 事件时，重新读取设备工具表，并通过 list_changed 通知客户端（`gateway.go` 的 discover 循环与 `refreshViews`）。

**授权与一致性**

- `internal/oauth/scopes.go:103-111` 硬编码了 computer 工具名单，scope 判定依赖它（`scopes.go:37`）。
- `ui/src/shared/builtin-tools.ts` 也列出了内建工具，文件注释说明它与配置中 `agent.disabled_tools` 的枚举保持一致。

### 1.5 今天新增一个桌面工具要改的地方

1. `internal/computer/`：Provider 方法和 Manager 方法，包括 refs 能力判断。
2. `internal/role/agent/computer.go`：`computerSpecs()` 中的定义，以及 `callComputer` 的分支。
3. `internal/oauth/scopes.go`：`computerBuiltin` 名单与 scope 映射。
4. `ui/src/shared/builtin-tools.ts` 与配置中 `agent.disabled_tools` 的枚举。
5. `docs/contracts/computer.md`，以及 `agentdesktop_test.go`、`manager_test.go` 等测试。

---

## 2. 上游 agent-desktop 的变化（1.0.2 → 1.1.0）

- **版本时间线**：1.0.0（10-28）、1.0.1（10-28）、1.0.2（10-30）、1.1.0（11-03）。
- **命令表**：两个版本完全相同，60 个命令名，其中 56 个可执行。
- **有意义的变化**：
  - macOS 的 CLI 模型：从“复用每个 snapshot 命名空间的私有 command host，保留原生 AX 对象，使被删除的行不会解析到同名的存活行”，改为“每次 CLI 调用一个进程”。这直接关系到我们的 `stale_ref` 语义（`agentdesktop_refs.go`），必须重新验证。
  - 信封版本 `2.5 → 2.6`。适配器目前不检查这个字段，应该读取并限制支持范围。
  - `open-system-surface` 在 macOS 上的支持（Dock 与菜单栏额外项）。我们没有使用。
  - C ABI `ad_init(6) → ad_init(7)`，只影响嵌入式库，我们不用。
- **npm 分发形态**：包内有一个 JS launcher `bin/agent-desktop`，以及六个平台的原生二进制和 helper，带 `.sha256` 侧车文件。postinstall 把原生二进制接到 bin 入口。如果安装脚本没有运行，每次调用都经过 Node launcher，README 说这会多约 125 ms。
  - 推断（需要 Mac 实测）：经过 Node 启动时，macOS 的权限主体可能不是 xmesh 签名的应用。所以 npm 全局的 shim 不能作为生产路径。`docs/validation/2026-11-01-agent-browser-desktop-p0.md:32` 已经记录了权限主体取决于“启动它的进程”这一点。

---

## 3. 目标与非目标

**目标**

- G1 可复现：每个 xmesh 发布包都带有确定版本和 SHA-256 的 agent-desktop，与既定计划一致。
- G2 可检测：随时能看出 installed、pinned、upstream latest 三个版本，以及是否已通过验收。
- G3 可控升级：手动（随发布或 PR）和自动（有门槛）两种方式，失败可回滚。
- G4 新工具默认不暴露：上游新出现的命令先进入“待映射”，映射和授权完成后才进入 MCP 与云端的列表。

**非目标**

- 远端（MCP 或云端）不能触发升级、切换引擎或修改路径，与 `allow_foreground` 一样只接受本机配置。
- 不从 CLI 帮助文本自动生成工具 schema，因为帮助文本无法表达风险等级和语义。
- 不直接跟随 npm 的 `latest`。

---

## 4. 设计

### 4.1 版本固定与随包分发（阶段一）

- **引擎清单**：新增一份清单，建议路径 `internal/computer/adengine/manifest.json`，用 `go:embed` 编进二进制。内容包括：`version`、每个平台的上游产物地址与 `sha256`、`license`（Apache-2.0），以及 `validated`（验收记录路径和通过的版本范围）。第一版钉在 `1.0.2`，也就是我们已经验证过的版本。
- **打包**：`scripts/package_computer.py` 增加 `--agent-desktop` 参数，把原生 `agent-desktop` 和它的 helper 放进 `xmesh-computer.app/Contents/MacOS/`，并随 app 一起签名；Windows 和 Linux 放在 xmesh 旁边。`ui/electron-builder.config.mjs` 的 extraResources 与 `release.yml`、`ui-package.yml` 同步修改。
- **启动校验**：Host 启动时对 agent-desktop 做 SHA-256 比对。不一致则拒绝启用该引擎，`reason: integrity_failed`，并回退到 native。
- **安装来源**：CI 从上游取得包后，直接提取原生二进制，用清单中的 SHA-256 校验，不运行上游的 postinstall。这与 agent-browser 安装器的 `--ignore-scripts` 做法一致。

### 4.2 更新检测

三个版本号：

- `installed`：`agent-desktop version` 的结果。Host 启动时读取一次，之后每 24 小时读取一次。
- `pinned`：清单中的版本。
- `upstream`：CI 每日查询 `npm view agent-desktop dist-tags.latest`，或 GitHub Release 的最新版本。

设备端不直接访问 npm，而是读取 xmesh 发布的“引擎公告”（结构与清单相同，随 release 或 Pages 发布）。这样 SHA-256 和验收状态都由我们把关。

状态机：

| 状态 | 条件 | 行为 |
|---|---|---|
| `current` | installed 等于 pinned，且 SHA-256 一致 | 正常运行 |
| `update_available` | 公告中有更新的、已验收的版本 | 提示，不自动应用 |
| `upstream_unvalidated` | 上游有新版本但未验收 | 只记录，由 CI 开 PR |
| `integrity_failed` | SHA-256 不符 | 拒绝启用，回退 native |
| `version_unsupported` | installed 不在验收范围内 | 不暴露映射工具，回退 native |

呈现方式：`computer.info` 新增 `engine`、`engine_version`、`engine_status`、`unmapped_commands`（见 4.5）；CLI 新增 `xmesh computer engine status`。

### 4.3 手动更新

- **默认路径（推荐）**：CI 每日运行上游检测。发现新版本时，自动开一个草稿 PR，更新清单（版本号、六个平台的 SHA-256）和 NOTICE，并运行 P0 验收矩阵。人工审核合并后，随下一次 xmesh 发布生效。这与计划中“更新时随 xmesh 一起升级，不要求用户自己 npm install”一致。
- **设备侧应急或灰度**：`xmesh computer engine update --to <version>`，只能在本机执行。流程：
  1. 从清单取得 URL 和 SHA-256；
  2. 下载到 `state/engines/agent-desktop/<version>/`，并校验；
  3. 原子切换 `state/engines/agent-desktop.active` 指针；
  4. 重启 Host；
  5. 健康检查：`version`、`permissions`、`list-displays`、一次截图；
  6. 失败则回滚到上一个版本。
- 不提供远程可触发的升级接口。

### 4.4 自动更新（可选，默认 `check`）

配置项：`agent.computer.engine.auto_update: off | check | apply`，默认 `check`，即只提示。

`apply` 的门槛，全部满足才应用：

- 只限补丁版本（同一 `major.minor`），且清单标记为已验收。
- Host 空闲：没有控制租约和观察会话，也没有暂停。
- 保留上一版本。健康检查失败，或连续失败两次后，停止自动更新并告警。
- 平台：Windows 和 Linux 可以自动应用。macOS 需要用户确认。原因是替换二进制会改变代码签名，可能让辅助功能和屏幕录制授权需要重新授予。`docs/contracts/computer.md:78` 已经说明切换引擎后需要另行授权。

供应链边界：自动应用只接受 xmesh 清单中带 SHA-256 的产物，绝不直接来自 npm 的 `latest`。

### 4.5 新工具的动态暴露（MCP 与云端）

核心原则：**映射清单是唯一的暴露来源，默认拒绝。**

**A. 映射清单**

在仓库中维护一份 `adcatalog`，每个上游命令一条记录：

- `tool`：xmesh 工具名，例如 `computer.find`，仍归入 `computer` 组；
- `upstream`：上游命令和参数模板；
- `input_schema`：由我们撰写；
- `risk`：`observe | pointer | keyboard | text | clipboard | app_control | system` 之一；
- `scope`：`computer:observe:<device>` 或 `computer:control:<device>`；剪贴板沿用已有的 `computer:clipboard-read:*`（见 `ui/src/renderer/lib/settings.ts:45-47`）；
- `modes`：支持 foreground、background、headless 中的哪些形式；
- `versions`：已验收的 agent-desktop 版本范围。

上游有但清单里没有的命令，只记入 `computer.info.unmapped_commands` 和升级报告，**不进入 MCP 或云端**。

**B. 运行期按能力过滤**

- `installBuiltins` 改为根据 `Info.Capabilities` 决定列出哪些 `computer.*` 工具。例如没有 `refs` 能力时，不列出 `computer.snapshot` 和 `computer.ref_act`。这也修复了原生引擎下列出却必然失败的问题。
- 引擎切换、升级或能力变化后，Agent 用 RemoveTools/AddTool 重建视图，沿用现有模式（`agent.go:420-440`）。
- Gateway 通过 10 秒轮询或 `Changed` 事件重新读取，并向客户端发送 list_changed，沿用现有机制。

**C. 授权与可见性**

- 新工具登记到 `computerBuiltin`（`scopes.go:103`）。
- 高风险类别（clipboard、app_control、system）默认放进 `agent.disabled_tools`，由本机配置显式打开。
- 云端可见性：默认 unified 模式不把 `computer` 加入 `ListedGroups`，保持现状。管理员可以用 `PinnedTools` 或 `ListedGroups` 显式开启。`tools.call` 始终可达，授权仍在调用时检查。
- 不新建工具组，避免改变 `waitsForOthers` 的语义（`surface.go:383-401`）。

**D. 版本门与降级**

Host 只有在 installed 版本落在验收范围内时，才暴露映射工具。超出范围时整体回退到 native。这与计划保留 native 作为回退的做法一致。

**E. 验收**

- 单元测试：用假 Runner 测试每个映射工具的参数翻译和错误映射，沿用 `agentdesktop_test.go` 的模式。
- 契约测试：未映射的命令不出现在 `tools/list` 中；缺少 scope 时拒绝调用。
- 真机：Mac 授权后，补测 1.1.0 的 ref 语义。当前 P0 记录中 Mac 的 snapshot 和 ref_act 仍未测（`docs/validation/2026-11-01-agent-browser-macos.md:31`）。

### 4.6 接口与契约改动清单（实现时）

- `docs/contracts/computer.md`：新增引擎状态字段、`unmapped_commands`、本机更新命令的契约；在实现完成前，修正第 68 行关于“固定版本”的描述。
- `internal/computer/types.go`：`Info` 新增 `engine`、`engine_version`、`engine_status`。
- `internal/role/agent/computer.go` 与 `specs.go`：按能力过滤。
- `internal/oauth/scopes.go`：`computerBuiltin` 与新工具同步。
- `ui/src/shared/builtin-tools.ts` 与配置 schema：同步新工具名。
- `scripts/package_computer.py`、`ui/electron-builder.config.mjs`、`.github/workflows/release.yml`、`ui-package.yml`：随包分发。
- 如果新增本机控制 API 端点：修改 `api/control/v2/types.go`，并用 `scripts/gen-control-ts` 重新生成 TS 类型。

---

## 5. 分阶段落地

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0 | 钉 1.0.2 + SHA-256 + 启动校验 + `engine status`（只读）；无 `refs` 能力时隐藏 snapshot 与 ref_act | 单测；Linux 假 Runner；现有 Mac 记录不变 |
| P1 | 清单驱动的工具映射 + scope 登记 + 随包分发（打包脚本与 UI 安装包） | 打包测试；契约测试；六平台归档包含引擎 |
| P2 | CI 上游监测与自动 PR；`engine update` 本机命令与回滚 | Linux 与 Windows 上完成升级和回滚演练；Mac 人工验收 |
| P3（可选） | 自动应用（非 macOS，或经用户确认）；默认仍为 `check` | 连续若干天无回滚后，再考虑把默认值改为 `apply` |

---

## 6. 风险

- 上游节奏快（6 天发了 4 个版本）。检测必须区分“已验收”和“仅已发布”，否则告警噪声很大。
- 1.1.0 的 macOS 进程模型变化，可能影响 ref 的 stale 语义，需要 Mac 实测。
- TCC 授权主体：替换二进制，或经 npm launcher 运行，都可能让授权落到错误的主体上（`docs/validation/2026-11-01-agent-browser-desktop-p0.md:32` 已指出该问题）。
- Windows 上 Notepad 快照曾经超时（`docs/validation/2026-11-01-agent-browser-windows.md:28`），本轮未重测。
- 上游 CLI 的参数语义（如 `--headed`）可能随版本变化。适配器的参数模板需要版本化。
- Host 作为用户服务运行，PATH 可能不含 node。经 npm shim（`#!/usr/bin/env node`）调用时会失败（推断，需验证）。

---

## 7. 尚未验证的事项

需要有权限的机器来完成：

- 运行 `agent-desktop version` 和 `agent-desktop <cmd> --help`，核对 1.0.2 与 1.1.0 的参数是否一致。本次沙箱拒绝执行下载的二进制，所以这一步没有做。
- 在 Mac 上用 1.1.0 跑 P0 矩阵中的 snapshot、ref_act、中文输入和拖拽。
- 在 Windows 和 Linux 上确认替换二进制后，Host 的行为是否正确。

---

## 8. 需要决定的事项（附推荐）

1. **版本**：继续钉 1.0.2，1.1.0 在 Mac 上验收后再切换。（推荐）
2. **自动更新默认值**：`check`。（推荐）
3. **云端默认可见性**：不把 `computer` 加入默认 unified 列表。（推荐）
4. **新工具范围**：按需求逐个映射，clipboard、app_control、system 默认关闭。（推荐）

---

## 附：证据

- 仓库：`git grep -i agent-desktop` 的结果只出现在 `internal/computer`、`cmd/xmesh/computer.go`、`internal/role/agent/computer.go` 和文档中；`scripts/`、`ui/`、`.github/` 中没有对 agent-desktop 的引用。
- 上游 npm 元数据：`dist-tags.latest = 1.1.0`；`time`：1.0.0 为 2026-10-28T01:16Z，1.0.2 为 2026-10-30T21:18Z，1.1.0 为 2026-11-03T04:15Z。
- 上游包：1.0.2 与 1.1.0 的 `package.json` 均为 Apache-2.0；两者都带六个平台的原生二进制和 `.sha256` 侧车文件。
