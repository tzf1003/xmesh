# Computer 工具与桌面执行器

状态：实现中。交付与真机验收记录见 [三端方案](../design/19-computer.md)。

## 会话

工具由目标 Agent 注册，只有 `agent.computer.enabled: true` 时才出现在目录中。工具名经过 Gateway 后带设备前缀。调用方只能从网络 / MCP 会话身份推导，参数里的身份字段不授予权限。

| 工具 | 必填参数 | 语义 |
| --- | --- | --- |
| `computer.info` | 无 | 协议版本、可用状态、桌面代次、显示器、能力 |
| `computer.open` | `mode` | observe 或 control；可选 desktop_id、ttl_seconds |
| `computer.observe` | session_id、desktop_epoch | 当前截图，可选 display_id、max_width、region |
| `computer.act` | session_id、desktop_epoch、lease_token、action_id、actions | 最多八个有限动作，可选 frame_id、observe_after |
| `computer.action_status` | session_id、desktop_epoch、action_id | 查询当前会话中已接收的动作 |
| `computer.renew` | session_id、desktop_epoch | 控制会话还须匹配 lease_token；可选 ttl_seconds |
| `computer.close` | session_id、desktop_epoch | 控制会话还须匹配 lease_token；释放输入 |
| `computer.sessions` | 无 | 仅调用方拥有的会话 |

租期默认 120 秒，范围 10–600 秒；观察会话也需要续期。会话上限默认 16，配置范围 1–32。每个会话最多缓存四张图和 128 个动作结果，全部会话共享 64 MiB 的截图缓存预算，超过预算按采集时间淘汰旧图。已过期会话立即拒绝；内核断开 caller、禁用 computer 或 kill drain 时撤销其会话。本机暂停只回收控制租约，观察会话保持有效。新的控制会话获得新的 fencing 序号。

## 画面与动作

截图的 `content` 包含文本元数据和原生 MCP 图片块，`structuredContent` 包含同一份元数据，图片字节不会复制到结构化对象中。frame_id 只在所属会话中有效；坐标动作使用 60 秒内的截图。桌面代次或布局改变后，旧画面不能用于输入。

`capture_region` 是相对于显示器左上角的系统桌面坐标，macOS 为逻辑点，Windows / X11 为后端使用的桌面像素。模型提交图片像素坐标；转换公式为 `display_origin + capture_region_origin + image_coordinate × capture_region_size / image_size`，坐标转换在服务端完成。返回图片和显示器本身都已按当前旋转方向排列。

动作类型：move、click、drag、scroll、key、text、wait。点击的 button 为 left / middle / right，clicks 最大 3；拖动最多 2 秒；每批显式等待总预算最多 4 秒，执行和排队使用五秒总预算。按键组合在同一动作中按顺序按下、反向释放。文本使用系统文字输入，正文上限 64 KiB。键盘与文本行为不借用剪贴板授权。

MCP 发现返回的 schema 明确列出 mode、action type 和 button 枚举、租期 / 坐标 / 时间预算，以及不同动作的必填字段；工具描述包含图片坐标、业务结果观察和 unknown 恢复语义。滚轮 delta 为离散步数，正值向右 / 向下。

动作 ID 与请求内容绑定。在有效会话内，相同 ID 和相同参数返回既有结果，相同 ID 配不同参数返回 conflict。请求响应丢失后查询 action_status。executed 表示全部输入动作已提交，业务成功仍须观察确认。failed 表示已知失败，executed 数量指出完整执行的前缀；unknown 表示执行结果不能确定，应重新观察。幂等缓存为内存状态，执行器 / 内核重建后桌面代次变化，旧动作不得重放。

## 授权和错误

OAuth / PAT scope 按设备区分 `computer:observe:<device>` 和 `computer:control:<device>`；control 包含观察。open 的 control 模式还在参数级检查 control scope 和 Agent 的 computer.act tag 授权。剪贴板 scope 单独保留，不授予截图或输入权限。

Computer 错误在共享错误信封中带 `resource: computer:<name>`。desktop_unavailable 表示执行器 / 图形会话不可用，desktop_locked 表示锁屏，permission_required 表示本机系统权限缺失。conflict 携带 reason 表达 desktop_epoch、layout_changed、stale_frame、action_id_reused。其余 not_owner、session_not_found、lease_required、resource_busy 沿用共享语义。Computer 的版本冲突使用桌面 reason，文件冲突仍使用文件 hash 和 mtime。

