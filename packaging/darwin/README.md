# MatrixClass macOS 未签名发布包

本目录由 `scripts/build-darwin-package.ps1` 或 `scripts/build-darwin-package.sh` 生成 macOS `.app` 目录和可选 `.dmg`。

## 支持架构

- `darwin/amd64`：Intel Mac
- `darwin/arm64`：Apple Silicon Mac
- Universal 2：仅在有 `lipo` 的 macOS 环境中生成

最低版本暂定为 macOS 12.3。Windows 主机可以交叉构建无 Apple 原生 framework 依赖的检查产物，但屏幕采集、音频、辅助功能等原生能力必须在真实 macOS 上继续适配和验证。

## 首次打开

当前包未使用 Apple Developer 账号签名或公证。首次运行时，在 Finder 中右键 `.app`，选择“打开”，然后在系统提示中再次确认“打开”。不要为了运行未知来源程序而关闭 Gatekeeper。

开发机上可选使用本机 ad-hoc 签名：

```sh
codesign --deep --force --sign - "MatrixClass Teacher.app"
codesign --deep --force --sign - "MatrixClass Student.app"
```

ad-hoc 签名只适合本机和测试环境，不等同于 Developer ID 签名或 Apple 公证。仅对确认来源的文件，在确有需要时移除隔离标记：

```sh
xattr -d com.apple.quarantine "/path/to/MatrixClass Teacher.app"
```

## 权限

屏幕广播、监控、课堂音频和远程输入需要的 macOS 权限及当前阶段限制见 `permissions.md`。本发布骨架不会绕过系统权限，也不会把静态画面当成真实采集结果。

## 许可证

本项目继续使用现有 Go + MyGo 依赖，未新增 GPL、LGPL 或 AGPL 传染性依赖。发布前请运行仓库中的许可证检查脚本。
