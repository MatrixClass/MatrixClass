# MatrixClass MyGo fork

This directory is based on `github.com/egoist/mygo` v0.9.2.

Local changes:

- Extend Linux native-backend build constraints to include `loong64`.
- Add the LoongArch64 Linux `memfd_create` syscall number used by the video backend.

The upstream MIT license and notices are retained in this directory. The fork is
used through the root module's `replace` directive so Linux LoongArch builds are
reproducible without modifying the Go module cache.

Cross-compilation does not replace real-device validation. A LoongArch Linux
desktop must still be used to verify GTK/WebKitGTK, OpenGL, screen capture,
audio, tray integration, and the distribution's runtime libraries.
