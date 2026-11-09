# agent-desktop 1.2.0 动作验收记录

日期：2026-11-09
设备：Mac（macOS 27.2，arm64，平台键 `darwin-arm64`）。测试对象是 agent-desktop 1.2.0，安装在测试目录 `xmesh-ad-test/1.2.0` 下。
测试窗口：Finder 窗口 `xmesh-ad-test`。所有动作只针对该窗口或窗口内的元素。
固定版本：未改动，仍为 1.0.2。通过后只把 1.2.0 加入 `validation.validated_versions`，`validated_platforms` 保持 `["darwin-arm64"]`。
1.0.2 的安装与 CLI 版本验收仍见 `docs/validation/2026-11-01-agent-browser-desktop-p0.md`。本记录覆盖 1.2.0 的动作验收，并作为 `validation.record` 指向的记录。

## 结论

Mac（darwin-arm64）上的动作验收计划已全部完成，结论为通过，范围只限 darwin-arm64。

- 观察、无头动作和物理动作都有结果。物理动作的引擎回执多数仍是 `delivered_unverified`，因此每项都用独立的效果观察补充验证：搜索框的值、窗口标题、行的 selected 状态、列表位置和滚动条的值。
- 物理 type：焦点在搜索框 textfield 上时输入 `xmesh`，得到 `delivered_verified`，值已读回。
- 物理 click：results 行点击后变为 selected，空白处点击后取消选择，两次都已验证。
- 物理滚轮：列表滚动已验证。帮助文本的方向与观察相反，见下方“滚轮方向”。
- 失败和拒绝都保留在下表中。ACTION_FAILED、POLICY_DENIED、ACTION_NOT_SUPPORTED 都是引擎的拒绝，没有产生副作用。
- 本次验收不改变运行时版本。固定版本仍是 1.0.2，升级固定版本需要另外的 PR。

## 滚轮方向

`agent-desktop mouse-wheel --help` 写的是“正数向上，负数向下”。这台 Mac 上的观察相反：

- 列表在顶部时，`--dy=-5` 与 `--dy=-15` 都没有变化：首行 y 保持 177，滚动条 value 保持 0。
- `--dy=15` 让列表显示后面的行：首行 y 从 177 变为 -193，滚动条 value 从 0 变为 0.598。

这台 Mac 的 `com.apple.swipescrolldirection` 为 1，即开启了自然滚动。这可能是原因，但没有进一步确认。xmesh 的 wrapper 把 `delta_y` 直接作为 `--dy` 传入，契约写的是“正值向右或向下”，与这里的纵向观察一致。自然滚动关闭时的表现没有测试。帮助文本与观察不符，需要反馈给上游。

## 结果

