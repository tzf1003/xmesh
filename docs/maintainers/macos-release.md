# 在本机签名和公证 Mac 安装包

本机发布使用钥匙串中的 **Developer ID Application** 证书及 `notarytool` 凭据。Xcode 的 Apple Accounts → 团队 → Manage Certificates → ＋ 可创建证书。证书需要带有本机可用的私钥；创建资格由 Apple 团队权限决定。

在 Apple 账户网站的“登录和安全 → 应用专用密码”创建用于公证的密码，然后在终端运行：

```sh
xcrun notarytool store-credentials xmesh-notary --apple-id YOUR_APPLE_ID --team-id YOUR_TEAM_ID
```

在终端的安全提示中输入应用专用密码。命令默认向 Apple 验证后保存到钥匙串。密码由维护者直接输入，勿放入命令参数、聊天、日志或仓库。

运行 `security find-identity -v -p codesigning` 找到 Developer ID Application 对应的 SHA-1。在 `~/Library/Application Support/xmesh/release.json` 保存引用：

```json
{"identity":"YOUR_40_CHARACTER_CERTIFICATE_SHA1","notary_profile":"xmesh-notary"}
```

此文件只记录引用。也可以使用 `CSC_NAME`（证书 SHA-1）和 `APPLE_KEYCHAIN_PROFILE` 环境变量。`APPLE_KEYCHAIN` 可指定自定义钥匙串。证书私钥和公证密码由钥匙串管理。

在 `ui/` 中执行：

```sh
node scripts/package-macos-local.mjs --check
node scripts/package-macos-local.mjs 0.1.0-beta.5
```

脚本先验证证书和公证认证，再构建并签署内核、应用和 DMG，提交公证、附加票据，最后运行签名、票据和 Gatekeeper 验证。运行需要已安装的 Go、Node.js、Xcode 命令行工具和 `ui/node_modules`。版本号同时写入内核与应用。首次签名可能出现钥匙串授权提示，由维护者完成。

产物位于 `ui/release/`。只有脚本成功退出且系统验证通过，才能将其标记为已签名、公证的安装包。记录架构、版本、SHA-256 和验证结果后，再通过正式发布流程分发。

GitHub Actions 使用仓库机密中的 `.p12` 和公证认证，沿用 `.github/workflows/ui-package.yml`。本机配置不会自动上传证书、私钥或密码。
