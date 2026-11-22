# MatrixClass Windows 发布包

本目录由 `scripts/build-windows.ps1` 或 `scripts/build-windows.sh` 生成，内含：

- `matrixclass-teacher.exe`：教师端
- `matrixclass-student.exe`：学生端

两个程序均为 `-H windowsgui` 构建的纯 Go GUI 程序（`CGO_ENABLED=0`，`-tags mygo_nowebview`），启动时不弹出控制台窗口，无需安装运行库，解压即可运行。

## 支持架构

| 包名 | 架构 | 说明 |
| --- | --- | --- |
| `matrixclass-windows-amd64.zip` | x64 | Windows 10 1809+ / Windows 11 |
| `matrixclass-windows-arm64.zip` | ARM64 | Windows 11 on ARM（骁龙等），原生运行 |

`windows/386`（32 位）可以编译，但 MyGo 没有 32 位原生 UI 后端，程序启动即报 `not supported yet`，因此不发布。

## 首次运行

- 程序未做 Authenticode 代码签名，SmartScreen 可能提示“已保护你的电脑”，点击“更多信息 → 仍要运行”。
- 首次启动时 Windows 防火墙会询问网络访问权限，请允许“专用网络”，否则局域网发现（UDP 广播/组播）和 KCP 连接会失败。
- 命令行参数（如 `--demo`、`--channel`、`--listen`、`--teacher`）可在快捷方式“目标”中追加。

## 许可证

全部依赖为 MIT/BSD/Apache 等宽松许可证，发布前由 `scripts/check_licenses.ps1` 校验无 GPL/LGPL/AGPL 依赖。
