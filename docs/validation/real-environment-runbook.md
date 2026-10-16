# 真实环境验收步骤（面向无需读代码的操作者）

本页把仓库里还缺的真实环境验收拆成独立小步骤。每一项都可以单独做，做完把“交回什么”里的内容贴回即可；做不到某一项就跳过它，其余不受影响。凭据（注册 key、OAuth 口令、PAT）只放在本机文件里，不要贴到聊天里。

| 步骤 | 验什么 | 需要什么 | 大致工作量 |
| --- | --- | --- | --- |
| [0](#0-取得-beta-并验证) | 拿到 beta 二进制并校验 | 任一台电脑 | 5 分钟 |
| [A](#a-三台物理设备在同一局域网互联) | 三台物理设备 LAN 互联 | 三台电脑，同一路由器 | 30 分钟 |
| [B](#b-claudeai-或-codex-的真实-oauth-与工具可见性) | claude.ai 或 Codex 的真实 OAuth 与工具可见性 | Tailscale 账号、Funnel、claude.ai 或 Codex | 40 分钟 |
| [C](#c-公共域名反向隧道可选) | 公共域名或反向隧道（可选） | 自有域名或隧道服务 | 30 分钟 |
| [D](#d-云沙箱出站与入网) | 云沙箱出站与入网 | Codex Cloud 或 Claude 云沙箱 | 20 分钟 |
| [E](#e-跨主机入口-pty-总预算) | 跨主机入口 PTY 总预算 | 两台入口主机加一台常驻 Agent | 40 分钟 |
| [F](#f-windows-普通用户登录会话) | Windows 普通用户登录会话；L2 应被拒绝 | 一台 Windows，加一个第二用户 | 30 分钟 |
| [G](#g-claude-code-连接观察) | Claude Code 连接本机 Gateway | 装了 Claude Code 的电脑 | 10 分钟 |

## 0. 取得 beta 并验证

beta 是 GitHub 预发布版（Releases 页，标签形如 `v0.1.0-beta.1`），为 Linux、macOS、Windows 各提供 `amd64` 与 `arm64` 的归档，另有 `SHA256SUMS.txt`。每个归档解压后是一个目录，里面有 `xmesh`（Windows 为 `xmesh.exe`）、本页用到的 `scripts/` 和本页本身（`docs/validation/`），不需要再克隆仓库。需要 Python 3.8 以上（只用标准库）。

1. 下载本机系统对应的归档和 `SHA256SUMS.txt`，放进同一个文件夹。
2. 校验：
   - Linux：`sha256sum --ignore-missing -c SHA256SUMS.txt`
   - macOS：`shasum -a 256 --ignore-missing -c SHA256SUMS.txt`
   - Windows（PowerShell）：`(Get-FileHash .\xmesh-<标签>-windows-amd64.zip).Hash` 应与 `SHA256SUMS.txt` 里该文件那一行相同（忽略大小写）。
3. 解压并进入目录。二进制未签名：macOS 先执行 `xattr -d com.apple.quarantine xmesh`；Windows 若出现 SmartScreen，选“更多信息”再“仍要运行”。
4. 执行 `./xmesh version`（Windows 为 `.\xmesh.exe version`），输出应为 `xmesh <标签>`。

**交回：** 第 2 步的校验结果（通过或不通过）、第 4 步的输出、系统与架构。

### 本页的约定

- 命令按 Linux/macOS 书写。Windows（PowerShell）把 `python3` 换成 `py -3`，`./xmesh` 换成 `.\xmesh.exe`，路径里的 `/` 换成 `\`，`~/` 换成 `$HOME\`。
- 所有节点的文件放在 `~/xmesh-accept/<节点名>/` 下：`config.json`、`state/`（状态目录）、`work/`（Agent 唯一允许操作的目录）。下面用 `ACC=~/xmesh-accept` 指代这个目录；Windows 写 `$ACC = "$HOME\xmesh-accept"`。
- 配置由 `scripts/accept_config.py` 生成，不用手写。它只引用密钥文件的路径，不把密钥写进配置。需要口令的文件（OAuth 口令、PAT）缺失时自动生成随机值，权限 `0600`，不会打印。
- 要让另一台机器上的 Windows 设备被 `exec.run` 验证，那台 Windows 上须装有 PowerShell 7（命令 `pwsh` 在 PATH 中），验证脚本用它执行命令。
- 每一步结束都可以用 `scripts/accept_collect.py` 打包要交回的内容（版本、状态、审计里的工具名与结果，不含路径与参数；已去除形如注册 key 与 Bearer 的内容）。发出之前仍请自己看一遍：

```bash
python3 scripts/accept_collect.py --binary ./xmesh --state-dir $ACC/<节点名>/state \
  --endpoint http://127.0.0.1:8080/mcp --out $ACC/evidence-<步骤>-<节点名>.json
```

排障速查放在页尾：[常见问题](#常见问题)。

## A. 三台物理设备在同一局域网互联

需要：三台电脑（系统不限）、同一路由器下的有线或 Wi-Fi，路由器不隔离客户端（关闭“AP 隔离”“访客网络”）。下文三台分别叫 `lan-a`、`lan-b`、`lan-c`。

1. 每台生成自己的配置（名字换成各自的）：

   ```bash
   python3 scripts/accept_config.py lan --name lan-a --out $ACC/lan-a
   ```

2. 每台导出自己的公开证书。命令会打印“节点证书指纹”：

   ```bash
   ./xmesh pair --config $ACC/lan-a/config.json --state-dir $ACC/lan-a/state --export $ACC/lan-a-public.json
   ```

3. 把三个 `*-public.json` 互相拷到另外两台。**指纹在导出方的屏幕和导入方的屏幕上各看一次，确认一致**（不要只从文件里读）。
4. 每台对另外两台各导入一次（`<指纹>` 填对方屏幕上显示的那串）：

   ```bash
   ./xmesh pair --config $ACC/lan-a/config.json --state-dir $ACC/lan-a/state \
     --import $ACC/lan-b-public.json --fingerprint <lan-b 的指纹> --tags tag:peer,tag:device
   ```

   指纹不一致时命令会报“配对指纹与 --fingerprint 不一致”并退出，这是正确行为，说明有人在中间换了文件。
5. 三台各开一个终端启动节点并保持运行：

   ```bash
   ./xmesh serve --config $ACC/lan-a/config.json --state-dir $ACC/lan-a/state
   ```

6. 每台对另外两台各验证一次，共六次。`--expected-os` 填被验证那台的系统（`linux`、`darwin` 或 `windows`）：

   ```bash
   python3 scripts/smoke_peer.py --gateway 127.0.0.1:8080 --device lan-b --expected-os linux --expected-mode lan
   ```

   通过时最后一行是 `{"result": "PASS", ...}`。mDNS 发现需要几秒，脚本最多等 20 秒。

**通过标准：** 六次全部 `PASS`。
**交回：** 六次脚本输出的最后一行（通过或失败都要）；失败时再贴 `./xmesh status --endpoint http://127.0.0.1:8080/mcp --tool devices.list --json` 的结果，以及各台的 `accept_collect.py` 输出。

## B. claude.ai 或 Codex 的真实 OAuth 与工具可见性

需要：一台能出站访问 Tailscale 控制面的电脑；一个 Tailscale 账号，已开启 MagicDNS 和 HTTPS 证书；claude.ai 或 Codex 账号，且账号能添加自定义 MCP 连接。

1. 在 Tailscale 管理台（Access controls）加入下面的策略片段（已有同名项就合并），然后在 Settings → Keys 生成一个注册 key：可重复使用，标签 `tag:ingress` 和 `tag:device`。把 key 存进本机文件，例如 `~/xmesh-accept/authkey`（权限 `0600`）：

   ```json
   {
     "tagOwners": { "tag:ingress": ["autogroup:admin"], "tag:device": ["autogroup:admin"] },
     "nodeAttrs": [{ "target": ["tag:ingress"], "attr": ["funnel"] }],
     "acls": [{ "action": "accept", "src": ["tag:ingress", "tag:device"], "dst": ["tag:device:7433"] }]
   }
   ```

2. 想好这台入口机在 tailnet 里的完整域名，形如 `https://<机器名>.<你的tailnet>.ts.net`（机器名就是下面的 `--name`；确切域名以管理台机器列表里显示的为准）。生成配置，`--issuer` 填这个域名：

   ```bash
   python3 scripts/accept_config.py ingress --name ing-a --out $ACC/ing-a \
     --authkey-file $ACC/authkey --issuer https://ing-a.<你的tailnet>.ts.net
   ```

   输出里的 `start` 就是启动命令，`notes` 说明口令文件的位置。口令在 `$ACC/ing-a/state/oauth-password`，登录页要填它。
   生成的配置把“自行注册的客户端”（claude.ai、Codex 都是）的 scope 上限放宽到 `devices:read、exec:*、fs:*、pty:*、sys:*`，否则它们只能看到 `devices.list`。这只是为了验收，做完请把配置里的 `ingress.oauth.dynamic_client_scopes` 收窄或删除。
3. 启动并保持运行（用上一步输出的 `start`）：

   ```bash
   ./xmesh serve --config $ACC/ing-a/config.json --state-dir $ACC/ing-a/state
   ```

4. 另外至少要有一台带 `tag:device` 的普通设备，让对话里有东西可操作：在那台机器上用 `scripts/accept_config.py tailnet --name dev-a --out $ACC/dev-a --authkey-file <同一个 key 文件>` 生成配置并 `serve`（它的授权已包含 `tag:ingress`）。入口机本身也是 Agent，可以直接操作它自己。
5. 从**另一台机器（或手机热点下的电脑）**先做外部检查，确认入口对外可达、OAuth 发现信息正确：

   ```bash
   python3 scripts/accept_ingress_probe.py --ingress https://ing-a.<你的tailnet>.ts.net
   ```

   应输出 `{"result": "PASS", ...}`。失败时它会说是哪一项（连不上、没有 401 质询、元数据里的地址不是你配置的 issuer）；先解决它，再继续。
6. 在 claude.ai（Settings → Connectors → 添加自定义连接器）或 Codex 的自定义 MCP 服务器里添加 `<issuer>/mcp`，按页面提示授权；浏览器里的登录页填口令文件的内容。
7. 授权后开一个**新对话**，让它依次：列出设备；在入口机或 `dev-a` 的 `work` 目录里写一个文件再读出来；执行 `exec.run echo hello`；打开一个终端、输入 `echo hi`、关闭。
8. 如果对话里看不到设备工具（只有 `devices.list`），先检查上面第 2 步的 scope 上限，再重新添加连接器（旧授权的 scope 不会自动变）。

**通过标准：** 第 5 步 `PASS`；第 7 步四项都成功；工具列表里有 `<设备>/exec.run` 等设备工具。
**交回：** 对话中模型看到的工具名列表的截图或文字；`exec.run` 的返回；入口机的 `accept_collect.py` 输出（其中审计只含工具名与状态）；第 5 步的输出。**不要贴**口令文件、注册 key、浏览器地址栏里的 `code=` 参数。

## C. 公共域名、反向隧道（可选）

只有想用自有域名或隧道而非 Funnel 时才做。

**自有域名与证书（`public:443`）。** 主机需要从公网可达 443 端口，域名解析到它：

```bash
python3 scripts/accept_config.py ingress --name ing-a --out $ACC/ing-a --authkey-file $ACC/authkey \
  --issuer https://mcp.example.com --listen public:443 --domain mcp.example.com
```

默认用 ACME 自动申请证书（`--tls autocert`）；有自己的证书文件时加 `--tls file --cert-file <证书> --key-file <私钥>`。控制面不必是官方的（Funnel 才要求官方控制面），可用 `--control-url` 指向自建 Headscale。

**反向隧道（`tunnel`）。** 入口只监听本机回环地址，外层隧道服务负责 HTTPS，并必须把真实的 `Host`（或 `X-Forwarded-Host`）转给入口，同时追加 `X-Forwarded-For`：

```bash
python3 scripts/accept_config.py ingress --name ing-a --out $ACC/ing-a --authkey-file $ACC/authkey \
  --issuer https://mcp.example.com --listen tunnel
./xmesh serve --config $ACC/ing-a/config.json --state-dir $ACC/ing-a/state --tunnel-listen 127.0.0.1:9000
```

在隧道服务里把 `https://mcp.example.com` 指向 `127.0.0.1:9000`。

两种方式都按 B 的第 5 至 7 步验证。隧道方式还可以在入口机本机先走完整个 OAuth 协议路径（发现、动态注册、PKCE、刷新轮转、`tools/list`、`exec.run`），不经过公网：

```bash
python3 scripts/oauth_client.py --base 127.0.0.1:9000 --issuer https://mcp.example.com \
  --password-file $ACC/ing-a/state/oauth-password --preregistered check --device ing-a
```

**交回：** 同 B；使用隧道时再写明隧道服务的名称，以及它是否转发了 `Host` 与 `X-Forwarded-For`。

## D. 云沙箱出站与入网

需要：Codex Cloud 或 Claude 云沙箱里能运行命令的环境，且网络策略允许访问 Tailscale 控制面与 DERP；一台已在 tailnet 里运行的自己的设备（下称对端，名字例如 `home-server`）。

1. 在对端上按 B 第 4 步的方式生成配置并 `serve`：`accept_config.py tailnet --name home-server ...`。
2. 在 Tailscale 管理台生成另一个注册 key：**临时（ephemeral）、可重复使用，标签 `tag:device`**，存成文件。
3. 把本归档里的 `xmesh`（Linux 沙箱用 `linux-amd64` 或 `linux-arm64` 版）、`scripts/` 和这个 key 文件放进沙箱。key 不要写进命令行或环境变量。
4. 在沙箱里执行：

   ```bash
   python3 scripts/smoke_sandbox.py --binary ./xmesh --auth-key-file ./authkey --peer home-server:linux
   ```

   `--peer` 是 `<设备名>:<系统>`，可重复。脚本启动两个生命周期，验证沙箱节点加入 tailnet、调用对端的文件/exec/PTY、重启后身份变化、身份状态不落盘。
5. 如果沙箱无法联网，脚本会在第一步失败。记下失败信息即可，这本身就是“该云沙箱出站被禁止”的记录。

**通过标准：** 最后一行 `{"result": "PASS", ...}`，或者在第一步因网络被拒而失败（记录为“出站被禁止”）。
**交回：** 脚本输出的最后 20 行；沙箱产品名与它的网络策略名称（例如“仅限列表域名”）。

## E. 跨主机入口 PTY 总预算

需要：两台各自作为入口的主机（`ing-a`、`ing-b`，B 的方式即可，两台都能被外部访问），加一台长期在线的 Agent（`home-server`，可以是其中一台之外的第三台，也可以复用 D 的对端）。三台都在同一 tailnet，注册 key 都带 `tag:device`，入口机的 key 另带 `tag:ingress`。

1. `home-server`：`accept_config.py tailnet --name home-server --out $ACC/home-server --authkey-file <key>`，然后 `serve`。
2. 先在 `ing-a` 上生成配置，再把它生成的口令文件拷到 `ing-b`，让两台入口使用**同一个口令**（脚本用同一个口令登录两台）。两台的配置都指向同一个 `pty_budget_authority`，并使用相同的 `max_pty_per_client`：

   ```bash
   # ing-a
   python3 scripts/accept_config.py ingress --name ing-a --out $ACC/ing-a --authkey-file $ACC/authkey \
     --issuer https://ing-a.<tailnet>.ts.net --max-pty-per-client 3 --pty-budget-authority home-server
   # 把 $ACC/ing-a/state/oauth-password 拷到 ing-b 的 $ACC/oauth-password，然后在 ing-b 上：
   python3 scripts/accept_config.py ingress --name ing-b --out $ACC/ing-b --authkey-file $ACC/authkey \
     --issuer https://ing-b.<tailnet>.ts.net --max-pty-per-client 3 --pty-budget-authority home-server \
     --password-file $ACC/oauth-password
   ```

   两台各自 `serve`（ing-b 用输出的 `start`，里面已带 `--oauth-password-file`）。
3. 在任意一台能访问两个入口的电脑上（口令文件拷一份过去）运行脚本。它用预注册客户端 `check` 登录两个入口，从两个入口交替同时发 10 次 `home-server/pty.open`：

   ```bash
   python3 scripts/accept_pty_budget.py --ingress https://ing-a.<tailnet>.ts.net --ingress https://ing-b.<tailnet>.ts.net \
     --password-file ./oauth-password --device home-server --limit 3 --attempts 10
   ```

   应输出 `"result": "PASS"`，其中 `opened` 为 3、`refused_quota_exceeded` 为 7，`live_per_pty_list` 为 3（用 `pty.list` 数出来的确实存活的终端）。脚本结束时会关掉它开的终端。
4. 停掉 `home-server` 上的 xmesh（Ctrl-C），然后运行：

   ```bash
   python3 scripts/accept_pty_budget.py --ingress https://ing-a.<tailnet>.ts.net --ingress https://ing-b.<tailnet>.ts.net \
     --password-file ./oauth-password --device home-server --expect-unavailable
   ```

   应输出 `"result": "PASS"`，每个入口的结果都是 `quota.unavailable`，没有创建终端。
5. 把 `home-server` 重新启动。约 30 秒内权威 Agent 不发放租约，之后再运行第 3 步一次应当恢复通过。

**通过标准：** 第 3、4 步 `PASS`。
**交回：** 两次脚本输出的整行 JSON；第 5 步恢复后的那一行。失败时再附两台入口和 `home-server` 的 `accept_collect.py` 输出。

## F. Windows 普通用户登录会话

需要：一台 Windows，用普通（非管理员）账户登录并保持登录；再有第二个本地用户（也是普通用户）。本步骤有两个目的：确认默认安装方式在普通账户下工作，以及确认**请求 L2 时被拒绝**（生产 Windows L2 尚未接通，预期结果是拒绝而不是成功，见页尾说明）。

状态目录请放在当前用户自己的配置目录下（下面用 `$HOME\xmesh-accept`）。Windows 上程序创建状态目录时不会设置专门的访问控制，实际保护来自父目录继承：放在 `C:\` 根下新建的目录里，系统默认会让其他已登录用户可读写；放在用户配置目录里默认只有本人和管理员可访问。

### 第一用户（普通账户）

1. 解压归档，把 `xmesh.exe` 放到固定位置，例如 `$HOME\xmesh\`（`install` 要求二进制与配置在持久位置）。生成配置并启用 PAT（第二用户没有口令时应被拒绝；回环地址对本机所有用户可达，所以要靠 PAT 区分）：

   ```powershell
   $ACC = "$HOME\xmesh-accept"
   py -3 scripts\accept_config.py lan --name win-a --out "$ACC\win-a" --pat-file "$ACC\gateway.pat"
   .\xmesh.exe install --config "$ACC\win-a\config.json" --state-dir "$ACC\win-a\state" --executable "$HOME\xmesh\xmesh.exe"
   ```

   `install` 会打印任务名。同名任务已存在时会失败，应先到“任务计划程序”里检查。
2. **注销再登录**，不要手动启动。登录后确认任务已自动运行，并让脚本报告运行身份：

   ```powershell
   .\xmesh.exe status --endpoint http://127.0.0.1:8080/mcp --tool local/sys.info --pat-file "$ACC\gateway.pat" --json
   powershell -NoProfile -File scripts\accept_windows_user.ps1 -Role owner -StateDir "$ACC\win-a\state"
   ```

   期望：`status` 返回本机信息；脚本报告 `is_administrator: false`、`high_integrity_token: false`，`processes` 里有 `xmesh.exe` 且属于当前用户、`session_id` 是交互会话（不是 0），`tasks` 里 `run_level` 是 `Limited`，`gateway_http_without_token` 是 `401`。

3. 验证请求 L2 被拒绝：生成一份开启 L2 的配置，重启任务（任务名取 `install` 打印的），再运行脚本：

   ```powershell
   py -3 scripts\accept_config.py lan --name win-a --out "$ACC\win-a" --pat-file "$ACC\gateway.pat" --isolation L2 --force
   Stop-ScheduledTask -TaskName "<任务名>"; Start-ScheduledTask -TaskName "<任务名>"
   py -3 scripts\accept_expect_refusal.py --gateway 127.0.0.1:8080 --pat-file "$ACC\gateway.pat"
   ```

   期望 `"result": "PASS"`：`exec.run` 与 `pty.open` 都返回 `policy_denied` / `sandbox.unavailable`。如果任何一个成功执行了，这是失败（隔离被悄悄跳过），把输出原样贴回。

### 第二用户（另一个本地普通用户）

4. 切换到第二个用户的会话（快速用户切换，第一用户保持登录），在其中运行（`<第一用户的路径>` 是第一用户 `$ACC\win-a\state` 的完整路径）：

   ```powershell
   powershell -NoProfile -File <归档目录>\scripts\accept_windows_user.ps1 -Role other -StateDir "<第一用户的路径>"
   ```

   期望：列目录、列 `audit`、写入文件都是 `DENIED`，不带 PAT 访问第一用户的 Gateway 得到 `401`，总结果 `PASS`。第一条显示 `NOT_FOUND` 说明路径写错了，不算通过。

**交回：** 第 2 步、第 3 步、第 4 步各自的完整输出；如果第 4 步有 `ALLOWED`，说明是哪个目录和它所在的位置。

## G. Claude Code 连接观察

在 Claude Code 里添加本机 Gateway：`claude mcp add --transport http xmesh http://127.0.0.1:8080/mcp`（配置了 PAT 时再加 `--header "Authorization: Bearer <PAT 文件内容>"`）。让它依次执行：列出设备；读一个文件；`exec.run`；打开并关闭终端。

**交回：** 工具列表是否完整；若某一步失败，贴失败的那一步和它的错误对象。

## 常见问题

| 现象 | 先查 |
| --- | --- |
| 设备不出现在 `devices.list` | 控制面里该节点是否带 `tag:device`；目标设备 `agent.authz` 是否列出调用方的 tag（生成的配置已列出 `tag:device`、`tag:ingress`；LAN 为 `tag:peer`）；防火墙是否放行 TCP 7433；Tailscale 策略是否允许 `tag:device:7433` |
| LAN 看不到对端 | 路由器是否隔离客户端；本机防火墙是否放行 mDNS（UDP 5353）；两端是否都完成了对方的 `--import`，并且都重启过 `serve` |
| `accept_ingress_probe.py` 连不上 | Funnel 是否已在管理台对该节点开启；DNS/证书是否生效（浏览器能否打开该域名）；入口机上的 `xmesh serve` 是否仍在运行 |
| 对话里只有 `devices.list` | `dynamic_client_scopes` 是否包含所需 scope；改完后要重新添加连接器 |
| 入口返回 421 | 请求的 `Host` 不是 `issuer` 的主机；隧道没有转发真实 `Host` |
| 重启入口后工具消失 | OAuth 令牌只在内存里，入口重启后要重新授权 |

## 不需要你提供的部分

Windows 生产 L2 的原生验收只通过 GitHub 托管 runner 完成，不需要你的机器；但由于生产启动链路尚未接通，当前仍返回 `sandbox.unavailable`，所以 F 的预期是“被拒绝”。L2 接通并进入发布后，F 的第 3 步会改成验证成功路径。
