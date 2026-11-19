# MatrixClass macOS Intel/Apple Silicon 适配计划

> **For agentic workers:** 使用 `subagent-driven-development` 或 `executing-plans` 按任务逐项执行；每个可独立验证的功能完成后单独提交 Git commit。

**Goal:** 在不引入 GPL/LGPL/AGPL 传染性依赖的前提下，为 MatrixClass 增加 macOS Intel（`darwin/amd64`）和 Apple Silicon（`darwin/arm64`）教师端、学生端，并完成真实 macOS 环境验证和可分发安装包。

**Architecture:** 保持现有 Go + MyGo + KCP/SMUX 模块结构，新增 Darwin 平台文件和 macOS 原生能力适配层。屏幕采集、音频、远程输入、锁屏和窗口权限优先调用 Apple 系统框架，通过 `purego` 或最小 CGO 桥接动态加载系统 API；业务层只依赖现有媒体、控制和平台接口，不直接耦合 Objective-C/UI 细节。

**Tech Stack:** Go、MyGo Native UI、Darwin system frameworks（ScreenCaptureKit、CoreAudio/AudioToolbox、CoreGraphics、ApplicationServices、IOKit/NSWorkspace）、KCP/SMUX、现有 SQLite；不新增 GPL/LGPL/AGPL 依赖。

---

## 目标与非目标

### 支持目标

- `darwin/amd64`：Intel Mac。
- `darwin/arm64`：Apple Silicon Mac。
- macOS 版本基线建议：`macOS 12.3+`，以便使用 ScreenCaptureKit；更低版本只提供明确的能力降级提示。
- 教师端和学生端均可启动、连接频道、传输文件、收发课堂消息、执行已有课堂控制功能。
- X11/Linux 专用代码不进入 Darwin 构建。
- 保持黑白灰、简洁的 MyGo 原生 UI，不引入 WebView 或 GPL 前端组件。

### 非目标

- 不承诺覆盖所有旧版 macOS。
- 不绕过 macOS 的屏幕录制、麦克风、辅助功能和自动化权限。
- 不通过模拟键盘、隐藏进程或系统注入绕过用户授权。
- 不把合成测试画面标记为真实屏幕采集画面。

## 当前差距

现有非 Windows/非 Linux 路径主要是通用回退实现：

- 屏幕采集：`internal/media/capture_other.go` 会进入不可用状态或测试回退逻辑。
- 音频：`internal/media/audio_other.go` 是通用实现，不是 macOS 真实设备采集。
- 输入：`internal/control/input_other.go` 返回不支持。
- 锁屏：`internal/control/lock_other.go` 只使用 overlay 回退。
- 平台能力：`internal/platform/platform_other.go` 不识别 macOS 桌面、权限和系统服务。
- 构建：当前只有 Linux 多架构脚本，没有 Darwin 双架构构建和 `.app`/`.dmg` 打包。

## 阶段 0：权限与技术基线

### Task 0.1：确定 macOS 支持矩阵

- [ ] 选择最低支持版本，默认 `macOS 12.3`。
- [ ] 建立测试设备矩阵：Intel + Apple Silicon，各至少一台。
- [ ] 记录 macOS 版本、架构、桌面登录会话、显示器数量、音频设备和网卡数量。
- [ ] 确认 Go、MyGo、`purego` 在两种架构上的构建情况。
- [ ] 确认 MyGo 的 macOS 窗口、托盘、置顶和原生菜单行为。

完成标准：确定支持版本和两种架构的构建约束，并写入 README 与发布说明。

### Task 0.2：定义 macOS 权限状态

新增 Darwin 能力状态字段或平台内部状态，至少区分：

- 屏幕录制权限：Screen Recording。
- 麦克风权限：Microphone。
- 辅助功能权限：Accessibility。
- 自动化/应用控制权限：仅在功能确实需要时请求。
- 摄像头权限：当前不作为必选能力，除非后续增加摄像头功能。

