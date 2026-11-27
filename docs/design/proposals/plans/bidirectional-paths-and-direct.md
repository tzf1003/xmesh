# 双向最优连接与直连途径：实现计划（定稿 2026-10-27）

完整方案页面：https://claude.ai/artifact/TPJhivrZrX1rjGwxg86kYS
依据代码：tzf1003/xmesh main 184e6a3。项目规则：改代码、推送、开 PR 前先问用户。

## 目标（用户已确认）
- 任一途径连上即可双向调用工具，与谁是服务端/客户端无关。
- 每个方向各走自己测得最好的路线：A 调用 B 走 A 拨出的连接，B 调用 A 走 B 拨出的连接。两端各自决定，互不干预。
- 所有途径同时测量，双方界面互相显示两个方向的质量，每条连接标出本机角色（客户端=拨出 / 服务端=被拨）。
- 每对设备最多 2 条连接；第二条按需建立，空闲 5 分钟关闭；备选路径只探测不连接。
- 新增“直连途径” `direct`（第四种 PathKind），不嵌入 FRP。端口映射本次不做。

## 现状要点（代码确认）
- carrier 双向 mTLS WebSocket，逻辑会话两端可开可接，断线按序号续传：devicelink/carrier.go、session.go。
- 每对设备一条活动连接 `p.active`，设备 ID 小的一端确认 epoch：manager.go handshake。
- 只有拨号端 review/切换：smart.go reviewLocked（`!c.outgoing` 直接返回）。
- 探测：connectivity/node.go probe，每次新建 TCP+TLS 发 GET identity；LAN/Tailnet 约 2s（超时 2s），Cloudflare 30s（超时 10s）。只测本机拨出方向。
- 统计：device/pathstats.go 5 分钟窗口 ≤200 样本，≥5 才打分；pathscore.go 评分公式。
- 心跳 5s、10s unstable、DeadAfter 30s、RecoveryWindow 120s（manager.go 默认）。心跳 RTT 未记入路径质量。
- 失败退避 1..30s：connectPaths。

## 分期（每期一个 PR）
1. 测量改造（不改选路行为）
   - 心跳 RTT/超时写入活动路径的 pathStats；活动路径停止额外探测。
   - 备选路径探测降频：LAN/Tailnet/direct 10–15s，Cloudflare 30s。
   - 质量记录带方向（本机拨出）。
   - 新控制帧：每 ~15s 互发本机拨出路径质量摘要，对端缓存带过期，只用于显示；带能力协商，旧对端不发不收。
   - 控制接口：设备路径增加 direction 与对端质量字段。
2. 双槽连接模型（核心，依赖 1 的能力协商）
   - handshake/epoch 按拨号方分两个槽（本机拨出 / 对端拨出），互不阻塞；去掉双槽下的 yieldToPreferred。
   - 逻辑会话按发起方绑定槽；本槽无连接时借用另一槽，本槽连上后新会话走本槽，旧会话空闲时迁回。
   - 第二条按需拨、空闲 5 分钟关闭。
   - 心跳连续 2–3 次无回应即提前拨备选；同槽内新连接提交后再关旧连接（交接）。
   - 测试：两端同时拨、一槽断另一槽在、借用与迁回、提前切换交接、provider 换代、RecoveryWindow 边界。
3. 直连途径本体（与 1 无依赖，可并行）
   - PathKind `direct`：device.PathKind.Valid、config.PathKinds 与校验、connectivity.pathKinds、providerEnabled/providerSame、runProvider、devicelink.dialKind、kindBias（0）、PathSelection priority/disabled。
   - 配置 `mesh.direct`：enabled（默认 false）、listen（默认 0.0.0.0:7435）、advertise（公网地址/域名）、peers（设备 ID → host:port）、allow_pairing（默认 false，仅邀请期放行，参照 Cloudflare 的 serve 严格 TLS 分支）。
   - 服务端严格 mTLS（trust.TLS(identity,"",true)）；配对时输入的地址写入 peers。
   - 默认顺序 lan, tailnet, direct, cloudflare。
4. 界面（依赖 1–3 的接口字段）
   - 设备详情：两条连接与角色标签；路径质量分“本机拨出 / 对端拨出”两栏（对端数据过期变灰）。
   - 网络页“直连”卡片；配对页填服务端地址；PathPriority 加直连；拓扑与审计显示方向与切换原因。
5. 加固与文档
   - 预认证并发上限、按源 IP 限速与失败退避、可选 IP 白名单。
   - 更新 docs/design/19-device-connectivity.md 与控制接口契约。

## 依赖关系
1 和 3 可并行；2 依赖 1；4 依赖 1–3；5 最后。
