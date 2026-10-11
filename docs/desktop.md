# MCP Shell · Claude 主机服务

你始终在虚拟机里与 Claude Code 对话。宿主机和办公 Mac 分别运行本程序，为同一个 Claude 提供两个独立 MCP 服务。GUI 负责启停、连接配置、隧道和中文实时日志。

```text
                         ┌─ HTTP / 局域网 ── 宿主机 MCP Shell（host-pc）
虚拟机中的 Claude Code ──┤
                         └─ HTTPS / 公网隧道 ── 办公 Mac MCP Shell（office-mac）
```

GUI 默认开放当前系统用户可访问的全部文件和所有命令工具，工具调用直接执行，没有逐次确认或危险命令审批。操作系统权限仍生效，不会自动提权。默认目录仅用于相对路径和命令工作目录；绝对路径可指向任何可访问目录。

## 1. 宿主机（Windows）

解压 `dist/mcp-shell-desktop-windows-x64.zip`，运行 `mcp-shell-gui.exe`。保留旁边的 `mcp-shell.exe` 和 `cloudflared.exe`。

1. 点击「启动 MCP 服务」。Windows 默认监听 `0.0.0.0:18765`，允许虚拟机通过宿主机网卡 IP 直连，仍需访问令牌。
2. 打开「接入 Claude」，服务名称用 `host-pc`，选择「虚拟机直连宿主机」。
3. 从网卡列表选择虚拟机能访问的宿主机地址：VMware NAT 常用 VMnet8，Host-only 常用 VMnet1，桥接则用局域网 IP。不要填写虚拟机自己的 IP 或 `127.0.0.1`。
4. 点击「复制接入命令（含令牌）」，在虚拟机终端粘贴执行。

如果 Windows 防火墙阻止连接，在系统提示中允许服务，或自行对虚拟机 IP 放行 TCP 18765；程序不会自动修改系统防火墙。不要将这个明文 HTTP 端口直接映射到公网。

源码构建（Go 1.20+、Rust 1.88+、Visual Studio C++ Build Tools）：

```powershell
.\install-cloudflared.ps1       # 下载官方隧道程序到本仓库，并校验 SHA-256
.\build-gui.ps1                 # 编译并打包
.\start-gui.ps1 -NoBuild
```

## 2. 办公电脑（macOS）

macOS 源码构建脚本支持 Apple Silicon 与 Intel，会按当前 Mac 架构生成包含 GUI、Go 服务和 cloudflared 的 `.app` ZIP。当前 Windows 开发环境不能执行 macOS GUI，Mac 的实际运行验收需在 Mac 上完成。

在 Mac 准备 Xcode Command Line Tools，以及 Go、Rust、cloudflared。如果使用 Homebrew：

```bash
xcode-select --install                    # 已安装则无需重复
brew install go rust cloudflared
cd /你的路径/mcp-shell
bash build-gui-macos.sh
```

如果希望从 Finder 双击构建脚本，首次使用前执行 `chmod +x build-gui-macos.sh build-macos.command`；也可以始终在终端用上面的 `bash` 命令运行。构建脚本会把当前架构的 `mcp-shell`、GUI 和 `cloudflared` 一起放进 `.app`，不会修改系统服务或安装目录。

构建完成后解压 `dist/mcp-shell-desktop-macos-arm64.zip`（Apple Silicon）或 `dist/mcp-shell-desktop-macos-amd64.zip`（Intel），打开 `MCP Shell.app`。这是本地 ad-hoc 签名包，未经过 Apple 公证；系统若阻止打开，请通过系统提供的应用打开流程处理。仓库还提供 `.github/workflows/desktop-macos.yml`，可在 GitHub Actions 手动构建两个架构的应用；仅新增流程文件，不代表已经运行该流程。

1. 打开 GUI，点击「启动 MCP 服务」。Mac 默认只监听 `127.0.0.1:18765`。
2. 点击「开启公网隧道」，等待生成 HTTPS 地址。
3. 打开「接入 Claude」，服务名称使用 `office-mac`，选择「通过公网隧道连接」。
4. 复制接入命令，到同一台虚拟机里执行。