要求：

- 启动日志打印 `darwin/amd64` 或 `darwin/arm64`。
- UI 或状态栏显示“需要系统设置授权”的具体能力。
- 权限缺失时返回可识别错误，不自动使用静态合成画面冒充真实能力。
- 提供打开“系统设置 → 隐私与安全性”的入口或明确操作说明。

测试：使用权限正常、权限拒绝、权限撤销后三种状态验证。

Commit：`feat(platform): add macOS capability and permission status`

## 阶段 1：macOS 双架构构建骨架

### Task 1.1：增加 Darwin 平台文件

新增或修改：

- `internal/platform/platform_darwin.go`
- `internal/media/native_screen_darwin.go`
- `internal/control/input_darwin.go`
- `internal/control/lock_darwin.go`
- `internal/media/audio_darwin.go`
- `internal/remoteexec/*_darwin.go`（只在确有平台差异时新增）

使用 `//go:build darwin`，避免与 Windows/Linux 文件冲突。

测试：

- `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -c ./internal/platform`。
- `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c ./internal/platform`。
- 对需要 Apple framework 的实现单独定义 `darwin_native` 构建标签，防止无 macOS SDK 的 Windows 主机误报可用。

### Task 1.2：增加 Darwin 构建脚本

新增：

- `scripts/build-darwin.ps1`
- `scripts/build-darwin.sh`
- `scripts/build-darwin-package.sh`
- `scripts/build-darwin-package.ps1`

当前阶段已完成构建和未签名发布骨架：PowerShell 与 POSIX 脚本均支持 `darwin/amd64`、`darwin/arm64` 教师端/学生端交叉构建；打包脚本会生成独立 `.app`、`Info.plist`、权限说明和可选 Universal 2/DMG。新增 `scripts/check-darwin-build.*` 负责四个架构产物和 SHA-256 校验，新增 `scripts/diagnose-darwin-network.*` 负责接口、路由、DNS、防火墙和教师地址连通性诊断。Windows 主机缺少 `lipo` 或 `hdiutil` 时会清晰跳过对应步骤，不把跨编译结果误报为 macOS 原生能力已验证。

输出：

```text
dist/darwin/
├─ matrixclass-teacher-darwin-amd64
├─ matrixclass-student-darwin-amd64
├─ matrixclass-teacher-darwin-arm64
└─ matrixclass-student-darwin-arm64
```

构建脚本要求：

- 明确记录 `GOOS`、`GOARCH`、`CGO_ENABLED`、SDK 路径和构建标签。
- 交叉编译只用于无原生 framework 依赖的代码检查。
- 最终原生能力必须在 macOS runner 上构建，不能仅凭 Windows 交叉编译宣称可用。

Commit：`build(darwin): add Intel and Apple Silicon build matrix`

## 阶段 2：macOS 屏幕采集

### Task 2.1：定义 Darwin 采集接口

扩展现有 `nativeScreenCapture`，增加以下能力：

- 当前后端名称。
- 权限状态和错误原因。
- 原始分辨率、缩放比例和显示器标识。
- 捕获失败后的重连和权限重新检查。
- 屏幕或窗口采集模式。

业务层继续通过 `media.Manager` 调用，不在教师端/学生端写 macOS API。

### Task 2.2：实现 ScreenCaptureKit 后端

优先使用 ScreenCaptureKit：

- 获取可捕获显示器和窗口列表。
- 请求用户选择屏幕或窗口。
- 采集 BGRA 帧并转换为现有 RGBA `VideoFrame`。
- 保留时间戳、序号、关键帧和变化检测。
- 分辨率变化、显示器拔插、应用窗口关闭后自动重建 stream。
- Screen Recording 权限撤销时停止发送并返回明确错误。

兼容策略：

