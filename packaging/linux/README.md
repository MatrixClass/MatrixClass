# MatrixClass Linux 发布包

本目录由 `scripts/build-linux-package.ps1` 或 `scripts/build-linux-package.sh` 生成可分发的 Linux 目录包和 `tar.gz` 包。

支持的目标架构（MyGo 提供原生 UI 后端，正式发布）：

- `amd64`：x86_64
- `arm64`：aarch64

仅编译验证（不发布）：`loong64`、`riscv64`、`386`、`arm`（GOARM=7）可以通过 `CGO_ENABLED=0` 交叉编译，但 `github.com/egoist/mygo` v0.9.2 的 Linux 原生后端只覆盖 `amd64 || arm64`，其他架构在启动时返回 `mygo: linux/<arch> is not supported yet`，界面无法打开。如需自行验证可执行 `scripts/build-linux-package.sh dist/linux loong64`。

安装包中包含教师端、学生端、`.desktop` 启动项、SVG 图标和可选的 user-level systemd 模板。安装脚本默认使用 `/opt/matrixclass` 保存真实二进制，使用 `/usr/local/bin` 提供命令入口；可通过 `PREFIX`、`BINDIR`、`DESKTOPDIR`、`ICONDIR`、`DATADIR`、`CONFIGDIR` 和 `LOGDIR` 覆盖路径。

配置、数据库、日志和接收文件默认保存在用户目录下，不随卸载删除：

- 数据：`$XDG_DATA_HOME/matrixclass`，默认 `~/.local/share/matrixclass`
- 配置：`$XDG_CONFIG_HOME/matrixclass`，默认 `~/.config/matrixclass`
- 日志：`$XDG_STATE_HOME/matrixclass`，默认 `~/.local/state/matrixclass`

`INSTALL_SYSTEMD=1` 时会安装 `matrixclass-teacher.service` 和 `matrixclass-student.service` 到用户 systemd 目录，但不会自动启用。模板中的 `%h/.local/bin/...` 适用于将程序放在用户目录的场景；系统级 `/opt` 安装请通过桌面启动项或自行调整 `ExecStart`。

发行版包：在 Linux 主机执行 `scripts/build-linux-distro-packages.sh dist/linux amd64`。存在 `dpkg-deb` 时生成 `.deb`，存在 `rpmbuild` 时生成 `.rpm`；工具缺失会清晰跳过，不代表安装包已经生成。Windows PowerShell 脚本仅在检测到 WSL 时调用上述脚本。

图标策略：当前提供可缩放 SVG，桌面环境可直接使用；PNG 多尺寸导出需要 `rsvg-convert` 或 `inkscape`，未安装时不自动伪造 PNG。发布前可生成 `64/128/256/512` PNG 并放入对应 `hicolor/<size>x<size>/apps/` 目录。

X11 屏幕采集与远程输入需要可用的 `DISPLAY`、`libX11.so.6` 和 `libXtst.so.6`；Wayland 当前会明确提示需要桌面授权，尚未宣称支持无授权采集。局域网发现可能需要允许 UDP 广播/组播和 KCP 监听端口。
