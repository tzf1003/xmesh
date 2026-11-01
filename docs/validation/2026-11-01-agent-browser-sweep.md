# agent-browser 包装器强杀后清扫复测（2026-11-01，master f05c284，含 #177）

复测 #177：包装器启动时及每分钟清扫已死包装器遗留的命名空间和 daemon/Chrome。此前两台设备的强杀结果见 `2026-11-01-agent-browser-macos.md` 与 `-windows.md`。

方法：先启动包装器 A，经 stdio 打开 `https://example.com`（用系统/已装 Chrome），强制终止 A（macOS `kill -9`，Windows `TerminateProcess`），确认 daemon 与 Chrome 残留；再启动包装器 B（只做 `initialize`），看 A 留下的东西是否被清掉。

## 结果

| 设备 | 强杀 A 后 | 启动 B 后 |
| --- | --- | --- |
| xiaosong-mac（macOS 27，Node 26） | daemon 与 Chrome 仍在运行（进程数 16 → 15），命名空间目录 `xmesh-<pid>-<id>` 仍在 | A 的 daemon、Chrome 全部消失，命名空间目录被删，`~/.agent-browser/namespaces` 为空 |
| home-windows（Windows 10.0.26100，Node 24） | 命名空间目录仍在，命令行含 `agent-browser` 的进程 13 个仍在 | 这 13 个进程全部消失，命名空间目录被删 |

Windows 这次用 WMI `Win32_Process.Create` 拉起测试脚本，脱离了 `exec.run` 的进程树回收，所以不再有上一轮的混淆：强杀后确实留下了 daemon 和 Chrome，是 B 启动时的清扫把它们收掉的。

## 局限

- 只测了“启动新包装器时清扫”这条路径。每分钟的定时清扫没有单独验证。
- 强杀与清扫之间，残留的 daemon/Chrome 仍会存在，直到下一个包装器启动或空闲超时。如果节点上没有其他包装器启动，残留会一直到 daemon 空闲超时（缺省 5 分钟）。
- Mac 与 Windows 各只跑了一次。
