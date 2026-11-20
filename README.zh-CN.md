# EchoManager

[English](README.md) · [下载发行版](https://github.com/in4pira10n/EchoManager/releases)

EchoManager 是一个本地优先的进程管理器。通过浏览器登记和管理长期运行的应用，查看进程状态、PID、端口和日志。后端使用 Go，界面使用原生 JavaScript 和 EchoKit。

## 功能

- 登记、编辑、启停和移除实例，查看启动时间及标准输出和标准错误日志。
- 管理本机和远程设备。macOS 与 Linux 通过 SSH 连接，Windows 通过 HTTPS WinRM 连接。
- 创建多步骤快捷指令，按顺序启动、停止、重启、打开或切换实例。每一步都可以按实例状态设置执行条件。
- 支持裁剪实例图标、调整主题色，以及跟随系统、浅色和深色外观。

实例直接使用配置中的可执行文件、参数和环境变量启动。EchoManager 通过 TCP 端口判断服务是否就绪。关闭管理器会保留受管进程。

## 安装与运行

1. 从 [GitHub Releases](https://github.com/in4pira10n/EchoManager/releases) 下载适合系统和 CPU 的压缩包并解压。发行页提供 `SHA256SUMS.txt` 供校验。
2. 准备独立的 [EchoKit checkout](https://github.com/in4pira10n/EchoKit)。EchoManager v0.0.3 请使用 EchoKit 的 `f8d4a3f` 或之后的版本，以使用更新后的 SF Symbols。把 EchoKit 放在程序旁边，或用 `ECHOKIT_DIR` 指定其根目录。
3. 运行 `EchoManager` 或 Windows 上的 `EchoManager.exe`，在浏览器打开 `http://127.0.0.1:3000`。首次启动时，配置 EchoKit 根目录并创建第一个管理员账号。

从源码运行需要 Go 1.24 或更新版本。如果 EchoKit 位于相邻目录 `../EchoKit`，可在项目根目录运行：

```sh
go run .
```

在可执行文件旁可放置 `config.json`，字段见 [config.example.json](config.example.json)。环境变量 `ECHOKIT_DIR`、`PORT` 和 `ECHOMANAGER_DATA_DIR` 优先于配置文件。Windows 发行包在后台运行，启动信息保存在数据目录的 `logs/manager.log`。

## 远程设备与数据

- SSH 主机密钥需提前录入管理端的 `~/.ssh/known_hosts`。远程 Unix 设备需要 Bash。
- Windows 设备需要启用 HTTPS WinRM，默认端口为 `5986`。可使用 [WinRM 配置脚本](scripts/configure-winrm-https.ps1)，默认证书校验应保留。
- 默认数据目录为 `.echomanager-data`，包含实例、设备、账号、会话、快捷指令、设置、图标和日志。备份时请保留同目录下的 `device-secrets.key`，用于解密保存的设备密码。
- 内置服务器使用 HTTP 并监听所有网卡。请在可信网络内使用，并用主机防火墙限制访问。

## 开发

`internal/server/` 包含服务端、进程控制和测试。`web/` 包含嵌入可执行文件的 Web 界面。应用文本集中在 `web/static/strings/zh-CN.json`，详见[文本资源指南](web/static/strings/README.md)。

```sh
go test ./...
go vet ./...
mkdir -p build
go build -o build/EchoManager .
```

如需了解更多构建、设备配置及存储细节，请参阅 [English README](README.md)。
