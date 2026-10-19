# 快速安装验收

本记录覆盖 2026-10-19 的本地实现检查。发布门禁、原生验收和外部用户验收分别记录，避免把构建成功作为正式交付证据。

当前验收先覆盖现有 Mac：安装入口、首次打开、配置、客户端调用与恢复。其他设备和云端实机验收安排在后续阶段。

| 范围 | 当前证据 | 后续验收 |
| --- | --- | --- |
| Go 配置、服务、网络及现有功能 | macOS arm64 全仓 race 测试通过；配置状态、崩溃恢复、私有文件、并发修改和审计挑战有回归测试 | CI 在 Ubuntu、macOS、Windows 运行 |
| macOS 用户服务 | `TestOnboardingNativeService` 实际安装独立 LaunchAgent，验证只读 MCP、nonce 调用、未认证拒绝；原生重新配置失败后恢复原配置及服务注册、运行状态；测试服务随后删除 | Intel Mac、干净用户环境、签名安装包升级 |
| Windows | amd64 测试二进制、arm64 应用编译通过；已添加当前用户计划任务验收工作流 | 真实交互会话运行原生验收；session 0 由 CI 明确报告跳过 |
| 桌面界面 | TypeScript、单元测试和实际 Electron 回归通过；用途、恢复、授权、口令、冲突确认、功能状态均有交互用例，桌面与 740px 宽度已检查 | 四个系统/架构安装包在目标机器首次打开 |
| 真实桌面与 CLI 桥接 | 生产模式 Electron（测试替身关闭）首次自动打开向导，经界面生成计划、安装并启动真实 Go 用户服务，显示设备就绪与客户端待验证；页面无错误；工作目录含中文和空格的 CLI 检查通过 | 安装包内置内核版本与界面组合验收 |
| 客户端配置与调用 | 本机已安装 Codex app-server 读取隔离客户端配置并实际调用专属 `local/fs.stat`，任务由 waiting_user 进入 succeeded；Codex CLI 和 Claude Code 配置读取均通过；测试节点和临时认证副本已回收 | Claude Code 的实际调用与其他客户端版本验收 |
| 分发 | 清单、版本选择、校验失败、离线包、文档站链接和脚本测试通过 | 执行 Pages 部署；四平台正式安装与升级 |
| Mac 本地安装包 | ARM64 DMG 实际构建通过；app.asar、发行版 fuses 和内置内核校验通过，应用与内核版本一致；实际包在隔离配置下首次自动打开向导，原生点击进入基础设置；预览随后退出 | 当前本地包是 ad-hoc 签名测试构建，正式分发需要 Developer ID 与公证 |
| 签名 | 稳定版本 CI 检查 Windows Authenticode、macOS 签名、公证及装订 | 配置发布凭据，读取实际发布产物的验证结果 |
| 多设备 | 探针必须调用已配对远端设备工具；模拟状态与界面跳转已验证 | 两台真实设备完成 LAN 与 tailnet 各一次设置及调用 |
| 云端 | 入网/Funnel/tag 前置条件测试、入口配置与密码流程、OAuth 审计挑战条件已验证 | 真实官方 tailnet + Funnel + 云端客户端完成 OAuth 及 nonce 调用 |
| 首次用户体验 | 持久任务时间戳支持本地统计耗时 | 至少 5 名首次使用者走完整流程，记录完成率、卡点及本机 3 分钟目标；分享诊断需用户选择 |

Mac 首次启动验收覆盖空配置目录自动展示向导。客户端自身的工具审批继续生效：无交互 CLI 配置为禁止审批时，连接可以建立而调用被客户端拒绝，任务保持待验证；本次真实调用通过已安装 Codex app-server 的用户发起工具接口完成。

## 执行命令

```sh
go test -race ./... -timeout=300s
go vet ./...
python3 scripts/gen-onboarding-types.py --check
python3 scripts/conformance_check.py
python3 -m unittest discover -s scripts -p 'test_install_distribution.py'
cd ui
npm run check-types
npm run typecheck
npm test
npm run test:e2e
```

原生用户服务验收使用已构建的持久二进制并只创建唯一 `qs-test-*` 注册：

```sh
go build -o /absolute/private-test-directory/xmesh ./cmd/xmesh
XMESH_TEST_ONBOARDING_BINARY=/absolute/private-test-directory/xmesh go test ./cmd/xmesh -run '^TestOnboardingNativeService$' -v -count=1 -timeout=120s
```

Windows 在登录用户的交互会话执行，以 PowerShell 设置同名环境变量。CI 的清理步骤再次核对测试服务的可执行文件路径，仅清理本次运行创建的注册。

仓库完整 Python 脚本套件在本地 Python 3.14/macOS 环境中还有已有的临时路径 `/var` 别名断言和 PTY 并发测试连接重置失败；安装分发和发布门禁专项已独立通过。
