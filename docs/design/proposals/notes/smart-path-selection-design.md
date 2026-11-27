# 智能選路設計方案（待確認，尚未改任何代碼）

## 現狀（已讀代碼）
- 路徑種類：lan / tailnet / cloudflare（internal/device/directory.go）。
- 選路是寫死的：internal/devicelink/manager.go 的 connect() 依序 LAN(2s) → Tailnet(5s) → Cloudflare(15s)；同一種類內按地址字串排序（comparePath）。
- 探測：internal/connectivity/node.go 的 probe() 每個候選發一次 GET identity，只記成功/失敗與 30s 有效期；沒有延時、沒有歷史、沒有丟包統計。
- 失敗只有指數退避冷卻（最長 30s），沒有長期穩定性概念。這正是 GFW 隨機阻斷時會「偶爾通、偶爾斷」卻一直被優先選中的原因。

## 一、數據模型
Path 新增（只在本機記憶體，可選持久化）：
- rtt_ms：最近有效 RTT 的 EWMA（α≈0.3）與 p50/p95。
- loss：滑動窗口（最近 N=20 次探測或 5 分鐘）的探測失敗率。
- flaps：窗口內「已建立連線被斷開」次數（比探測失敗更重的信號）。
- last_ok / last_fail、samples（樣本數，過少時分數不可信）。
UI 每個地址後顯示：`42 ms · 穩定 98%`，不穩定標紅，無樣本顯示 `—`。

## 二、探測
- 現有 probe 順帶計時（TTFB 即 RTT），不增加新協議。
- 探測頻率：在線連線的對端，每個候選路徑每 10–15s 一次（Cloudflare 30s 以免太貴）；未使用的備選路徑也要探，否則永遠不知道它是否更好。
- 活連線上的 ping/斷線事件記入 flaps。
- 成功率窗口需要至少 5 個樣本才參與評分，之前按「手動優先級／默認種類順序」。

## 三、智能模式評分（分數越低越好）
score = rtt_ewma + loss_penalty + flap_penalty + kind_bias
- loss_penalty = 丟包率 × 2000 ms（丟 10% ≈ +200ms；丟包比延時重要得多，因為 TCP/WebSocket 在丟包下重傳會放大尾延時）。
- flap_penalty = 窗口內斷連次數 × 300 ms，上限 3000，隨時間衰減（半衰期 5 分鐘）。
- kind_bias：LAN −50ms，Tailnet 0，Cloudflare +30ms（平局時偏向直連、少依賴第三方）。
- 用 p95 而非平均值做 RTT 項，可顯式懲罰抖動：rtt_term = 0.5·ewma + 0.5·p95。
- 硬淘汰：連續 3 次探測失敗或最近 30s 內斷連的路徑暫時排除（但仍探測，恢復後重新入選）。

## 四、防抖（避免來回切換）
- 滯後：只有新路徑分數比當前路徑低 ≥25% 且 ≥30ms，並持續 ≥ 30s（連續 3 次探測都更優）才切換。
- 當前路徑變壞（硬淘汰條件）則立即切換，不等滯後。
- 切換用現有「新連線建立後再關舊連線」的邏輯（make-before-break），避免正在執行的會話中斷；先確認 devicelink 是否支持並行 carrier（目前 p.active 單個，需要看一下仲裁）。
- 切換事件寫入審計日誌（internal/connectivity/audit.go）。

## 五、手動模式
- 每台設備一個有序列表 [lan, tailnet, cloudflare]（或到具體地址），用戶可拖拽調整，可禁用某路徑。
- 按順序嘗試，第一個可達的勝出；不做自動重排。可選「手動優先 + 失敗時回退到智能」開關。
- 全局默認模式 + 每台設備可覆蓋。

## 六、存放與 API/UI
- 配置：internal/config 的 mesh 下增加 path_policy: { mode: smart|manual, order: [...], disabled: [...] }；按 device_id 覆蓋。
- 控制 API：GET 設備列表的 paths 增加 rtt_ms/loss/flaps/score/selected 字段；新增 PUT 設置 path_policy。
- UI（設備頁）：地址行後面顯示延時與穩定性；頂部模式切換（智能/手動）；手動時顯示可拖動排序。
- 現有 #146 在改地址列表（Tunnel host 顯示），本功能的 UI 部分應等 #146 合併後再做，避免衝突。

## 七、實施分期（每期一個 PR）
1. 指標採集：probe 計時 + 滑動窗口統計 + API 暴露 + UI 顯示延時/穩定性（不改選路行為，風險最低，用戶馬上能看到數據）。
2. 策略引擎：評分與防抖、config 的 path_policy、manager.connect 改用策略排序（默認仍為現有順序，智能模式需開啟）。
3. 手動優先級 UI 與 API；智能模式默認化。
4. 測試：評分/防抖的純函數單元測試（表驅動，注入時鐘），模擬 GFW 場景（Tailnet RTT 低但 15% 丟包 vs Cloudflare RTT 高但穩定 → 選 Cloudflare）。

## 待用戶確認的問題
1. 評分權重（丟包 ×2000ms、斷連 300ms）是否接受？可後續調。
2. 切換防抖策略（25%/30s）是否接受？
3. 手動模式是否要「失敗時回退智能」？
4. 先從第 1 期（只加延時/穩定性顯示）開始是否可以？