Mac 默认使用 `zsh`。从 Finder 启动时会补充 Homebrew 和系统常用程序路径。受 macOS 系统隐私权限保护的文件（如桌面、文稿等）需要在「系统设置 → 隐私与安全性 → 文件与文件夹」中为 `MCP Shell.app` 授予访问权限；若办公电脑需要跨目录执行开发工具，可在同一处授予「完全磁盘访问权限」后重启应用。

## 3. 虚拟机 Claude 同时接入两台机器

GUI 生成的命令等价于（下方占位符替换成真实值）：

```bash
claude mcp add --scope user --transport http host-pc \
  http://宿主机IP:18765/mcp --header 'Authorization: Bearer 宿主机令牌'

claude mcp add --scope user --transport http office-mac \
  https://Mac隧道域名/mcp --header 'Authorization: Bearer Mac令牌'
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

GUI 也提供这个权限片段的复制按钮。服务名若自定义，需要同步替换规则中的名称。已有更优先的 deny、ask 或组织策略仍由 Claude Code 处理。本程序不会修改虚拟机上无法访问的设置。

配置格式来自 [Claude MCP 官方文档](https://code.claude.com/docs/en/mcp) 与 [工具权限官方文档](https://code.claude.com/docs/en/permissions)。已有同名 MCP 配置时，先运行 `claude mcp remove office-mac --scope user`，再执行更新后的接入命令。

## 中文实时日志

「服务控制台」启动后持续展示：

- 客户端接入：来源、Claude 客户端名称、MCP 协议版本。
- 操作开始：读取、写入、修改、删除、移动、搜索、命令执行等中文操作名，以及路径、命令、工作目录。
- 实时输出：前台命令和后台进程的 stdout / stderr 在收到数据时显示，不必等命令结束。被执行程序自身缓冲的输出仍需等待它刷新。
- 操作完成：成功/失败、耗时、退出码和修改摘要。
- 服务和隧道状态：隧道地址申请、连接建立、错误和重连。

支持跟随最新、暂停显示、关键字筛选、复制和清空显示。UI 最多保留 2000 条；暂停或高吞吐时内存队列可能丢弃显示条目，完整操作日志仍写入磁盘。系统原始错误、程序输出、隧道详细信息保持原文，分类和状态使用中文。

日志不会主动输出访问令牌，但命令本身和 stdout/stderr 会被记录。配置与令牌保存在 `desktop.json`，重启保持不变；点击「更换访问令牌」后需更新 Claude 配置。macOS/Linux 的状态目录会设置为仅当前用户可读写（`0700`），配置文件为 `0600`。

状态目录：

| 系统 | 目录 |
|---|---|
| Windows | `%LOCALAPPDATA%\mcp-shell` |
| macOS | `~/Library/Application Support/mcp-shell` |
| Linux | `$XDG_STATE_HOME/mcp-shell` 或 `~/.local/state/mcp-shell` |

`operations.log` 是中文操作日志，`audit.jsonl` 是结构化审计记录，`output` 保存长命令输出。自定义限制目录模式下长输出放在首个目录的 `.tmp/mcp-shell`。Unix 配置文件权限为 0600，Windows 继承用户配置目录权限。

## 隧道与服务生命周期

Cloudflare Quick Tunnel 不需要路由器端口映射，被控 Mac 主动建立出站连接。HTTPS 在 Cloudflare 终止，因此 Cloudflare 是流量中转的受信任方。Quick Tunnel 是临时地址，重开隧道后可能变化；固定地址可自行配置命名隧道，源站指向本机回环端口，然后把固定地址填入 Claude 配置。

关闭隧道只关闭公网入口；停止服务或正常关闭 GUI 会停止隧道、取消活动命令并清理后台进程。意外强杀 GUI 后 Go 服务能检测 stdin 关闭并退出；cloudflared 可能残留，需手动结束，此时它的本地源站已退出。程序不添加开机启动项或系统服务；Mac 休眠时远程连接不可用。

Quick Tunnel 使用与网络要求见 [Cloudflare 官方文档](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)。

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

需要真实公网隧道测试时，同时设置 `MCP_SHELL_TEST_BINARY` 和 `MCP_SHELL_TEST_TUNNEL_BINARY`，运行 `cargo test --locked --manifest-path gui/Cargo.toml live_public_tunnel -- --ignored --nocapture`。该测试只通过隧道公开限制在临时目录的测试服务，结束后清理。
