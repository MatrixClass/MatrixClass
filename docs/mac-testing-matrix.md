# MatrixClass macOS 原生测试矩阵

本文档用于区分“Windows 交叉编译通过”和“macOS 原生能力已验证”。没有 Apple Developer 账号不影响本地真机测试，但未签名应用仍需要 Finder 右键打开并手动授权。

## 测试设备

| 设备 | 架构 | macOS | 显示器 | 音频设备 | 网络接口 | 防火墙 | 结果 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Intel Mac | `amd64` | 待填写 | 待填写 | 待填写 | Wi-Fi/有线/VPN | 开/关 | 待测 |
| Apple Silicon Mac | `arm64` | 待填写 | 待填写 | 待填写 | Wi-Fi/有线/VPN | 开/关 | 待测 |

每台设备记录：

```sh
sw_vers
uname -m
system_profiler SPDisplaysDataType SPAudioDataType SPNetworkDataType
```

## 构建与产物检查

```sh
scripts/check-darwin-build.sh dist/darwin
```

macOS 上生成 Universal 2 后必须确认两个架构都存在：

```sh
lipo -info "dist/darwin/apps/universal/MatrixClass Teacher.app/Contents/MacOS/matrixclass-teacher"
lipo -info "dist/darwin/apps/universal/MatrixClass Student.app/Contents/MacOS/matrixclass-student"
shasum -a 256 -c dist/darwin/SHA256SUMS.txt
```

没有 `lipo` 或 `hdiutil` 的机器只能验证架构二进制和 `.app` staging，不能记录为 Universal 2 或 DMG 已生成。

## 权限测试

每项分别测试“首次允许、拒绝、撤销后重启”三种状态：

| 权限 | 使用功能 | 预期 |
| --- | --- | --- |
| Screen Recording | 屏幕广播、屏幕监控 | 允许后取得真实帧；拒绝时显示明确原因，不显示合成画面 |
| Microphone | 麦克风采集 | 允许后收到 PCM；拒绝时课堂音频明确不可用 |
| Accessibility | 远程鼠标、键盘、滚轮 | 允许后输入生效；拒绝时返回权限错误 |
| Automation | 仅在确实控制其他应用时 | 未使用时不请求 |

入口：系统设置 → 隐私与安全性。修改权限后完全退出并重新打开教师端或学生端，再记录结果。

## 屏幕与窗口

- 教师端广播到学生端后连续运行 30 分钟，记录首帧耗时、帧率、断帧和重连次数。
- 单显示器、双显示器、Retina 缩放 100%/200% 分别验证。
- 拔插显示器或改变分辨率后，采集流重新建立且不显示旧画面。
- 学生端监控窗口保持置顶、可拖动、可关闭；权限撤销后不假报正常。
- 验证教师端默认小窗口、最大化、独立弹窗边框和菜单栏/托盘退出行为。

## 音频

- 麦克风允许/拒绝、默认设备切换、蓝牙耳机连接/断开。
- 教师端扬声器和麦克风音量滑块、静音按钮状态保持一致。
- 音频服务或设备重启后，采集/播放能停止并给出可理解的恢复状态。
- 系统声音回环必须标记为实际可用或不可用，不得把静音数据当作回环成功。

## 远程输入、锁屏和控制

- 验证鼠标移动、左右键、中键、滚轮、常用组合键和中英文输入法。
- 验证 Retina、多显示器坐标和权限撤销后的停止行为。
- 验证锁屏失败不会把控制器状态误记为已锁屏。
- 进程终止、电源操作在普通用户权限下显示失败原因，不绕过系统授权。

## 网络与 KCP

在 macOS 真机执行：

```sh
CHANNEL_ID=1001 TEACHER_ADDRESS=192.168.1.10 PORT=9000 \
  scripts/diagnose-darwin-network.sh /tmp/matrixclass-network.txt
```

检查项目：

- 只有 Wi-Fi、有线、Wi-Fi+有线、VPN/虚拟网卡四种组合。
- 教师端选择的频道 ID、监听地址和实际接口一致。
- 防火墙开启和关闭各测试一次，记录系统弹窗和入站规则。
- 切换 Wi-Fi 与有线、断开再恢复 VPN，KCP 能自动重连且不会重复创建教师会话。
- 教师端日志包含接口、地址、频道和重连原因。
- 学生端启动后能根据频道 ID 自动加入，错误频道能明确提示。

## 文件、消息和数据恢复

- 文件分发、文件夹分发、断点续传、自动覆盖和接收路径权限。
- 学生端下载队列显示进度、失败重试和默认不自动打开。
- 消息弹窗、作业提交、随堂测验窗口强制置顶且可拖动关闭。
- 教师端 SQLite 数据、设置、日志和缓存重启后保持；升级不覆盖用户数据。

## 记录格式

每次测试至少保存：设备、架构、macOS 版本和权限截图；网络诊断输出；`build-info.txt`、`SHA256SUMS.txt`；关键日志、失败时间、复现步骤和是否自动恢复。

## 当前状态

- 已完成：Darwin 平台权限状态骨架、双架构交叉构建脚本、未签名 `.app`/可选 DMG 打包骨架、控制层安全错误边界。
- 未完成：ScreenCaptureKit、CoreAudio、CGEvent 的真实 macOS 原生实现及 Intel/Apple Silicon 真机验证。
- 未完成：Universal 2、DMG、网络切换和权限矩阵的 macOS 真机证据。
- Windows 交叉编译通过不等于上述原生能力可用。
