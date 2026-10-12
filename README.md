# mcp-shell

一个跑在开发机上的 MCP 服务器。局域网里另一台机器（例如虚拟机）上的 Claude Code 通过 Streamable HTTP 连接它，在这台机器上读写代码、执行编译测试命令、启动开发服务。

    客户端 (Claude Code) ──HTTP + Bearer token──▶ mcp-shell :8765/mcp ──▶ roots 内的项目目录

- Go 1.20 即可编译，只用标准库，没有第三方依赖（不需要 go.sum，不用联网）。
- 主要面向 Windows（PowerShell / cmd），也能在 macOS（zsh）和 Linux（bash）上运行。
- MCP 协议层在 `internal/mcp`：Streamable HTTP、无会话、每个 POST 直接返回 `application/json`，实现 initialize、ping、tools/list、tools/call、notifications/cancelled。

## Rust 桌面 GUI

新增原生 **Rust + egui** 主机服务控制台。宿主机与办公 Mac 各运行一份，虚拟机里的 Claude Code 通过 MCP 操作这两台电脑；GUI 管理服务启停与客户端连接配置，并实时显示中文操作日志和命令输出，外观跟随系统明暗模式。

```powershell
.\build-gui.ps1
.\start-gui.ps1 -NoBuild
```

在宿主机或办公 Mac 启动 MCP 服务，在「连接配置」中选择本机连接或局域网 / 虚拟机，再复制接入命令到 Claude 所在电脑。支持 macOS 顶部菜单栏状态常驻、关闭窗口隐藏至后台运行、Caffeinate 保持唤醒（防休眠断网）、网卡智能识别（Tailscale / 局域网）与一键切换监听，以及可视化「运行中进程」管理面板（实时查看、向 stdin 发送输入、一键终止）。Mac 默认仅监听本机，跨电脑连接时可一键切换为局域网监听。分别命名 `host-pc` 与 `office-mac`，之后始终与虚拟机中的 Claude 对话即可。GUI 默认开放全部文件访问和命令工具，不做逐次确认；令牌与配置保存后可跨重启复用。Windows 产物在 `dist\mcp-shell-desktop`；Mac 使用 `bash build-gui-macos.sh` 构建 `.app`。见 [完整使用说明](docs/desktop.md)。

## 工具

| 工具 | 作用 |
|---|---|
| `list_roots` | 系统、允许的根目录、路径别名、默认 shell、临时文件目录（`scratch_dir`）、PATH 上有哪些开发工具 |
| `list_dir` | 列目录（可多层），`.git`、`node_modules` 不展开 |
| `read_file` | 带行号读取，支持 offset/limit，拒绝二进制 |
| `write_file` | 新建或整体覆写（原子写入） |
| `edit_file` | 精确字符串替换，默认要求唯一；保留 CRLF 和 BOM |
| `multi_edit` | 同一文件多处替换一次完成，按顺序应用；任何一处不匹配则整个文件不写入 |
| `delete_path` / `move_path` | 删除、移动/重命名；限制目录模式下保护根目录 |
| `glob` / `grep` | 按 glob 找文件、按正则搜内容；默认跳过 `.gitignore` 忽略的文件（`dist/` 等构建产物），`include_ignored=true` 可搜 |
| `run_command` | 执行命令并等待，超时或客户端取消时杀掉整个进程树；超过 `max_call_wait_sec` 仍在运行则转为后台进程，结果里给出续读方式 |
| `start_process` / `read_process_output` / `list_processes` / `stop_process` | 后台进程（dev server、watcher 等）；`read_process_output` 支持 `until_exit`（等到退出）、`wait_for`（等到输出匹配正则）、`tail`（只看末尾） |
| `write_process_input` | 向运行中的后台进程标准输入（stdin）写入文本/按键，可选择关闭 stdin |
| `notify_user` | 在主机屏幕弹出原生系统通知（如构建或长任务完成时提醒），支持标题、内容与提示音 |
| `get_clipboard` / `set_clipboard` | 读取与写入主机系统剪贴板，支持跨电脑传递代码、Token 或错误日志 |
| `capture_screen` | macOS 静音截屏，可保存至临时目录或指定路径，供多模态模型查看屏幕 UI 与报错 |
| `open_target` | 在主机默认浏览器打开 URL（如本地服务），或在访达/资源管理器打开文件与目录 |
| `inspect_port` | 检查主机 TCP 端口占用（显示监听进程 PID 与名称），支持传入 `kill: true` 释放端口 |
| `http_request` | 由主机发起 HTTP/HTTPS 请求，用于本地回环接口联调与服务健康检查 |
| `system_info` | 系统版本、用户、CPU、内存、磁盘、开机时长、代码页、时区，以及 git/go/node/pnpm/python/java/docker 等工具的路径和版本 |
| `get_env` | 查看环境变量：全部、按关键字过滤或按名称；PATH 类变量逐行列出并标出不存在的目录；Windows 上 `scope=user/machine` 查看注册表里的值；疑似密钥的值默认隐藏，`MCP_SHELL_TOKEN` 永不显示 |
| `operation_history` | 查看历史工具调用（跨重启、跨会话持久保存）：按工具、关键字、失败、仅改动、时间过滤；传 `id` 查看单次调用的参数和输出开头 |

