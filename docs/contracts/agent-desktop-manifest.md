# agent-desktop 引擎清单（schema v1）

清单是桌面引擎 `agent-desktop` 的唯一信任来源。它位于 `internal/computer/adengine/manifest.json`，随 xmesh 编译进二进制（`go:embed`）。Host 只运行 SHA-256 与清单或本机安装回执一致的文件；清单之外的版本一律不用。

## 字段

| 字段 | 类型 | 谁维护 | 说明 |
| --- | --- | --- | --- |
| `schema_version` | 整数 | 人工 | 固定为 `1`。 |
| `package` | 字符串 | 人工 | 固定为 `agent-desktop`。 |
| `version` | `X.Y.Z` | 上游 CI | 钉住的上游版本，也是 `engine update` 唯一可安装的版本。 |
| `license` | 字符串 | 上游 CI | 上游许可证，当前为 `Apache-2.0`。 |
| `tarball.url` | https 地址 | 上游 CI | 上游 npm 包的 tarball 地址。 |
| `tarball.integrity` | `sha512-<base64>` | 上游 CI | 取自 registry 的 `dist.integrity`，安装前校验整个 tarball。 |
| `tarball.published_at` | 字符串 | 上游 CI | 上游发布时间，仅供参考。 |
| `platforms` | 对象 | 上游 CI | 键为平台，见下文。每个值含 `binary` 与 `helpers`。 |
| `platforms.<键>.binary` | `{member, sha256}` | 上游 CI | 主程序在 tarball 中的位置与 SHA-256。 |
| `platforms.<键>.helpers` | `[{member, sha256}]` | 上游 CI | 辅助程序，与主程序放在同一目录。Windows 没有辅助程序。 |
| `validation.validated_versions` | `[X.Y.Z]` | **只能人工加入** | 已验收的版本。CI 从不添加或删除。 |
| `validation.validated_platforms` | `[键]` | 人工 | 可选。列出已验收的平台；缺省表示所有有产物的平台。 |
| `validation.record` | 路径 | 上游 CI 设为新版本的记录 | 验收记录所在的仓库路径。 |
| `upstream.latest` | `X.Y.Z` | 上游 CI | 可选。仅在首次发现上游新版本时写入，不做每日改动。 |

## 约定

- **平台键**沿用 npm 风格：`darwin-arm64`、`darwin-x64`、`linux-arm64`、`linux-x64`、`win32-arm64`、`win32-x64`。Go 侧把 `windows` 映射为 `win32`，`amd64` 映射为 `x64`。
- **member** 是 tarball 中的路径，已去掉顶层的 `package/`，例如 `bin/agent-desktop-linux-x64`。路径必须是干净的相对路径，不允许 `..` 或绝对路径。
- **安装文件名**由 member 去掉末尾的 `-<平台键>` 得到，`.exe` 保留。例如 `bin/agent-desktop-macos-helper-darwin-arm64` 安装为 `agent-desktop-macos-helper`，`bin/agent-desktop-win32-x64.exe` 安装为 `agent-desktop.exe`。辅助程序与主程序同名分发，CLI 依赖这一点。
- **只钉一个版本**。升级时 CI 只改 `version`、`tarball`、`platforms`、`validation.record` 和 `upstream.latest`，其余字段原样保留。新版本在人工把它加入 `validated_versions` 之前不会被使用。

## 门控语义

- 版本可用：`version` 在 `validated_versions` 中，且当前平台在 `validated_platforms` 中（缺省为全部平台）。否则 `engine_status` 为 `version_unsupported`，Host 回退 native。
- `engine update` 只安装可用的钉住版本。
- 自动更新只准备同一 `major.minor` 的补丁版本，且只在已验收、非 macOS 的设备上进行（见 `docs/contracts/computer.md`）。

## 示例（节选）

```json
{
  "schema_version": 1,
  "package": "agent-desktop",
  "version": "1.0.2",
  "license": "Apache-2.0",
  "tarball": {
    "url": "https://registry.npmjs.org/agent-desktop/-/agent-desktop-1.0.2.tgz",
    "integrity": "sha512-…",
    "published_at": "2026-10-30T21:18:45.761Z"
  },
  "platforms": {
    "darwin-arm64": {
      "binary": { "member": "bin/agent-desktop-darwin-arm64", "sha256": "bf0a…" },
      "helpers": [{ "member": "bin/agent-desktop-macos-helper-darwin-arm64", "sha256": "5228…" }]
    }
  },
  "validation": {
    "validated_versions": ["1.0.2"],
    "validated_platforms": ["darwin-arm64"],
    "record": "docs/validation/2026-11-01-agent-browser-desktop-p0.md"
  },
  "upstream": { "latest": "1.1.0" }
}
```

完整文件见 `internal/computer/adengine/manifest.json`。

## 验收记录

`validated_platforms: ["darwin-arm64"]` 的含义是部分验收：P0 只在 Mac 上验证了安装与 CLI 版本，桌面截图与快照仍受系统授权限制，未测。Mac 授权后补测的结果应写入新的验收记录，并据此调整 `validated_platforms`。