- macOS 12.3+ 使用 ScreenCaptureKit。
- 低于基线版本不实现隐式降级，直接返回“当前 macOS 版本不支持生产屏幕采集”。
- 不生成静态画面冒充真实采集。

测试：

- 首帧、重复帧 hash、分辨率变化、权限拒绝、采集流断开、重新授权。
- 教师端广播和学生端监控连续运行至少 30 分钟。
- Intel 和 Apple Silicon 分别测试 15/30 FPS。

Commit：`feat(darwin): add ScreenCaptureKit screen capture`

## 阶段 3：macOS 音频

### Task 3.1：实现麦克风采集

使用 CoreAudio 或 AudioToolbox：

- 获取默认输入设备。
- 请求麦克风权限。
- 输出现有 `core.AudioFrame` 的 PCM 格式。
- 处理采样率、声道数和设备变化。
- 设备拔出、默认设备切换后自动恢复。

### Task 3.2：实现系统声音播放和可选回环

- 教师端播放学生端/课堂音频到默认输出设备。
- 处理扬声器、耳机和蓝牙设备切换。
- 不把“系统声音回环”假设为所有 macOS 都可用。
- 如果系统没有合法回环源，能力状态显示不可用，并说明需要用户安装受支持的虚拟音频设备或关闭该功能。

### Task 3.3：音量、静音和生命周期

- 接入已有音量滑块和一键静音状态。
- 采集/播放进程或 AudioUnit 异常时自动重建。
- 停止时释放 AudioUnit、设备监听和回调。
- 加入长时间运行和设备热插拔测试。

Commit：`feat(darwin): add CoreAudio capture and playback`

## 阶段 4：macOS 远程输入

### Task 4.1：实现 Accessibility 权限检测

- 使用 ApplicationServices/AX API 检查辅助功能权限。
- 权限不足时返回明确错误。
- 不自动绕过 TCC 权限。
- 提供打开系统设置的提示。

### Task 4.2：实现 CGEvent 输入后端

支持：

- 鼠标移动。
- 左、中、右键和滚轮。
- 常用键盘按键、组合键和功能键。
- 多显示器坐标转换。
- Retina 缩放坐标转换。

测试：

- Intel/Apple Silicon 各验证鼠标、键盘、滚轮和组合键。
- 验证中英文输入法不被错误吞掉。
- 权限撤销后远程控制停止并上报错误。

Commit：`feat(darwin): add CGEvent remote input backend`

## 阶段 5：锁屏、进程和电源控制

### Task 5.1：macOS 锁屏

优先使用公开系统命令或公开 API：

- `CGSession -suspend` 或系统公开锁屏机制。
- 评估 `pmset displaysleepnow` 是否只用于显示器休眠，不误当作用户会话锁屏。
- 明确区分“锁定会话”和“关闭显示器”。

要求：

- 锁屏失败时回滚控制器状态。
- 教师断联时按现有安全策略解除学生端锁屏。
- 不使用私有、未文档化的安全绕过接口。

### Task 5.2：macOS 进程列表和终止

- 使用 `syscall`/`os.Process` 或系统公开命令枚举进程。
- 仅在权限允许范围内显示进程。
- 终止失败时返回权限和进程状态原因。
- 保持现有管控策略的数据结构和撤销行为。

### Task 5.3：电源控制

- 使用 `shutdown -h now`、`shutdown -r now`、`osascript` 或公开系统机制前先确认权限和交互影响。
- 普通用户无权限时明确提示。
- 教师端执行高风险操作前保持现有确认流程。

Commit：`feat(darwin): add macOS lock process and power adapters`

## 阶段 6：macOS 窗口、托盘和权限引导

### Task 6.1：MyGo 原生 UI 验证

- 验证默认小窗口尺寸。
- 验证手动最大化和窗口拖动。
- 验证独立窗口系统边框、置顶、关闭和焦点。
- 验证悬浮条圆角、自动吸附和触碰展开。
- 验证高分辨率 Retina 渲染和字体缩放。