## 安全边界

- CLI 默认限制目录模式：文件路径与命令 cwd 必须落在 `roots` 内，解析 symlink/junction 后检查。GUI 默认 `unrestricted: true`：开放系统当前用户可访问的文件路径，`roots` 仅决定默认目录；也可在设置中切换为限制目录模式。
- `/mcp` 需要 `Authorization: Bearer <token>`；`allowed_ips` 限制来源 IP（支持 CIDR）。
- `run_command` 可以执行任意命令，命令本身仍能访问 roots 之外的路径。HTTP 服务只应在可信局域网或回环地址使用，不直接暴露到互联网。程序不提供内置公网穿透；跨网络使用需要自行建立可信网络连接。详见 [使用说明](docs/desktop.md#连接与服务生命周期)。
- 每次工具调用和被拒绝的请求都写入 `audit_log`（JSON lines；`content`/`old_string`/`new_string` 只记长度，`env` 参数的值不记录）。
- 命令不会继承 `MCP_SHELL_TOKEN`（GUI 用它把令牌交给服务），打印环境变量也看不到访问令牌。
- 操作历史（`history_dir`）保存命令全文和改动类工具输出的开头，和日志一样只存在本机；不需要时设 `"history_dir": "-"` 关闭。

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
| `history_dir` | 操作历史目录，每天一个 JSON lines 文件，供 `operation_history` 查询（默认 `<output_dir>/history`，`-` 关闭） |
| `history_days` | 操作历史保留天数（默认 30） |
| `max_call_wait_sec` | 单次工具调用最长等待（默认 45）：`run_command` 超过后转为后台进程继续运行（仍受超时限制），`read_process_output` 单次最多等这么久。应小于客户端和中间代理的请求超时；设为负数关闭转后台 |

### Windows PowerShell 5.1 的默认行为

- 文件类命令（`Get-Content`、`Set-Content`、`Add-Content`、`Out-File`、`>`、`Select-String`、`Import-Csv`、`Export-Csv`）默认使用 UTF-8，避免中文源码乱码；需要精确字节时用 `write_file`。
- 原生命令往重定向的 stderr 写内容（git 的 CRLF 提示、`2>&1`、`2>$null`）不再被当成失败；cmdlet 出错仍返回非 0。
- 退出码按有符号数显示（`-1` 而不是 `4294967295`）。
- 每条命令都会合并注册表里的最新 PATH 和新增的环境变量，安装新工具后不用重启 mcp-shell。

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

读类工具（read_file、grep 等）只记参数，不记内容；改动类工具记结果，edit_file 还记旧/新片段。命令超过 `max_call_wait_sec` 转为后台时记为“转入后台”。

### 操作历史

每次工具调用都追加到 `history_dir/<日期>.jsonl`：时间、工具、参数（文件内容只记长度）、结果（成功/失败/超时/转入后台、退出码）、耗时、客户端（来源 IP + User-Agent），改动类工具还保存输出开头。AI 通过 `operation_history` 查询，例如：

```
history: C:\Users\me\AppData\Local\mcp-shell\output\history (kept 30 days); calls 1-3 of 128 matching, newest first
#128  15:28:31  run_command         FAILED exit=128   21.1s  git push origin master  (cwd E:\ws\platform)
#127  15:27:02  multi_edit          ok                 9ms  E:\ws\web\src\views\a.vue  (2 edits)
#126  15:26:40  edit_file           ok                 7ms  E:\ws\web\src\api\b.ts
```

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