审计记录动作 ID、会话 ID、参数键和字节数。输入正文、图片和其可猜测摘要不写入审计。完整桌面控制具有用户级 GUI 权限；文件路径策略不会自动约束 GUI。Ingress 处理工具结果，是截图数据的信任边界。

`agent.computer.pause_on_user_input` 默认关闭。启用时，控制会话绑定原生执行器报告的本机输入序号；检测到其他输入来源的点击或按键后，输入执行和下一次状态检查会撤销控制并暂停。执行器的标记用于区别自己产生的事件，同用户进程不属于独立安全边界。`user_input_monitor` 为 false 时，启用该策略的控制申请返回 permission_required；不会默默放行。Wayland compositor 尚未提供全局输入监测时可使用本机接管按钮和暂停快捷键。

将该策略从关闭改为启用会立即回收已有控制租约；调用方重新打开控制会话后绑定当前输入基线。本机状态读取完成桌面状态复核后才列出会话，因此同一次响应反映权限撤销及本机输入暂停。

## 本机执行器通道

`xmesh computer host` 在图形用户会话中运行，使用用户专用 Unix socket 或 Windows 命名管道接入目标内核，并通过匿名继承管道监督 Rust 原生执行器。此 host 不承载 Agent / Gateway / Ingress，也不接入 mesh。Windows SYSTEM 内核通过配置的用户 SID 管道连接图形会话。

Unix 双方检查 peer uid，Windows 管道具有 DACL，客户端核对管道服务器进程 token 的 SID 与管道名中目标 SID 一致。每个用户桌面执行器还有排他文件锁。一条 broker 只绑定一个管理内核；内核以独立标识每两秒心跳，超过六秒没有心跳时关闭执行器并释放绑定。重建后原生执行器重新生成 desktop_epoch。

本机 HTTP 请求仅使用受保护的本机传输，路径为 /v1/info、heartbeat、capture、execute、release、disconnect。原生匿名管道为每行一个 JSON 对象：请求包含 id、method、params，响应包含 id 和 result 或 error。协议版本为 1；请求限 1 MiB，响应限 8 MiB。外部 Agent 只看到 MCP。

原生管道写入和等待均受上下文限制；执行器停止读取导致写入超时后，监督进程终止该执行器，后续调用经新的桌面代次重新建立权限。

`xmesh computer status` 查询本机 Host 的运行状态、PID、版本和原生子进程 PID；该查询不启动执行器、不取得内核绑定。服务不存在时返回 running=false，身份检查与协议错误仍返回失败。`--state-dir` 在 Unix 上选择对应用户端点，Windows 按用户 SID 定位管道。

面板桌面预览经当前 Gateway 调用同一组 MCP computer 工具，按所选设备的观察权限获取图片；每次预览创建十秒观察租约并在获取画面后关闭。手动请求或用户开启的两秒刷新均串行执行；隐藏页面暂停采集，切换节点、设备、显示器、连接代次或离开页面时取消请求并清除画面。预览不写入调用历史或持久存储，保留画面采集时间。截图和输入仍共享目标 Agent 的授权与会话管理。

原生采集限制单帧最多 32 Mi 像素；在分配应用侧原始像素缓冲之前检查，覆盖 Windows、macOS、X11 和 PipeWire 路径。当前上限包含 7680×4320 的单显示器画面。

`scripts/accept_computer.py --gateway 127.0.0.1:<端口> --device <目标> --token-file <本机 PAT 文件>` 对已授权的真实目标进行验收：连续两张原生 MCP 图像、有限 wait 控制动作、相同动作 ID 去重、状态查询、续租和关闭。脚本仅输出图片大小及尺寸，不保存截图或令牌；点击、文本、拖动等输入仍由隔离窗口夹具验收。

桌面 UI 更新在替换安装包之前停止运行中的 Host，并确认 Host 与已知原生子进程均退出。更新记录保存是否需要恢复 Host；新应用先恢复 Host 并核对版本，再启动节点。安装交接失败时恢复已停止的 Host；恢复失败记为更新验收失败。未运行的 Host 保持停止。

本机显式执行 `xmesh computer host --authorize` 请求系统权限。普通远程请求遇到未授权状态直接失败。权限初始化和自动启动仍按三端验收清单逐项验证。
