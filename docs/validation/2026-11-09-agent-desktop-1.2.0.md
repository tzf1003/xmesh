# agent-desktop 1.2.0 动作验收记录（部分完成）

日期：2026-11-09
设备：Mac（macOS 27.2）。测试对象是 agent-desktop 1.2.0，安装在测试目录 `xmesh-ad-test/1.2.0` 下。
测试窗口：Finder 窗口 `xmesh-ad-test`。所有动作只针对该窗口或窗口内的元素。
固定版本：未改动，仍为 1.0.2。1.2.0 没有加入 `validated_versions`。

## 结论

这次只完成了一部分验收，不能据此把 1.2.0 视为已通过。

- 观察、无头动作和部分物理动作有结果。
- 多数物理动作的回执是 `delivered_unverified`。这表示引擎报告已投递，但没有独立观察确认效果。
- 复位点击失败后，物理输入停止。失败时指针没有到位，前台应用已切到 Google Chrome。原因未确认，可能有人在使用这台 Mac。

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
| type | --headed | 拒绝，ACTION_NOT_SUPPORTED | 投递状态 unknown。随后检查，搜索框为空，列表没有残留文字 |
| clear | 无头 | 拒绝，ACTION_NOT_SUPPORTED | 无语义写入动作 |
| set-value | 无头 | 拒绝，ACTION_NOT_SUPPORTED | 同上 |
| press escape | 无头，指定窗口 | 拒绝，STALE_REF，未投递 | 焦点未确认，按失败处理 |
| press escape | --headed | 成功，delivered_unverified | 按键投递到当时的前台应用，效果未验证 |
| hover 列表 | --headed | 成功，delivered_verified | 首次因引用过期被拒绝（STALE_REF，未投递）。重新快照后成功，指针位置经验证，约为 (613.5, 349) |
| mouse-move | --headed | 成功，delivered_verified | 指针到 (600, 400)，经验证 |
| mouse-click | --headed | 成功，delivered_unverified | 空白处点击，效果未验证 |
| mouse-wheel | --headed | 成功，delivered_unverified | 空白处滚轮，效果未验证 |
| drag | --headed | 成功，delivered_unverified | 从空白处框选，`tmp` 行出现 selected 状态，有旁证 |
| mouse-click 复位 | --headed | 失败，ACTION_FAILED | 指针未到位：观察到 (611.7, 559.8)，请求 (600, 400)。之后没有再发送物理输入 |

## 安全与环境

- 测试目录检查：`1.2.0`、`results`、`tmp` 三个文件夹仍在原位，名称未变。废纸篓里没有今天新增的项目。
- 测试窗口曾被 `GestaltX-main` 窗口遮挡，导致一次 headed click 被命中测试拒绝。之后测试窗口成为焦点窗口，hover 的命中测试通过。
- 物理输入只在测试窗口内的空白区域进行。没有拖放文件，也没有使用 Delete 键。
- 一次无窗口指定的 `press escape` 把按键投递到了前台应用。按键本身没有副作用，但这说明无头按键前必须先确认焦点。

## 未完成

- 物理 click 与 type 的效果验证：需要窗口稳定、位于最前、没有遮挡。
- 取消 `tmp` 行的框选。复位点击失败，之后没有再发送物理输入。
- 确认焦点后的物理 press 验证。
- 是否把 1.2.0 加入 `validated_versions`：待以上项目完成后人工决定。升级固定版本需要另外的 PR。
