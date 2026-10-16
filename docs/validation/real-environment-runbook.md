# 真实环境验收步骤（面向无需读代码的操作者）

本页把仓库里还缺的真实环境验收拆成独立小步骤。每一项都可以单独做，做完把“交回什么”里的内容贴回即可；做不到某一项就跳过它，其余不受影响。凭据（注册 key、OAuth 口令）只放在本机文件里，不要贴到聊天里。

先决条件见[运行手册](../operations.md)的“tailnet 节点”“LAN 配对”“后端与入口”。构建：`go build -o bin/xmesh ./cmd/xmesh`（Windows 为 `bin\xmesh.exe`）。

## A. 三台物理设备在同一局域网互联

需要：三台电脑（系统不限）、同一路由器下的有线或 Wi-Fi，路由器不隔离客户端。

1. 三台各自写一份配置，`mesh.mode` 为 `lan`，`mesh.lan.mdns` 为 `true`，`node.name` 分别为 `lan-a`、`lan-b`、`lan-c`，`roles` 为 `[agent, gateway]`。
2. 每台执行 `xmesh pair --config xmesh.yaml --state-dir <状态目录> --export node-public.json`，把三个导出文件互相拷贝。
3. 每台对另外两台各执行一次 `xmesh pair ... --import <对方文件> --fingerprint <对方指纹> --tags tag:peer,tag:device`。指纹在两台机器的屏幕上各看一次确认一致。
4. 三台都重启 `xmesh serve`。
5. 在 `lan-a` 上执行 `python3 scripts/smoke_peer.py --gateway 127.0.0.1:8080 --device lan-b --expected-os <系统>`，再对 `lan-c` 重复，然后在另两台上各做一遍。

交回：六次脚本输出（通过或失败的最后几行）；如果失败，贴 `xmesh status --endpoint http://127.0.0.1:8080/mcp --tool devices.list --json` 的结果。

## B. claude.ai 或 Codex 的真实 OAuth 与工具可见性

需要：一台能出站访问 tailscale 控制面的电脑；一个 Tailscale 账号并已开启 Funnel；claude.ai 或 Codex 账号，且账号能添加自定义 MCP 连接。

1. 在 Tailscale 管理台给这台机器的注册 key 授予 `tag:ingress` 和 `tag:device`，开启 Funnel。
2. 配置参考 `docs/contracts/schemas/examples/xmesh.config.json` 的 `ingress` 段：`listen: funnel`，`oauth.issuer` 写成 `https://<这台机器>.<你的tailnet>.ts.net`，`clients` 里为 `claude` 或 `codex` 各留一项 scope。
3. 在状态目录里放一个 `oauth-password` 文件（随机长口令，权限 0600，也可用 `--oauth-password-file` 指向别处）。`node.roles` 要含 `ingress`（例如 `[agent, gateway, ingress]`），或者启动时加 `--ingress`；两者都没有时配置里的 `ingress` 段不会生效，Funnel 也不会开。然后 `xmesh serve` 启动。
4. 在 claude.ai 或 Codex 中添加自定义 MCP 连接，地址填 `<issuer>/mcp`，按页面提示授权，口令填上面的文件内容。
5. 授权后开新对话，让它依次：列出设备、读一个测试文件、执行一次 `exec.run echo hello`、打开一个终端并关闭。

交回：对话中模型看到的工具名列表的截图或文字、`exec.run` 的返回、状态目录 `audit` 里对应时间的几行记录（只要工具名与状态，不含内容）。

## C. 公共域名、反向隧道（可选）

只有想用自有域名或隧道而非 Funnel 时才做。选 `ingress.listen: public:443` 并配置 `domain` 与证书，或 `tunnel` 并在隧道服务里把 HTTPS 指到本机回环端口。验证办法同 B 的第 4、5 步，交回同样内容。

## D. 云沙箱出站与入网

需要：Codex Cloud 或 Claude 云沙箱里能运行命令的环境，且网络策略允许访问 tailscale 控制面与 DERP。

1. 把 `bin/xmesh` 和一个 `ephemeral` 的 tag 注册 key 文件放进沙箱（key 不要写进命令行）。
2. 在沙箱里执行：`python3 scripts/smoke_sandbox.py --binary <路径> --auth-key-file <key 文件> --peer <你的设备名>:<系统>`。
3. 如果沙箱无法联网，脚本会在第一步失败；记下失败信息即可，这本身就是“云沙箱出站被禁止”的记录。

交回：脚本输出的最后 20 行；沙箱的网络策略名称（例如“仅限列表域名”）。

## E. 跨主机入口 PTY 总预算（新功能）

需要：两台能公网或内网各自作为入口的主机，加一台长期在线的 Agent（可以是其中一台）。

1. 三台都加入同一 tailnet，Agent 设备名假设为 `home-server`。
2. 两台入口主机的配置里都写 `"pty_budget_authority": "home-server"`，并保持相同的 `max_pty_per_client`（例如 3）。
3. 两台入口各用同一个 OAuth client 同时反复调用 `home-server/pty.open`（可让模型对话里“同时打开 10 个终端”分别在两个入口上做）。
4. 数 `home-server` 上的活动终端：不应超过 3 个。其余请求应返回 `quota.exceeded`。
5. 停掉 `home-server` 上的 xmesh，再请求打开终端：应返回 `quota.unavailable`，不创建终端。

交回：第 4 步的终端数、第 5 步的返回内容。

## F. Windows 普通用户登录会话

需要：一台 Windows，用普通（非管理员）账户登录，并保持登录。

1. 在这个账户下构建或拷入 `xmesh.exe`，写配置，执行 `xmesh install`（Windows 默认注册当前用户的登录计划任务）。
2. 注销再登录，确认任务已自动启动：`xmesh status --endpoint http://127.0.0.1:8080/mcp --tool local/sys.info --json`（这里打的是本机 Gateway，所以工具名是 `local/sys.info`；配置的 `roles` 需含 `gateway`）。
3. 再开第二个用户的会话，尝试访问第一个用户的状态目录和命名对象：应被拒绝。

交回：第 2 步输出；第 3 步各命令的拒绝信息。

## G. Claude Code 连接观察

在 Claude Code 里添加 `http://127.0.0.1:8080/mcp`（本机 Gateway，若配置了 PAT 则使用 `Authorization: Bearer`）。让它依次执行：列出设备、读文件、`exec.run`、打开并关闭终端。交回：工具列表是否完整、哪一步失败及其错误对象。

## 不需要你提供的部分

Windows 生产 L2 的原生验收只通过 GitHub 托管 runner 完成，不需要你的机器；但由于生产启动链路尚未接通，当前仍返回 `sandbox.unavailable`，不要在 F 里期待 L2 可用。
