# 2026-11-26 委托原生平台 CI 验证

2026-11-23，GitHub 托管 runner 在 `master` 提交 `1b748e929cefa5ad0b845a75fcd7f3eea000091a` 上运行了 `go test -race ./... -timeout=120s`。本记录补充原生 CI 证据。[议题 #210](https://github.com/tzf1003/xmesh/issues/210) · [测试提交](https://github.com/tzf1003/xmesh/commit/1b748e929cefa5ad0b845a75fcd7f3eea000091a)

| 平台 | `internal/delegate` 结果 | 作业日志 |
| --- | --- | --- |
| Windows Server 2025（amd64） | `ok`，42.063s | [test (windows-latest)](https://github.com/tzf1003/xmesh/actions/runs/45088343200/job/137412088279) |
| macOS（`macos-26-arm64`） | `ok`，41.118s | [test (macos-latest)](https://github.com/tzf1003/xmesh/actions/runs/45088343200/job/137412088449) |

## 用例覆盖

日志只列出包级结果。以下覆盖范围依据该提交的测试文件平台后缀和运行时跳过条件判定。

- **Windows 与 macOS 进程树终止。** 超时、显式取消和管理器关闭用例均无平台限制或跳过条件，通过检查模拟运行器的后代进程停止写入心跳来验证终止。[`manager_test.go`](../../internal/delegate/manager_test.go)
- **Windows 进程存活判断。** PID 用例只在 `int` 小于 64 位时跳过。在 Windows amd64 上，它确认本测试进程为存活，并拒绝低 32 位与当前 PID 相同的超范围值。已退出会话用例调用 Unix socket 辅助函数，在 Windows 上跳过。[`session_pid_test.go`](../../internal/delegate/session_pid_test.go) · [`session_test.go`](../../internal/delegate/session_test.go)
- **Windows 命名管道端点检查。** `_windows_test.go` 用例验证管道路径识别、拒绝普通文件及其内容保持不变。发送成功用例使用 Unix socket，在 Windows 上跳过。[`session_windows_test.go`](../../internal/delegate/session_windows_test.go) · [`session_test.go`](../../internal/delegate/session_test.go)

## 待验证

- [ ] Windows 命名管道成功写入，并在接收端核对消息内容。
- [ ] Windows 用户环境冒烟，确认真实会话的存活与退出状态，以及取消或超时后运行器后代退出。
- [ ] macOS 用户环境冒烟，确认取消或超时后运行器后代退出。
