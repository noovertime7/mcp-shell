# mcp-shell

一个跑在开发机上的 MCP 服务器。局域网里另一台机器（例如虚拟机）上的 Claude Code 通过 Streamable HTTP 连接它，在这台机器上读写代码、执行编译测试命令、启动开发服务。

    客户端 (Claude Code) ──HTTP + Bearer token──▶ mcp-shell :8765/mcp ──▶ roots 内的项目目录

- Go 1.20 即可编译，只用标准库，没有第三方依赖（不需要 go.sum，不用联网）。
- 主要面向 Windows（PowerShell / cmd），也能在 macOS（zsh）和 Linux（bash）上运行。
- MCP 协议层在 `internal/mcp`：Streamable HTTP、无会话、每个 POST 直接返回 `application/json`，实现 initialize、ping、tools/list、tools/call、notifications/cancelled。

## Rust 桌面 GUI 与公网隧道

新增原生 **Rust + egui** 主机服务控制台。宿主机与办公 Mac 各运行一份，虚拟机里的 Claude Code 通过 MCP 操作这两台电脑；GUI 管理服务、Cloudflare 隧道，并实时显示中文操作日志和命令输出。

```powershell
.\build-gui.ps1
.\start-gui.ps1 -NoBuild
```

在宿主机启动 MCP 服务，复制局域网连接命令到虚拟机；在办公 Mac 启动服务与隧道，复制隧道连接命令到同一台虚拟机。分别命名 `host-pc` 与 `office-mac`，之后始终与虚拟机中的 Claude 对话即可。GUI 默认开放全部文件访问和命令工具，不做逐次确认；令牌与配置保存后可跨重启复用。Windows 产物在 `dist\mcp-shell-desktop`；Mac 使用 `bash build-gui-macos.sh` 构建 `.app`。见 [完整使用说明](docs/desktop.md)。

## 工具

| 工具 | 作用 |
|---|---|
| `list_roots` | 系统、允许的根目录、路径别名、默认 shell |
| `list_dir` | 列目录（可多层），`.git`、`node_modules` 不展开 |
| `read_file` | 带行号读取，支持 offset/limit，拒绝二进制 |
| `write_file` | 新建或整体覆写（原子写入） |
| `edit_file` | 精确字符串替换，默认要求唯一；保留 CRLF 和 BOM |
| `delete_path` / `move_path` | 删除、移动/重命名；限制目录模式下保护根目录 |
| `glob` / `grep` | 按 glob 找文件、按正则搜内容 |
| `run_command` | 执行命令并等待，超时或客户端取消时杀掉整个进程树 |
| `start_process` / `read_process_output` / `list_processes` / `stop_process` | 后台进程（dev server、watcher 等） |

## 安全边界

