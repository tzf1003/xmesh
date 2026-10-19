# 工具与路径约束

路径算法的步骤、别名和错误码在 [CTR-CWD-01](../contracts/cwd.md)。本节是这些行为里不许放松的部分。

## CON-TOOL-01

`exec` 的 `cwd`、`pty` 的 `cwd`、全部 `fs.*` 的路径参数，以及配置里的 workspace 路径，都调用同一个 `cwd.Resolver`。禁止在各个工具里各自展开 `~` 或拼接相对路径。

## CON-TOOL-02

allow/deny 比较的是 `EvalSymlinks` 之后的绝对路径。比较前的字符串即使以 allow 前缀开头，只要真实路径落在 deny 上或落在 allow 之外，仍然拒绝。符号链接指向 allow 之外时拒绝，并返回 `invalid_cwd`，`reason` 为 `symlink_escape`。

deny 优先于 allow。路径同时匹配两边时拒绝。

## CON-TOOL-03

凡是接受路径并成功执行的工具，响应里包含 `resolved_path`，值为解析并求值符号链接之后的绝对路径。Windows 上使用本地分隔符展示，但比较时大小写按该卷的惯例折叠。输入里的 `~`、`@别名` 和 `/` 不得原样当作 `resolved_path` 返回。

## CON-TOOL-04

`fs.read` 对 UTF-8 文本按行处理。文件以 UTF-8 BOM 或 UTF-16 BOM 开头时，`fs.read` 可以拒绝并说明应使用专门路径，也可以原样返回字节；禁止悄悄换成无 BOM 的 UTF-8。`fs.write` / `fs.edit` 写入调用方给出的字节，不转换换行，不增删 BOM。

本契约选择：带 BOM 或 NUL 字节密度判定为二进制的文件，`fs.read` 拒绝，错误码 `binary_rejected`，提示改用 `exec`。图片例外，见 [CTR-FS-01](../contracts/fs.md#ctr-fs-01)。

## CON-TOOL-05

`fs.glob` 与 `fs.grep` 必须接受上限。达到上限时返回已收集的结果，并设 `truncated: true`。禁止为了「给全」而无界扫描到内存耗尽。默认上限见对应契约。

## CON-TOOL-06

`fs.delete` 在目标是目录且 `recursive` 不为 `true` 时，返回 `policy_denied`，规则名 `fs.delete.recursive_default_deny`，且不删除该目录下的任何子项。`recursive: true` 仍要经过 cwd 策略；路径不在 allow 内则拒绝且无删除。

## CON-TOOL-07

`fs.read` 对大小超过 10 MiB 的文件拒绝，错误码 `too_large`，消息指明改用 `exec`。拒绝发生在读出正文之前。图片的体积上限同样是 10 MiB。

## CON-TOOL-08

`fs.write` 与 `fs.edit` 在目标文件的同一目录创建临时文件，写完并 fsync 后原子替换目标。`fs.upload` 使用相同的暂存流程，默认以排他重命名或硬链接原子发布，目标存在时返回 `conflict`；只有 `force: true` 才允许原子覆盖。父目录不存在时返回 `invalid_cwd`，`reason` 为 `parent_missing`，禁止自动 `mkdir -p`。`fs.mkdir` 才是创建目录的工具。

## CON-TOOL-09

`fs.edit` 在 `replace_all` 不为 `true` 时，`old_string` 必须恰好匹配一处。零处匹配返回 `edit_not_found`（见 [CTR-FS-03](../contracts/fs.md#ctr-fs-03)；`conflict` 表示 `expected_sha256` 不一致、移动或上传目标已存在，或下载源变化），多处匹配返回 `edit_ambiguous`，并带匹配行号。两种情况都不得修改文件。

## CON-TOOL-10

后端工具的参数对象去掉保留字段 `_lease_token` 之后，其余字段按 JSON 原样送入后端。返回的 MCP result 原样交给上游，Agent 不改写工具私有的字段名。Agent 只在传输失败、租约失败和协议失败时用自己的错误信封替换结果。

## CON-TOOL-11

后端 `policy` 为 `exclusive` 时，缺少 `_lease_token` 返回 `lease_required`；token 与当前租约不一致或已过期返回 `resource_busy`。校验通过后必须删除该字段再转发。`shared` 与 `per-caller` 若调用方仍传入 `_lease_token`，同样剥离，但不要求必须携带。

## CON-TOOL-12

stdio 后端可以带环境变量（API Key 之类），由节点的用户通过本机控制通道为单个后端设置（[CTR-CTL-14](../contracts/control.md#ctr-ctl-14)）。

- 值保存在状态目录的 `backend-env.json`（`0600`，原子替换），不写入 `xmesh.yaml`，所以配置的读取、差异、导出和备份都不带值。值不出现在任何读接口、状态、事件、审计记录和日志里；读接口只给变量名。
- 后端进程的环境是继承的最小环境（[CON-SEC-04](security.md#con-sec-04) 的构造顺序）加上这些变量，只对 `transport: stdio` 的后端生效。
- 变量变化只重启该后端：它的进程、租约与会话被替换，其他后端不动。不变的变量集合（即使顺序不同）不触发重启。
- 对调用方 `env` 被拒绝的服务商凭据名（`AWS_*`、`AZURE_*`、`GOOGLE_*`、`OPENAI_*`、`ANTHROPIC_*`）在这里允许，因为这些变量是节点的用户有意交给某个后端的。装载器与外壳劫持类名（`LD_*`、`DYLD_*`、`BASH_ENV`、`ENV`、`ZDOTDIR`、`PYTHONPATH`、`NODE_OPTIONS`、`SSH_AUTH_SOCK`、`TS_AUTHKEY`、`TAILSCALE_*`、`XMESH_*`）、策略 `env.deny_names` 与指向凭据目录的值仍然拒绝；保存时就被拒绝并返回原因，而不是等后端启动失败。
- 从配置删除后端时，它的变量一并删除。文件损坏时内核保持原样、后端不带变量启动，并在标准错误报告，不覆盖文件。
