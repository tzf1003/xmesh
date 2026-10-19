# 14. 设备连接密码与发现

本篇描述版本 2 的统一设备配对。设备身份、双路径和恢复策略见 [设备身份与 LAN / Tailnet 共存](19-device-connectivity.md)，本机管理接口以 [控制通道契约](../contracts/control.md) 为准。

## 用户流程

1. 在设备 B 的面板上设置连接密码，长度为 10 到 128 个字符，首尾不能有空白。
2. 在 A 的发现列表选择 B 的可用 LAN 或 Tailnet 路径，也可手动填写地址。
3. 输入 B 的连接密码。成功后双方立即信任对方的版本 2 设备公钥，默认授予 `tag:peer`、`tag:device`。
4. 信任属于设备，可用于双方任意可达的路径。设备列表以 `device_id` 合并显示，首次连接与恢复优先 LAN，健康连接保持当前路径。
5. 撤销设备信任会结束其全部路径与逻辑会话；Tailnet 网络权限变化只影响对应路径。

高级流程使用 `xmesh pair --export/--import` 或控制接口交换公开身份文档。用户独立核对 SHA-256(SPKI) 指纹后导入，运行中的节点立即生效；离线操作通过文件锁与原子写入保存信任。

## 配对协议

LAN 与 Tailnet 共用同一个密码、限流状态及信任服务。连接使用 TLS 1.3，配对引导的 ALPN 为 `xmesh-pair/2`。该引导只提供 `/mesh/v2/pair/` 路由；业务链路要求受信任的客户端公钥。

SPAKE2 使用 ristretto255 群和 Argon2id 派生值。双方的版本 2 公开身份文档进入确认 transcript，绑定设备 ID、公钥与名称。公开文档包含 `version`、`device_id`、`name`、`public_key`、`fingerprint`、`tags`。

| 对端路由 | 行为 |
| --- | --- |
| `POST /mesh/v2/pair/identity` | 返回公开设备文档，发现时核对 TLS 公钥与设备 ID |
| `POST /mesh/v2/pair/challenge` | 返回协议版本、盐与 KDF 标识；未设置密码时拒绝 |
| `POST /mesh/v2/pair/exchange` | 计入限流，交换群元素与身份，返回事务 ID 和确认码 |
| `POST /mesh/v2/pair/confirm` | 验证确认码与密码、信任版本，原子授予设备信任 |

更改或清除密码提升密码版本并清空待定事务。每个设备的信任版本使撤销前的迟到确认失效；相同确认重试可幂等成功，撤销后的旧回执失效。

待完成事务最多 16 个，60 秒过期。连续 5 次未成功尝试后锁定，初始 30 秒，指数增加至 30 分钟。限流按节点共享；成功确认清零。密码状态只返回是否设置、更新时间、失败次数和锁定期限。

## 发现与存储

LAN 使用 mDNS 的版本 2 设备 ID 与名称作为候选。Tailnet 状态提供候选 IP，再通过 TLS 公开身份接口识别设备。发现信息只用于找地址；经过当前设备信任的 mTLS 核验后，路径才进入设备目录。

身份、信任与密码派生值保存在 `<状态目录>/identity-v2/`，私有文件权限为 `0600`。设备 ID 来自持久公钥；证书续期与 IP 变化保持设备 ID。

## 本机控制与 CLI

控制接口使用 `/v2/pair/`：密码 `GET/PUT/DELETE password`、`GET discovered`、`POST connect`、`POST export/import`，以及 `DELETE trust/{device_id}`。`connect` 可选择 `path: lan|tailnet`。

```sh
xmesh control pair password set --password-file 文件
xmesh control pair discover
xmesh control pair connect --address https://host:7433 --path tailnet --password-file 文件
xmesh control pair export --out 文件
xmesh control pair import --file 文件 --fingerprint 公钥指纹
xmesh control pair revoke --device-id 设备ID
```

密码从标准输入或文件读取，避免进入命令行参数。面板统一提供本机密码、发现、手动添加、公钥文档导入导出与设备信任撤销。

## 升级与验证

版本 2 使用新的设备身份与链路协议。升级前运行 `xmesh migrate` 保存旧配置及状态备份、转换配置，随后重新配对。旧连接在升级时结束；回滚使用原程序、配置与备份状态。

回归覆盖密码轮换与迟到确认、信任撤销与导入竞争、确认重试、发现合并、独立 provider 版本、双向同时拨号、路径恢复与稳定资源归属。跨平台和实际网络的验收记录统一保存在共存方案中。