- CLI 默认限制目录模式：文件路径与命令 cwd 必须落在 `roots` 内，解析 symlink/junction 后检查。GUI 默认 `unrestricted: true`：开放系统当前用户可访问的文件路径，`roots` 仅决定默认目录；也可在设置中切换为限制目录模式。
- `/mcp` 需要 `Authorization: Bearer <token>`；`allowed_ips` 限制来源 IP（支持 CIDR）。
- `run_command` 可以执行任意命令，命令本身仍能访问 roots 之外的路径。原始 HTTP 服务只应在可信局域网或回环地址使用，不直接暴露到互联网；公网连接使用 GUI 管理的 HTTPS 隧道。详见 [使用说明](docs/desktop.md#隧道与服务生命周期)。
- 每次工具调用和被拒绝的请求都写入 `audit_log`（JSON lines；`content`/`old_string`/`new_string` 只记长度）。

## 安装

从 [Releases](https://github.com/noovertime7/mcp-shell/releases) 下载对应平台的压缩包，或自己编译：

```powershell
go build -o bin\mcp-shell.exe .\cmd\mcp-shell     # Go 1.20+
Copy-Item config.example.json config.json
.\start.ps1 -NewToken                              # 生成 token，填进 config.json
notepad config.json                                # 填 listen、allowed_ips、roots
.\bin\mcp-shell.exe -config config.json            # 启动；-version 打印版本
```

macOS / Linux：

```bash
go build -o bin/mcp-shell ./cmd/mcp-shell
cp config.example.json config.json
openssl rand -hex 24                               # 生成 token
./bin/mcp-shell -config config.json
```

`start.ps1` 等价于“编译 + 用 config.json 启动”，`-NoBuild` 跳过编译，`-Go <go.exe>` 指定编译器。

`config.json`（含 token）已在 `.gitignore` 中，不要提交。token 也可以用环境变量 `MCP_SHELL_TOKEN` 提供。

Windows 防火墙只放行客户端（管理员 PowerShell）：

```powershell
New-NetFirewallRule -DisplayName "mcp-shell 8765" -Direction Inbound -Protocol TCP -LocalPort 8765 -RemoteAddress <客户端IP> -Action Allow
```

### 配置项

| 键 | 说明 |
|---|---|
| `listen` | 监听地址，填本机局域网 IP，例如 `192.168.1.10:8765` |
| `token` | 至少 16 个字符 |
| `allowed_ips` | 允许的客户端 IP / CIDR，空表示不限制 |
| `roots` | 允许访问的根目录，第一个是相对路径和默认 cwd 的基准 |
| `unrestricted` | CLI 默认 false；GUI 默认 true，开放当前系统用户可访问的全部文件，roots 仅作为默认目录 |
| `path_aliases` | 客户端路径前缀 → 本机路径，例如 `/mnt/hgfs/ws` → `D:\ws`，客户端用自己看到的路径也能访问 |
| `default_shell` | Windows：`powershell`（默认）、`pwsh`、`cmd`；macOS 默认 `zsh`；Linux 默认 `bash` |
| `command_timeout_sec` / `max_command_timeout_sec` | `run_command` 默认超时 / 允许的最大超时 |
| `max_output_bytes` | stdout、stderr 各自返回给模型的上限（默认 25000），超出保留头尾，完整输出存到 `output_dir` |
| `max_read_bytes` | `read_file`、`grep` 单次返回的上限（默认 60000），超出提示用 offset 续读 |
| `max_result_bytes` | 任何工具单次结果的硬上限（默认 60000）。Claude Code 默认拒收超过约 25k token 的 MCP 结果（`MAX_MCP_OUTPUT_TOKENS`） |
| `output_dir` | 被截断的命令输出、后台进程的完整日志（默认 `<第一个root>/.tmp/mcp-shell`，`-` 关闭），启动时清理 3 天前的日志 |
| `audit_log` | 审计日志路径（JSON lines），空表示不记录 |
| `log_file` | 操作日志路径（人可读，同时输出到控制台），空表示只输出到控制台 |

### 操作日志

GUI、控制台和 `log_file` 实时记录中文操作开始、命令输出与完成状态，例如：

```
2026/10/11 21:30:01 [MCP连接] 客户端已连接 | 来源=192.168.1.20:51234 | 客户端=claude-code 2.x
2026/10/11 21:30:05 [操作开始] 修改文件 (edit_file) 路径=D:\projects\web\src\a.ts
2026/10/11 21:30:05 [操作完成] 成功 | 修改文件 (edit_file) | 耗时=8ms | 删除=1行 新增=2行
       - 修改前: const a = 1
       + 修改后: const a = 2 const b = 3
2026/10/11 21:30:06 [操作开始] 执行命令 (run_command) 命令="go test ./..."
2026/10/11 21:30:08 [实时输出] go test ./... · 标准输出 | ok package/example
2026/10/11 21:30:09 [操作完成] 成功 | 执行命令 (run_command) | 耗时=3s
       => exit_code: 0
2026/10/11 21:31:00 [后台进程] p1 已退出 | 退出码=1 | 命令=pnpm dev
```

读类工具（read_file、grep 等）只记参数，不记内容；改动类工具记结果，edit_file 还记旧/新片段。

## 在 Claude Code 中接入

```bash
curl -s http://<服务器IP>:8765/healthz        # 应返回 ok mcp-shell 0.1.0
claude mcp add --transport http shell http://<服务器IP>:8765/mcp \
  --header "Authorization: Bearer <token>" --scope user
```

然后在 Claude Code 里用 `/mcp` 查看连接状态，工具名形如 `mcp__shell__run_command`。

- 客户端开着代理（如 clash TUN）时，把服务器 IP 加入直连规则，或设置 `NO_PROXY=<服务器IP>`。
- 很长的命令超过 Claude Code 的 MCP 工具超时时，启动前设置 `MCP_TOOL_TIMEOUT`（毫秒），或改用 `start_process` + `read_process_output`。
- 输出被截断时结果里有 `full_output: <路径>`，可以用 `read_file` 或 `grep` 查看完整日志。
- 服务器重启后旧的 MCP 会话失效，在 Claude Code 里执行 `/mcp` 重新连接。

## 开发

```bash
go vet ./...
go test ./...
```

目录：`cmd/mcp-shell`（入口）、`internal/mcp`（MCP 协议层）、`internal/config`、`internal/pathguard`（路径边界）、`internal/fstools`（文件工具）、`internal/exectools`（命令与后台进程）、`internal/server`（工具注册、认证、审计、操作日志）。

## 许可

MIT
