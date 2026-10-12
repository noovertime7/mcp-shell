# MCP Shell · Claude 主机服务

你始终在虚拟机里与 Claude Code 对话。宿主机和办公 Mac 分别运行本程序，为同一个 Claude 提供两个独立 MCP 服务。GUI 负责服务启停、连接配置和中文实时日志，界面跟随系统的浅色 / 深色模式。

```text
                         ┌─ HTTP / 局域网 ── 宿主机 MCP Shell（host-pc）
虚拟机中的 Claude Code ──┤
                         └─ HTTP / 局域网 ── 办公 Mac MCP Shell（office-mac）
```

GUI 默认开放当前系统用户可访问的全部文件和所有命令工具，工具调用直接执行，没有逐次确认或危险命令审批。操作系统权限仍生效，不会自动提权。默认目录仅用于相对路径和命令工作目录；绝对路径可指向任何可访问目录。

## 1. 宿主机（Windows）

解压 `dist/mcp-shell-desktop-windows-x64.zip`，运行 `mcp-shell-gui.exe`。保留旁边的 `mcp-shell.exe`。

1. 点击「启动服务」。Windows 默认监听 `0.0.0.0:18765`，允许虚拟机通过宿主机网卡 IP 直连，仍需访问令牌。
2. 打开「连接配置」，服务名称用 `host-pc`，选择「局域网 / 虚拟机」。
3. 从网卡列表选择虚拟机能访问的宿主机地址：VMware NAT 常用 VMnet8，Host-only 常用 VMnet1，桥接则用局域网 IP。不要填写虚拟机自己的 IP 或 `127.0.0.1`。
4. 点击「复制接入命令」，在虚拟机终端粘贴执行。

如果 Windows 防火墙阻止连接，在系统提示中允许服务，或自行对虚拟机 IP 放行 TCP 18765；程序不会自动修改系统防火墙。不要将这个明文 HTTP 端口直接映射到公网。

源码构建（Go 1.20+、Rust 1.88+、Visual Studio C++ Build Tools）：

```powershell
.\build-gui.ps1                 # 编译并打包
.\start-gui.ps1 -NoBuild
```

## 2. 办公电脑（macOS）

macOS 源码构建脚本支持 Apple Silicon 与 Intel，会按当前 Mac 架构生成包含 GUI 和 Go 服务的 `.app` ZIP。

在 Mac 准备 Xcode Command Line Tools，以及 Go 和 Rust（1.88+）。如果使用 Homebrew：

```bash
xcode-select --install                    # 已安装则无需重复
brew install go rust
cd /你的路径/mcp-shell
bash build-gui-macos.sh
```

如果希望从 Finder 双击构建脚本，首次使用前执行 `chmod +x build-gui-macos.sh build-macos.command`；也可以始终在终端用上面的 `bash` 命令运行。构建脚本会把当前架构的 `mcp-shell` 和 GUI 一起放进 `.app`，不会修改系统服务或安装目录。

构建完成后解压 `dist/mcp-shell-desktop-macos-arm64.zip`（Apple Silicon）或 `dist/mcp-shell-desktop-macos-amd64.zip`（Intel），打开 `MCP Shell.app`。这是本地 ad-hoc 签名包，未经过 Apple 公证；系统若阻止打开，请通过系统提供的应用打开流程处理。仓库还提供 `.github/workflows/desktop-macos.yml`，可在 GitHub Actions 手动构建两个架构的应用；仅新增流程文件，不代表已经运行该流程。

1. 打开 `MCP Shell.app`，点击「启动服务」。Mac 默认只监听 `127.0.0.1:18765`。
2. Claude 也运行在这台 Mac 时，在「连接配置」中选择「本机连接」。
3. Claude 运行在另一台电脑或虚拟机时，先停止服务，在「服务设置」中选择「局域网」并保存，再启动服务。回到「连接配置」，选择「局域网 / 虚拟机」，填写这台 Mac 的可达 IPv4 地址。
4. 服务名称使用 `office-mac`，复制接入命令到 Claude 所在电脑的终端执行。复制内容包含真实访问令牌；预览默认隐藏令牌。

也可在终端解压和打开（Apple Silicon）：

```bash
ditto -x -k dist/mcp-shell-desktop-macos-arm64.zip dist
open "dist/MCP Shell.app"
```

客户端必须能访问所选主机地址。仅本机监听时，GUI 不会生成可复制的局域网连接配置。

Mac 默认使用 `zsh`。从 Finder 启动时会补充 Homebrew 和系统常用程序路径。受 macOS 系统隐私权限保护的文件（如桌面、文稿等）需要在「系统设置 → 隐私与安全性 → 文件与文件夹」中为 `MCP Shell.app` 授予访问权限；若办公电脑需要跨目录执行开发工具，可在同一处授予「完全磁盘访问权限」后重启应用。

## 3. 虚拟机 Claude 同时接入两台机器

