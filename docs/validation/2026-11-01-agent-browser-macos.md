# agent-browser 浏览器后端 macOS 真机补测（2026-11-01，v0.1.0-beta.28）

补 `2026-11-01-agent-browser.md` 的“未覆盖”项。只记录实测到的；没测到的写明原因。

环境：xiaosong-mac，macOS 27.0.1 arm64，Homebrew Node v26.13.0，npm 11.20.0，agent-browser 0.41.0，Chrome for Testing 156.0.8078.80（`~/.agent-browser/browsers`，由先前的 `agent-browser install` 下载）。

## 结果

| 项目 | 结果 |
| --- | --- |
| 安装脚本 `install.mjs` | **失败两处，已修**。见下 |
| 修后 `install.mjs --prefix /tmp/xb --skip-chrome` | 通过：`npm ci` 装好 agent-browser 0.41.0，打印出可粘贴的配置 |
| 包装器经 stdio 做 MCP `initialize` | 通过（serverInfo 为 agent-browser 0.41.0，profile 为 core、tabs） |
| 离线设备用已装 Chrome（`AGENT_BROWSER_EXECUTABLE_PATH` 指向已装的 Chrome，路径含空格） | 通过：`agent_browser_open https://example.com` 返回 `Example Domain`，`isError:false`，结果带内容边界 nonce |
| 正常退出（stdin 关闭）后的回收 | 通过：退出后 3 秒内没有 `agent-browser` 或 Chrome 进程残留 |
| 包装器被 `kill -9` 时的回收 | **不会回收**。daemon（PPID 1、自成进程组）与 Chrome 在包装器被杀 8 秒后仍在运行，命名空间目录 `~/.agent-browser/namespaces/xmesh-<pid>-<id>` 仍在；用 `agent-browser close --all`（带该命名空间）可清理 |

### 修掉的两个安装问题

1. `npm-cli.js not found next to node`：Homebrew 的 npm 在共享前缀 `/opt/homebrew/lib/node_modules/npm`，Node 目录下的 `npm` 是指向它的链接，原来的候选路径找不到。现在也解析 node 目录下 `npm` 链接的真实路径。
2. `npm ci` 报 lock 文件不同步（`Missing: xb@0.1.0 from lock file`）：npm 11 在别的工作目录里执行 `npm ci --prefix DIR` 会把项目当成目录名，与锁文件对不上。现在改为在前缀目录内执行 `npm ci`（不再传 `--prefix`），手工在前缀里运行同样的命令已验证通过。

## 对方案的影响

- `process_cleanup: tree` 在 macOS 上对包装器被强杀的情况**无效**，因为 daemon 和 Chrome 不在包装器的进程组里。正常退出路径已被包装器接住；强杀（OOM、`kill -9`、节点被强制终止）后依赖 daemon 的空闲超时（包装器缺省 5 分钟）来退出。需要更强保证时，要在后端 supervisor 里按命名空间目录扫描并执行 `close --all`，这是后续工作。
- 本轮没有测 `tools/list` 与第二个调用方的隔离，那部分由 `scripts/accept_browser.py` 覆盖。

## 未覆盖

- **Windows**：Job 回收与 `node.exe` 直连没有测成。home-windows 在本轮多次进入 stale，恢复后能执行命令，但终端会话无响应，没能跑完安装；源文件已能从仓库拉取。需要设备稳定后重测。
- **agent-desktop 的 snapshot/ref_act**：Mac 仍缺 Accessibility 与 Screen Recording 授权，未测。
- 我自写的 Node 测试脚本在一次 MCP 往返里全部超时，原因没有查明（用 shell 管道直接喂同样的请求是正常的），所以上面的 MCP 结果都来自 shell 管道，不是那个脚本。