### Task 6.2：菜单栏图标和右键菜单

- 验证教师端菜单栏图标显示。
- 验证右键菜单、退出、显示主窗口。
- 兼容“控制中心隐藏菜单栏图标”的 macOS 行为。
- 无菜单栏图标时提供主窗口退出入口。

### Task 6.3：权限引导页面

新增 macOS 权限检查页或设置区域：

- 屏幕录制：用于屏幕监控和广播。
- 麦克风：用于语音和课堂音频。
- 辅助功能：用于远程输入。
- 逐项显示状态、原因和打开系统设置按钮。

Commit：`feat(darwin): add permission onboarding and menu bar support`

## 阶段 7：网络和数据路径适配

### Task 7.1：多网卡和网络发现

- 验证 Wi-Fi、USB 网卡、VPN、虚拟网卡共存。
- 选择实际可达的局域网接口进行广播/组播。
- 网络切换后自动重连 KCP。
- 日志记录接口名、地址、频道和重连原因。
- 验证 macOS 防火墙开启/关闭两种状态。

### Task 7.2：macOS 数据目录

- 应用配置、SQLite 数据库、日志和接收文件采用 macOS 规范目录。
- 默认建议：
  - 配置/数据库：`~/Library/Application Support/MatrixClass/`
  - 日志：`~/Library/Logs/MatrixClass/`
  - 缓存：`~/Library/Caches/MatrixClass/`
  - 接收文件：用户桌面或下载目录，遵循应用设置。
- 保留用户显式设置的接收目录。
- 升级时不覆盖现有数据库和用户文件。

Commit：`feat(darwin): use macOS application data directories`

## 阶段 8：`.app`、Universal Binary 和发布包

### Task 8.1：应用包结构

为教师端和学生端生成独立 `.app`：

```text
MatrixClass Teacher.app/
└─ Contents/
   ├─ Info.plist
   ├─ MacOS/matrixclass-teacher
   ├─ Resources/MatrixClass.icns
   └─ Resources/permissions.md
```

`Info.plist` 至少包含：

- Bundle identifier。
- 版本号和构建号。
- `NSMicrophoneUsageDescription`。
- 屏幕录制相关说明和发布文档。
- 菜单栏/应用类型配置。

### Task 8.2：Intel、Apple Silicon 和 Universal Binary

- 分别构建 `darwin/amd64` 和 `darwin/arm64`。
- macOS 主机上使用 `lipo -create` 合并为 Universal 2 二进制。
- 对教师端、学生端分别检查：

```bash
lipo -info MatrixClass\ Teacher.app/Contents/MacOS/matrixclass-teacher
lipo -info MatrixClass\ Student.app/Contents/MacOS/matrixclass-student
```

### Task 8.3：无开发者账号的分发方案

当前没有 Apple Developer 账号，因此第一阶段只实现未签名或本机自签名测试包，不把 Apple 签名和公证作为阻塞条件。

- [ ] 生成未签名教师端和学生端 `.app`。
- [ ] 生成包含两个 `.app`、README 和权限说明的 `.dmg`。
- [ ] 在 README 中说明首次启动方式：Finder 中右键应用，选择“打开”，再确认打开。
- [ ] 对本机自编译包提供可选的 ad-hoc 签名：`codesign --deep --force --sign -`。
- [ ] 验证 ad-hoc 签名只用于本机或测试环境，不能当作公证签名。
- [ ] 仅针对用户明确下载且确认来源的文件提供去除隔离标记说明：`xattr -d com.apple.quarantine <应用路径>`；不建议关闭系统 Gatekeeper。
- [ ] 不把签名证书、私钥或任何凭据提交到仓库。

后续具备 Apple Developer 账号后再增加：

- [ ] Developer ID Application 签名。
- [ ] 使用 `codesign --deep --strict --options runtime` 校验。
- [ ] 使用 `notarytool` 公证并 `stapler` 固化票据。
- [ ] 公证后的正式 `.dmg` 发布流程。