| 命令 | 方式 | 结果 | 说明 |
|---|---|---|---|
| list-windows | 观察 | 成功 | 首次返回 TIMEOUT，窗口清单不稳定。之后正常 |
| focus-window | 窗口焦点 | TIMEOUT，投递状态 unknown | 之后测试窗口成为焦点窗口（is_focused） |
| snapshot | 观察 | 成功 | 能读到测试窗口的列表和搜索框 |
| get value | 观察 | 成功 | 搜索框的值为 null |
| click 搜索框 | 无头 | 成功，delivered_unverified | AXPress 成功，效果未验证 |
| click 搜索框 | --headed | 拒绝，ACTION_FAILED | 命中测试判定受遮挡（receives_events），未投递 |
| type | 无头 | 拒绝，POLICY_DENIED | 搜索框没有 SetValue，焦点回退被拒绝 |
| type（目标为搜索框内的“搜索”按钮） | --headed | 拒绝，ACTION_NOT_SUPPORTED，not_delivered | 按钮不可编辑，没有文字进入搜索框。随后检查，搜索框为空，列表没有残留文字 |
| clear | 无头 | 拒绝，ACTION_NOT_SUPPORTED | 无语义写入动作 |
| set-value | 无头 | 拒绝，ACTION_NOT_SUPPORTED | 同上 |
| press escape | 无头，指定窗口 | 拒绝，STALE_REF，未投递 | 焦点未确认，按失败处理 |
| press escape | --headed | 成功，delivered_unverified | 按键投递到当时的前台应用，效果未验证 |
| hover 列表 | --headed | 成功，delivered_verified | 首次因引用过期被拒绝（STALE_REF，未投递）。重新快照后成功，指针位置经验证，约为 (613.5, 349) |
| mouse-move | --headed | 成功，delivered_verified | 指针到 (600, 400)，经验证 |
| mouse-click | --headed | 成功，delivered_unverified | 空白处点击，效果未在该步骤验证 |
| mouse-wheel | --headed | 成功，delivered_unverified | 空白处滚轮。当时文件夹没有可滚动内容，效果未验证。后续滚动见下方 |
| drag | --headed | 成功，delivered_unverified | 从空白处框选，`tmp` 行出现 selected 状态，有旁证 |
| mouse-click 复位 | --headed | 失败，ACTION_FAILED | 指针未到位：观察到 (611.7, 559.8)，请求 (600, 400)。引擎拒绝了这次点击。之后重新移动指针再点击 |
| press cmd+a | --headed | 成功，delivered_unverified；行状态已验证 | 测试窗口获得焦点、前台为 Finder 后发送。三行均变为 selected |
| mouse-click 取消框选 | --headed | 成功，delivered_unverified；行状态已验证 | 空白处点击后三行恢复为 focused，没有 selected |
| focus（搜索框） | 无头 | 成功，delivered_verified | AXFocused，用于确认按键目标 |
| press cmd+f | --headed | 成功，delivered_unverified；窗口标题已验证 | 窗口进入搜索模式，标题变为“正在搜索“这台Mac””（搜索范围为这台 Mac） |
| type（目标为搜索框 textfield，状态 focused） | --headed | 成功，delivered_verified | post_state 的 value 为 `xmesh` |
| press escape（三次） | --headed | 成功投递，delivered_unverified；未退出搜索 | 三次 Escape 后窗口仍在搜索模式，这是 Finder 的行为，不是引擎故障。最后一次前搜索框已确认有焦点 |
| click “返回” | 无头 | 成功，delivered_verified | AXPress，退出搜索模式，窗口标题恢复为 xmesh-ad-test。之后搜索框值为 null |
| click results 行（第一次） | --headed | 成功，delivered_unverified；选中状态已验证 | 点击前 `is --property selected` 为 false，点击后为 true |
| mouse-move (600, 400) | --headed | 成功，delivered_verified | 指针位置经验证 |
| mouse-click 空白处 (600, 400) | --headed | 成功，delivered_unverified；取消选择已验证 | 点击后 results 的 selected 为 false，列表没有 selected 状态 |
| click results 行（第二次） | --headed | 成功，delivered_unverified；选中状态已验证 | 为进入文件夹做准备 |
| press cmd+down | --headed | 成功，delivered_unverified；标题已验证 | 打开 results，标题为 results，共 48 项 |
| mouse-wheel (600, 400) dy=-5 | --headed | 成功投递，delivered_unverified；无变化 | 列表在顶部，首行 y 保持 177 |
| mouse-wheel (600, 400) dy=-15 | --headed | 成功投递，delivered_unverified；无变化 | 同上，滚动条 value 保持 0 |
| mouse-wheel (600, 450) dy=15 | --headed | 成功投递，delivered_unverified；滚动已验证 | 首行 y 从 177 变为 -193，滚动条 value 从 0 变为 0.598 |
| press cmd+up | --headed | 成功，delivered_unverified；标题已验证 | 回到 xmesh-ad-test。Finder 自动选中 results，随后用空白处点击取消 |
| mouse-move + mouse-click 空白处 (600, 400) | --headed | 成功，取消选择已验证 | 三个文件夹均无 selected 状态 |

## 安全与环境

- 测试目录：结束时 `xmesh-ad-test` 顶层为 `.DS_Store`、`1.2.0`、`results`、`tmp`。`1.2.0` 有 3 项，`results` 有 48 项，`tmp` 为空。本次没有执行新建、移动、重命名或删除。`.DS_Store` 是 Finder 的隐藏元数据。
- 废纸篓：今天有修改时间的项目只有 `.DS_Store`，没有新增被删的测试项目。
- 焦点：每次按键或指针动作前，都先确认前台应用为 Finder，且测试窗口为焦点窗口。前台切到 Chrome 时没有发送任何输入。物理动作只在用户确认可以继续后进行。
- 搜索：输入 `xmesh` 时搜索范围是“这台 Mac”，窗口里短暂显示了全盘中含 `xmesh` 的匹配项。这些文件没有被打开、选中或读取。
- 指针：最后一次物理动作把指针停在 (600, 400)，即测试窗口的空白区域。
- 前一版记录中的拉回前台操作失误，本次没有重复。

## 未完成

本次验收计划没有未完成项。以下内容不在本次范围内：

- Windows 与 Linux 没有测试。`validated_platforms` 只保留 `darwin-arm64`。
- 自然滚动关闭时的滚轮方向没有测试。
- 固定版本仍为 1.0.2。升级固定版本需要另外的 PR。
- 滚轮帮助文本的方向需要反馈给上游。
