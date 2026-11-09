# agent-desktop 1.2.0 验收记录

状态：**待验收（pending）**。由 `scripts/agent_desktop_upstream.py` 生成。人工验收通过之前，不要把 `1.2.0` 加入 `validation.validated_versions`。

## 来源

- 上游包：`agent-desktop@1.2.0`
- npm dist-tag `latest`：1.2.0
- 发布时间：2026-11-05T02:45:51.044Z
- tarball：https://registry.npmjs.org/agent-desktop/-/agent-desktop-1.2.0.tgz
- integrity：`sha512-sp8hHs8JfnuZe8ZQwiKZrqSOlu26SSeZXkjDog4KsU31L09G3uZJa8pITGN2vORif9uuh0iOSJHD146CEyTqag==`（已与 registry 比对）
- 钉住版本：1.0.2 → 1.2.0
- 校验范围：tarball 的 sha512；六个平台的二进制与 helper 逐一比对 `.sha256` 侧车。没有运行任何上游二进制，也没有运行 postinstall。

## 六个平台的 SHA-256

| 平台 | 二进制 | sha256 | helper | helper sha256 |
| --- | --- | --- | --- | --- |
| darwin-arm64 | `bin/agent-desktop-darwin-arm64` | `dec75e0bba0d1f9b589bdb88188770ad1a199cde9e02cf0a5b94175491f738e9` | `bin/agent-desktop-macos-helper-darwin-arm64` | `4a795a40ba7fc93b726b0834342f0225779c30d83e28f1f219db479860c38a78` |
| darwin-x64 | `bin/agent-desktop-darwin-x64` | `5dae755fee4af37d1a245f058139b262d358352cce15aeb49d0100ff46b12156` | `bin/agent-desktop-macos-helper-darwin-x64` | `bdf060658b93f326efa06ac24d521e88025bf88109cecc257fa7fa40e65801cd` |
| linux-arm64 | `bin/agent-desktop-linux-arm64` | `915de59a8b3d22c9ad33023740bbcc5fe0b4fa830de9587bbf761237eafeb76f` | `bin/agent-desktop-linux-clipboard-owner-linux-arm64` | `a6bfde6778dadcf9fd04ba94de17cdf205eb0b23b643c3f34073d264d2117cc3` |
| linux-x64 | `bin/agent-desktop-linux-x64` | `da52a1eb2152cdc21b756ab20e6208d242da8062c73cd20bc8a8ae2a0384392c` | `bin/agent-desktop-linux-clipboard-owner-linux-x64` | `3e96f92b805c0d1f9de309935d62f4189b8e11e1385ad022bb878487bb3942ec` |
| win32-arm64 | `bin/agent-desktop-win32-arm64.exe` | `f1577d9f8cb6c9b1e767584fed96c72fbe1591fefbd67100965607d1a337a2fb` | — | — |
| win32-x64 | `bin/agent-desktop-win32-x64.exe` | `d274667031322e50b9c2b1ab81c2ca46835d52cc932769a5c34bd4d481fd94c3` | — | — |

## 未映射的上游文件

- 无

## 验收清单

- [ ] macOS arm64 与 x64：P0 矩阵（snapshot、ref_act、中文输入、拖拽），核对 ref 的 stale 语义
- [ ] macOS：替换二进制后，辅助功能与屏幕录制授权是否需要重新授予
- [ ] Windows：P0 矩阵，确认 Notepad 快照超时是否复现
- [ ] Linux：参数语义与映射清单一致
- [ ] 对照上游变更（README、发布说明）；新命令保持未映射、不暴露
- [ ] 验收结论与证据补在本文件；通过后再把版本加入 `validation.validated_versions`
