# 控制面管理约束

范围：`meshadmin` 库与 `xmesh mesh` 子命令，契约见 [meshadmin](../contracts/meshadmin.md)。

## CON-MESH-01

内核、Gateway、Agent 不引用 `meshadmin`，也不持有它用的管理凭据。`meshadmin` 只依赖标准库与公开模块，不引用 `internal/`。Tailscale 与 Headscale 的差异只出现在 `meshadmin` 的 Provider 里，调用方（CLI、桌面 UI）不按控制面类型分支，见 [RUL-20](../contracts/rulings.md#rul-20)。

管理凭据能改整个 tailnet 的访问策略，比任何一台设备的权限都大。它和设备上的 Agent 放在同一个进程里，就会让一次 Agent 被攻破扩大成整个 tailnet 被改写。

## CON-MESH-02

管理凭据只存放在系统钥匙串或权限 `0600` 的文件里。禁止出现在 `xmesh.yaml`、命令行参数、环境变量、日志、审计事件、`--json` 输出和错误信息里。密钥只从标准输入、`--secret-file` 或终端无回显输入读取。

写操作只用 `write` 凭据，从不退到 `read` 凭据；读操作先用 `read`，没有时才退到 `write`。预授权 key 正文只在创建它的那次输出里出现一次，写入的是调用方指定的 `0600` 文件，不进标准输出。细则见 [RUL-22](../contracts/rulings.md#rul-22)。

## CON-MESH-03

写控制面策略默认只预览。预览必须先通过控制面的校验，并给出 `before`、`after`、diff 与 `base_version`。只有显式 `--apply` 才写；写前存本机快照；写入带版本，版本已变就返回冲突，不覆盖别人的修改；每次写入都能用快照回滚，回滚本身也先存快照。细则见 [RUL-21](../contracts/rulings.md#rul-21)。

## CON-MESH-04

xmesh 的自动模板和原 DERP 片段命令只修改自己写入的、带 `// xmesh:managed <section>` 标记的策略元素。其余字节（注释、空白、键顺序、用户自己写的规则）原样保留。用户自己写的同名 `tagOwners` 或同号 DERP region 不被自动覆盖：前者保留用户的并警告，后者阻止写入。

用户在完整策略草稿或已有 DERP 地域编辑器中明确选择的修改，可以编辑原有元素。
这些修改先进入同一份完整草稿，必须经过完整差异预览、控制面校验、版本条件、
写前快照和用户确认后才写入云端；写后回读。没有明确选择的元素保留原样。

## CON-MESH-05

自建 DERP region 的 id 限 900 到 999，避开官方 region。写入 DERP 后默认探测每个新 region（`tcp`、`tls`、`http`、`stun`）；探测失败就用写前快照回滚，不把不可用的 region 留在策略里。

## CON-MESH-06

DERP 准入服务对放行名单失败即关闭：放行文件不存在、损坏或不可读时拒绝所有连接，不放行。`/admit` 之外的管理端点必须带 bearer token，没有配置 token 时关闭。derper 必须以 `--verify-client-url-fail-open=false` 启动，否则准入服务不可达时上游默认放行。

准入只看节点公钥，不接触转发的数据。内部自建准入之外的计量、计费、多租户目录不在本约束内。