Commit：`build(darwin): package unsigned macOS apps`

## 阶段 9：测试和 CI

### Task 9.1：单元和交叉编译验证

- Windows 主机上验证无原生 API 依赖包的 Darwin 交叉编译。
- macOS Intel runner 执行完整 `go test -tags mygo_nowebview ./...`。
- macOS arm64 runner 执行完整测试和原生能力测试。
- 运行 `git diff --check`。
- 运行 GPL/LGPL/AGPL 许可证扫描。

### Task 9.2：原生能力测试矩阵

每个设备记录：

- macOS 版本。
- Intel 或 Apple Silicon。
- 屏幕录制、麦克风、辅助功能权限。
- 单/多显示器和 Retina 缩放。
- 音频设备和蓝牙耳机。
- Wi-Fi、USB 网卡、VPN 和防火墙状态。

必须测试：

- 教师端屏幕广播。
- 学生端监控连续运行 30 分钟。
- 屏幕录制权限撤销/恢复。
- 麦克风和扬声器设备拔插。
- 远程输入权限撤销/恢复。
- 教师端发送消息后学生端独立提示窗口置顶。
- 文件分发、作业提交和断点续传。
- 网络切换和 KCP 自动重连。
- 应用退出、重启和 SQLite 数据恢复。

### Task 9.3：CI 构建产物

- `darwin/amd64` 构建产物。
- `darwin/arm64` 构建产物。
- Universal 2 `.app`。
- 未签名 `.dmg`。
- 后续可选的签名、公证 `.dmg`。
- SHA-256 校验和和构建日志。

补充工具和证据清单：

- `scripts/check-darwin-build.ps1` / `scripts/check-darwin-build.sh` 检查四个架构产物、生成 `SHA256SUMS.txt`，并可强制要求 Universal 2 或 DMG。
- `scripts/diagnose-darwin-network.ps1` / `scripts/diagnose-darwin-network.sh` 输出接口、路由、DNS、防火墙和教师地址连通性信息。
- `docs/mac-testing-matrix.md` 记录 macOS 真机权限、屏幕、音频、控制、网络、数据恢复和发布验证证据。

## 推荐执行顺序

1. 权限状态和 Darwin 平台能力探测。
2. Darwin 双架构构建骨架。
3. ScreenCaptureKit 屏幕采集。
4. CoreAudio 音频采集与播放。
5. CGEvent 远程输入。
6. 锁屏、进程和电源控制。
7. MyGo 窗口、菜单栏和权限引导。
8. 多网卡、网络重连和 macOS 数据目录。
9. `.app`、Universal 2、未签名 `.dmg` 和首次启动说明。
10. Intel/Apple Silicon 实机矩阵和 macOS CI。

## 验收标准

macOS 适配完成必须同时满足：

- `darwin/amd64` 和 `darwin/arm64` 均能构建教师端与学生端。
- Intel 和 Apple Silicon 真机均能启动并加入同一频道。
- 屏幕采集、音频、远程输入和锁屏权限状态真实可见。
- 权限拒绝不会伪造功能成功，也不会让监控永久卡住。
- Wi-Fi/有线/VPN 切换后连接可恢复。
- `.app` 可正常启动，窗口、菜单栏、独立弹窗和退出行为正常。
- 未签名包有清晰的首次启动、权限授权和 Gatekeeper 说明；具备账号后再验收正式签名包。
- 许可证扫描继续保持 0 GPL/LGPL/AGPL 传染性依赖。
- 所有阶段均有测试证据和独立 Git commit。

> 当前文档中勾选的构建/打包项只表示脚本和 staging 已完成；没有 macOS 真机证据时，不得将 ScreenCaptureKit、CoreAudio、CGEvent、Universal 2、DMG 或网络自动重连标记为已验证。
