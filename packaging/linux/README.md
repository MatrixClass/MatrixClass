# MatrixClass Linux 发布包

本目录由 `scripts/build-linux-package.ps1` 或 `scripts/build-linux-package.sh` 生成可分发的 Linux 目录包和 `tar.gz` 包。

支持的目标架构：

- `amd64`：x86_64
- `arm64`：aarch64
- `loong64`：loongarch64

安装包中包含教师端、学生端、`.desktop` 启动项和 SVG 图标。安装脚本默认使用 `/opt/matrixclass` 保存真实二进制，使用 `/usr/local/bin` 提供命令入口；可通过 `PREFIX`、`BINDIR`、`DESKTOPDIR` 和 `ICONDIR` 覆盖路径。

X11 屏幕采集与远程输入需要可用的 `DISPLAY`、`libX11.so.6` 和 `libXtst.so.6`；Wayland 当前会明确提示需要桌面授权，尚未宣称支持无授权采集。局域网发现可能需要允许 UDP 广播/组播和 KCP 监听端口。
