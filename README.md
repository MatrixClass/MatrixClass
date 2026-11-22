# 矩联电子教室 MatrixClass（Go + MyGo Native UI + KCP）

基于 Go 与 `github.com/egoist/mygo` 原生 GPU UI（零 WebView、零 GPL/LGPL/AGPL 传染性依赖）打造的模块化局域网电子教室系统，包含教师端（`cmd/teacher`）与学生端（`cmd/student`）。

## 技术栈与许可证合规
- **原生 UI**：`github.com/egoist/mygo`（MIT）+ Direct3D 11 / OpenGL / Metal 渲染，构建启用 `-tags mygo_nowebview`。
- **局域网传输**：纯 Go 自研 MIT KCP ARQ 引擎 + `github.com/xtaci/smux`（MIT）+ `github.com/klauspost/reedsolomon`（MIT）FEC + AES-GCM + UDP 广播自动发现与组播降级。
- **音视频采集与编码**：Windows DXGI Desktop Duplication / GDI 回退、Tile-Delta Zstd 与 Windows Media Foundation H.264、WASAPI 语音与 `.nxlr` 录课。
- **严格开源合规**：运行 `scripts/check_licenses.ps1` 自动校验所有依赖，0 GPL/LGPL/AGPL 传染库。

## 快速构建与测试
```powershell
# 全量单元测试与 KCP 端到端集成测试
go test -tags mygo_nowebview ./...

# 编译教师端与学生端（无 WebView 依赖；-H windowsgui 避免启动时弹出控制台黑框）
go build -tags mygo_nowebview -ldflags "-H windowsgui" -o dist/nexlab-teacher.exe ./cmd/teacher
go build -tags mygo_nowebview -ldflags "-H windowsgui" -o dist/nexlab-student.exe ./cmd/student

# 本地预览界面（演示模式）
.\dist\nexlab-teacher.exe --demo
.\dist\nexlab-student.exe --demo
```

### Linux 多架构构建

当前提供 `linux/amd64`、`linux/arm64` 和 `linux/loong64` 的 `CGO_ENABLED=0` 交叉构建与 tar.gz 发布包：

学生端锁屏页面支持紧急本地解锁。未配置站点专用口令时，默认口令为 `nexlab-unlock`；生产部署建议在运行时配置专用口令，不要继续使用默认口令。

```powershell
.\scripts\build-linux.ps1
# 生成三架构目录包和 tar.gz
.\scripts\build-linux-package.ps1
```

Linux 端当前已支持基础窗口启动、KCP/SMUX 网络、文件传输、课堂消息、Linux Shell 远程命令、XDG `.desktop` 应用列表解析、X11 屏幕采集、X11 远程输入和桌面锁定。X11 与 PipeWire/PulseAudio 能力仍需在对应发行版与桌面环境上进行实机验证；Wayland 会明确返回需要桌面授权的状态，当前不伪造为可用屏幕采集。