GUI 生成的命令等价于（下方占位符替换成真实值）：

```bash
claude mcp add --scope user --transport http host-pc \
  http://宿主机IP:18765/mcp --header 'Authorization: Bearer 宿主机令牌'

claude mcp add --scope user --transport http office-mac \
  http://Mac局域网IP:18765/mcp --header 'Authorization: Bearer Mac令牌'
```

然后在 Claude 中用 `/mcp` 检查两个服务。直接对 Claude 说：

> 用 host-pc 检查宿主机的项目文件，再用 office-mac 查看办公 Mac 上的项目。

MCP 服务端不会弹出操作确认。Claude Code 自身若还要求工具授权，将以下片段**合并**到虚拟机的 `~/.claude/settings.json`，保留原有其他配置：

```json
{
  "permissions": {
    "allow": ["mcp__host-pc__*", "mcp__office-mac__*"]
  }
}
```

GUI 的「已有配置与工具授权」中可复制当前服务名称对应的权限片段。已有更优先的 deny、ask 或组织策略仍由 Claude Code 处理。本程序不会修改虚拟机上无法访问的设置。

配置格式来自 [Claude MCP 官方文档](https://code.claude.com/docs/en/mcp) 与 [工具权限官方文档](https://code.claude.com/docs/en/permissions)。已有同名 MCP 配置时，先运行 `claude mcp remove office-mac --scope user`，再执行更新后的接入命令。

## 中文实时日志

「服务控制台」启动后持续展示：

- 客户端接入：来源、Claude 客户端名称、MCP 协议版本。
- 操作开始：读取、写入、修改、删除、移动、搜索、命令执行等中文操作名，以及路径、命令、工作目录。
- 实时输出：前台命令和后台进程的 stdout / stderr 在收到数据时显示，不必等命令结束。被执行程序自身缓冲的输出仍需等待它刷新。
- 操作完成：成功/失败、耗时、退出码和修改摘要。
- 服务状态：启动、就绪、停止和异常退出。

支持跟随最新、暂停显示、关键字筛选、复制和清空显示。UI 最多保留 2000 条；暂停显示会冻结当前画面，后台仍接收最新日志，恢复后切换到最新缓冲区。高吞吐或窗口停止刷新时，有限的内存队列可能丢弃显示条目，完整操作日志仍写入磁盘。系统原始错误和程序输出保持原文，分类和状态使用中文。

日志不会主动输出访问令牌，但命令本身和 stdout/stderr 会被记录。配置与令牌保存在 `desktop.json`，重启保持不变；点击「更换访问令牌」后需更新 Claude 配置。macOS/Linux 的状态目录会设置为仅当前用户可读写（`0700`），配置文件为 `0600`。

状态目录：

| 系统 | 目录 |
|---|---|
| Windows | `%LOCALAPPDATA%\mcp-shell` |
| macOS | `~/Library/Application Support/mcp-shell` |
| Linux | `$XDG_STATE_HOME/mcp-shell` 或 `~/.local/state/mcp-shell` |

`operations.log` 是中文操作日志，`audit.jsonl` 是结构化审计记录，`output` 保存长命令输出。自定义限制目录模式下长输出放在首个目录的 `.tmp/mcp-shell`。Unix 配置文件权限为 0600，Windows 继承用户配置目录权限。

## 连接与服务生命周期

程序提供本机和局域网 HTTP 连接，不提供内置公网穿透。跨网络使用需要自行建立可信网络连接；不要把 HTTP 监听端口直接映射到公网。

停止服务或正常关闭 GUI 会取消活动命令并清理后台进程。意外强杀 GUI 后，Go 服务会检测 stdin 关闭并退出。程序不添加开机启动项或系统服务；Mac 休眠时远程连接不可用。

旧版配置里的隧道程序字段会被忽略，下次保存时移除；服务端口、工作目录和访问令牌继续沿用。

## 验证

Windows 项目目录执行：

```powershell
go test ./...
go vet ./...
go build -o bin\mcp-shell.exe ./cmd/mcp-shell
cargo test --locked --manifest-path gui/Cargo.toml
cargo clippy --locked --manifest-path gui/Cargo.toml --all-targets -- -D warnings
$env:MCP_SHELL_TEST_BINARY = (Resolve-Path bin\mcp-shell.exe).Path
cargo test --locked --manifest-path gui/Cargo.toml real_server_auth_files_commands_cancellation_and_shutdown -- --ignored --nocapture
cargo test --locked --manifest-path gui/Cargo.toml real_claude_workflow_live_chinese_logs_and_unrestricted_files -- --ignored --nocapture
Remove-Item Env:MCP_SHELL_TEST_BINARY
```

两项集成测试使用临时目录和本机回环服务，验证鉴权、取消、进程清理、默认目录之外的文件操作，以及命令完成之前已收到中文实时输出。不会连接用户的真实 Claude 或 Mac。
