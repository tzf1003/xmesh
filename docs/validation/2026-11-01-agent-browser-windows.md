# agent-browser 浏览器后端 Windows 真机补测（2026-11-01，v0.1.0-beta.28）

补 `2026-11-01-agent-browser.md` 的“未覆盖”项。只记录实测到的；对结论有影响的混淆因素写在结果旁边。

环境：home-windows，Windows 10.0.26100，Node v24.15.0（`C:\nodejs\node.exe`），agent-browser 0.41.0，本机已装的 Google Chrome（`C:\Program Files\Google\Chrome\Application\chrome.exe`，未下载 Chrome for Testing）。

## 结果

| 项目 | 结果 |
| --- | --- |
| `install.mjs --prefix %LOCALAPPDATA%\xmesh-browser --skip-chrome` | 通过（`npm ci` 约 4 秒，`agent-browser doctor` 通过）。打印的配置里 `command` 是 `["C:\\nodejs\\node.exe", "C:\\Users\\12442\\AppData\\Local\\xmesh-browser\\bin\\xmesh-agent-browser.mjs", "--tools", "core,tabs"]`，即 `node.exe` 直连，不经 `.cmd` |
| `node.exe` 直连包装器，经 stdio 做 MCP `initialize` | 通过 |
| 离线设备用已装 Chrome（`AGENT_BROWSER_EXECUTABLE_PATH` 指向系统 Chrome） | 通过：`agent_browser_open https://example.com` 返回 `Example Domain`，`isError:false` |
| 强制终止包装器（`TerminateProcess`）后的回收 | 见下 |

### 强制终止后的回收（结论有限）

测试方式：包装器由 Node 脚本拉起、打开页面后强制终止，8 秒后检查。终止后约 8 秒再查，已没有命令行含 `agent-browser` 的进程，但命名空间目录 `%USERPROFILE%\.agent-browser\namespaces\xmesh-<pid>-<id>` 仍留在磁盘上（我手动删了）。

这个结果**不能证明包装器自己能回收**：整个测试跑在 xmesh 的 `exec.run` 里，`exec.run` 结束时会连同整棵进程树一起回收，可能是它而不是包装器清掉了 daemon 和 Chrome。要区分两者，需要在没有外层回收的环境里测（比如作为长期运行的后端由 xmesh 启动后再被强杀）。进程计数也受到本机用户自己运行的 Chrome 干扰（基线约 21 个，仅作参考）。

可以确定的是：强杀不会让包装器执行它的清理，所以命名空间目录一定会残留；macOS 上同样的强杀则确实留下了 daemon 和 Chrome（见 `2026-11-01-agent-browser-macos.md`）。

## 未覆盖

- Windows 上 `process_cleanup: tree` 对 daemon 的实际回收（需要 xmesh 后端真实拉起的场景，排除 `exec.run` 的干扰）。
- 命名空间目录残留的清理（目前没有任何组件会在强杀后删除它们）。
- agent-desktop 的 snapshot/ref_act（Mac 仍缺授权；Windows 上上一轮 Notepad 快照超时，本轮未重测）。
- 测试过程中 home-windows 的终端会话（PTY）对输入没有响应，所以安装和测试改用了 `exec.run` 的短命令完成。这是设备侧问题，原因未查。
