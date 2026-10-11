use crate::client::Remote;
use rand::{rngs::OsRng, RngCore};
use serde_json::json;
use std::{
    io::{BufRead, BufReader},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::mpsc::{self, Receiver, Sender, SyncSender},
    thread,
    time::{Duration, Instant},
};
use tempfile::TempDir;

#[derive(Clone)]
pub struct HostConfig {
    pub server: String,
    pub cloudflared: String,
    pub roots: String,
    pub port: u16,
    pub token: String,
    pub bind: String,
    pub unrestricted: bool,
}

pub fn state_dir() -> Result<PathBuf, String> {
    #[cfg(windows)]
    let base = std::env::var_os("LOCALAPPDATA").map(PathBuf::from);
    #[cfg(target_os = "macos")]
    let base =
        std::env::var_os("HOME").map(|p| PathBuf::from(p).join("Library/Application Support"));
    #[cfg(all(not(windows), not(target_os = "macos")))]
    let base = std::env::var_os("XDG_STATE_HOME")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("HOME").map(|p| PathBuf::from(p).join(".local/state")));
    base.map(|p| p.join("mcp-shell"))
        .ok_or_else(|| "无法确定应用配置目录".into())
}

/// Create the per-user state directory with private permissions where the
/// platform supports Unix mode bits.  The directory contains the bearer token
/// and operation logs, so relying on the process umask alone would leave a
/// freshly-created macOS/Linux directory readable by other local users.
fn ensure_state_dir() -> Result<PathBuf, String> {
    let state = state_dir()?;
    std::fs::create_dir_all(&state).map_err(|e| e.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&state, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| e.to_string())?;
    }
    Ok(state)
}

impl HostConfig {
    pub fn defaults() -> Self {
        Self {
            server: binary("mcp-shell"),
            cloudflared: binary("cloudflared"),
            roots: std::env::var(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
                .unwrap_or_else(|_| ".".into()),
            port: 18765,
            token: token(),
            bind: if cfg!(windows) {
                "0.0.0.0"
            } else {
                "127.0.0.1"
            }
            .into(),
            unrestricted: true,
        }
    }
    pub fn load() -> Result<Self, String> {
        let mut config = Self::defaults();
        let state = ensure_state_dir()?;
        let path = state.join("desktop.json");
        if path.exists() {
            let value: serde_json::Value =
                serde_json::from_slice(&std::fs::read(path).map_err(|e| e.to_string())?)
                    .map_err(|e| e.to_string())?;
            for (key, target) in [
                ("server", &mut config.server),
                ("cloudflared", &mut config.cloudflared),
                ("roots", &mut config.roots),
                ("bind", &mut config.bind),
                ("token", &mut config.token),
            ] {
                if let Some(text) = value[key].as_str() {
                    *target = text.into();
                }
            }
            if let Some(port) = value["port"].as_u64().filter(|v| *v > 0 && *v <= 65535) {
                config.port = port as u16;
            }
            config.unrestricted = value["unrestricted"].as_bool().unwrap_or(true);
            if config.token.len() < 16 {
                return Err("已保存令牌无效，请在设置中重置令牌".into());
            }
        } else {
            config.save()?;
        }
        Ok(config)
    }
    pub fn save(&self) -> Result<(), String> {
        use std::io::Write;
        let state = ensure_state_dir()?;
        // NamedTempFile uses 0600 on Unix. Windows inherits the user profile ACL.
        let mut file = tempfile::NamedTempFile::new_in(&state).map_err(|e| e.to_string())?;
        let value = json!({"server":self.server,"cloudflared":self.cloudflared,"roots":self.roots,
            "port":self.port,"token":self.token,"bind":self.bind,"unrestricted":self.unrestricted});
        file.write_all(&serde_json::to_vec_pretty(&value).unwrap())
            .map_err(|e| e.to_string())?;
        file.persist(state.join("desktop.json"))
            .map_err(|e| e.to_string())?;
        Ok(())
    }
}

pub enum Action {
    Start(HostConfig),
    Stop,
    TunnelStart,
    TunnelStop,
}
pub enum Event {
    Running(bool),
    Tunnel(bool),
    Url(String),
    Error(String),
}

pub fn token() -> String {
    let mut bytes = [0u8; 32];
    OsRng.fill_bytes(&mut bytes);
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

pub fn binary(name: &str) -> String {
    let name = format!("{name}{}", std::env::consts::EXE_SUFFIX);
    let mut candidates = vec![];
    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            candidates.push(parent.join(&name));
        }
    }
    candidates.push(PathBuf::from("bin").join(&name));
    candidates.push(PathBuf::from("../bin").join(&name));
    // Finder-launched apps do not inherit the terminal's Homebrew PATH.
    #[cfg(target_os = "macos")]
    for dir in ["/opt/homebrew/bin", "/usr/local/bin"] {
        candidates.push(PathBuf::from(dir).join(&name));
    }
    candidates
        .into_iter()
        .find(|p| p.is_file())
        .and_then(|p| p.canonicalize().ok())
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or(name)
}

fn hidden(command: &mut Command) -> &mut Command {
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x08000000); // CREATE_NO_WINDOW
    }
    command
}

fn spawn(
    command: &mut Command,
    logs: SyncSender<String>,
    source: &'static str,
) -> Result<Child, String> {
    #[cfg(target_os = "macos")]
    {
        let inherited = std::env::var("PATH").unwrap_or_default();
        // Finder-launched apps can inherit an empty or minimal PATH. Keep the
        // Homebrew locations used by cloudflared, but always retain macOS's
        // system directories so zsh and standard tools resolve reliably.
        let mut path =
            String::from("/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin");
        if !inherited.trim().is_empty() {
            path.push(':');
            path.push_str(&inherited);
        }
        command.env("PATH", path);
    }
    let mut child = hidden(command)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("无法启动 {source}：{e}"))?;
    let stderr = child.stderr.take().ok_or("未能读取子进程日志")?;
    thread::spawn(move || {
        for line in BufReader::new(stderr).lines().map_while(Result::ok) {
            // Bound memory and never let a full log queue block the child process.
            let clipped: String = line.chars().take(2048).collect();
            let _ = logs.try_send(format!("[{source}] {clipped}"));
        }
    });
    Ok(child)
}

pub fn tunnel_url(line: &str) -> Option<String> {
    for word in line.split_whitespace() {
        let candidate = word.trim_matches(|c: char| matches!(c, '|' | '"' | '\'' | ','));
        if let Ok(url) = reqwest::Url::parse(candidate) {
            let host = url.host_str()?;
            let label = host.strip_suffix(".trycloudflare.com")?;
            if url.scheme() == "https"
                && !label.is_empty()
                && !label.contains('.')
                && url.username().is_empty()
                && url.password().is_none()
                && url.port().is_none()
                && url.query().is_none()
                && url.fragment().is_none()
                && url.path() == "/"
            {
                return Some(format!("https://{host}/mcp"));
            }
        }
    }
    None
}

pub fn chinese_log(line: &str) -> String {
    if line.starts_with("[cloudflared]") {
        let description = if tunnel_url(line).is_some() {
            "公网地址已分配"
        } else if line.contains("Registered tunnel connection") {
            "隧道连接已建立"
        } else if line.contains("Retrying connection") {
            "正在重连隧道"
        } else if line.contains("ERR") {
            "隧道错误"
        } else if line.contains("WRN") {
            "隧道提示"
        } else if line.contains("Requesting new quick Tunnel") {
            "正在申请公网地址"
        } else {
            "隧道运行信息"
        };
        return format!(
            "[隧道] {description} | {}",
            line.trim_start_matches("[cloudflared] ")
        );
    }
    line.trim_start_matches("[mcp-shell] ").to_owned()
}

pub struct Host {
    server: Child,
    tunnel: Option<Child>,
    config: HostConfig,
    _temp: TempDir,
}

impl Host {
    pub fn start(config: HostConfig, logs: SyncSender<String>) -> Result<Self, String> {
        if config.port == 0 {
            return Err("本地端口不能为 0".into());
        }
        let roots: Vec<_> = config
            .roots
            .lines()
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .map(|p| {
                PathBuf::from(p)
                    .canonicalize()
                    .map_err(|e| format!("目录 {p}：{e}"))
            })
            .collect::<Result<_, _>>()?;
        if roots.is_empty() || roots.iter().any(|p| !p.is_dir()) {
            return Err("请至少填写一个已存在的共享目录，每行一个".into());
        }
        let temp = tempfile::tempdir().map_err(|e| e.to_string())?;
        let state = ensure_state_dir()?;
        let path = temp.path().join("config.json");
        let output = if config.unrestricted {
            state.join("output")
        } else {
            roots[0].join(".tmp/mcp-shell")
        };
        // The secret is passed only in the child's environment, never on disk or argv.
        let data = json!({
            "listen": format!("{}:{}", config.bind, config.port),
            "allowed_ips": if config.bind == "127.0.0.1" { vec!["127.0.0.1"] } else { vec![] },
            "unrestricted":config.unrestricted, "output_dir":output,
            "roots":roots, "command_timeout_sec":60, "max_command_timeout_sec":3600,
            "audit_log":state.join("audit.jsonl"), "log_file":state.join("operations.log")
        });
        std::fs::write(&path, serde_json::to_vec_pretty(&data).unwrap())
            .map_err(|e| e.to_string())?;
        let server = spawn(
            Command::new(&config.server)
                .arg("-config")
                .arg(path)
                .arg("-parent-stdin")
                .env("MCP_SHELL_TOKEN", &config.token),
            logs,
            "mcp-shell",
        )?;
        let mut host = Self {
            server,
            tunnel: None,
            config,
            _temp: temp,
        };
        let remote = Remote::new(
            &format!("http://127.0.0.1:{}", host.config.port),
            &host.config.token,
            Duration::from_secs(1),
        )?;
        let deadline = Instant::now() + Duration::from_secs(10);
        while Instant::now() < deadline {
            if let Some(status) = host.server.try_wait().map_err(|e| e.to_string())? {
                return Err(format!("本地服务启动失败（{status}），请检查端口和日志"));
            }
            if remote.connect().is_ok() {
                return Ok(host);
            }
            thread::sleep(Duration::from_millis(100));
        }
        Err("服务未能在 10 秒内就绪，请检查端口是否被占用".into())
    }

    fn start_tunnel(&mut self, logs: SyncSender<String>) -> Result<(), String> {
        if self.tunnel.is_some() {
            return Ok(());
        }
        self.tunnel = Some(spawn(
            Command::new(&self.config.cloudflared)
                .args(["tunnel", "--no-autoupdate", "--url"])
                .arg(format!("http://127.0.0.1:{}", self.config.port)),
            logs,
            "cloudflared",
        )?);
        Ok(())
    }

    fn stop_tunnel(&mut self) {
        if let Some(mut child) = self.tunnel.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

impl Drop for Host {
    fn drop(&mut self) {
        self.stop_tunnel();
        drop(self.server.stdin.take()); // EOF triggers Go's graceful shutdown, including process trees.
        let deadline = Instant::now() + Duration::from_secs(8);
        while Instant::now() < deadline {
            if matches!(self.server.try_wait(), Ok(Some(_))) {
                return;
            }
            thread::sleep(Duration::from_millis(50));
        }
        let _ = self.server.kill();
        let _ = self.server.wait();
    }
}

pub struct Supervisor {
    pub tx: Option<Sender<Action>>,
    pub events: Receiver<Event>,
    pub logs: Receiver<String>,
    worker: Option<thread::JoinHandle<()>>,
}

impl Supervisor {
    pub fn new() -> Self {
        let (tx, rx) = mpsc::channel();
        let (events_tx, events) = mpsc::channel();
        // A minimized window may stop repainting; logs must not grow without bound.
        let (display_logs, logs) = mpsc::sync_channel(400);
        let worker = thread::spawn(move || {
            let (logs_tx, mut logs_rx) = mpsc::sync_channel(256);
            let mut active_logs = logs_tx;
            let mut host: Option<Host> = None;
            loop {
                match rx.recv_timeout(Duration::from_millis(100)) {
                    Ok(Action::Start(config)) => {
                        if host.is_some() {
                            continue;
                        }
                        // Isolate old process logs from a new sharing session.
                        let channels = mpsc::sync_channel(256);
                        active_logs = channels.0;
                        logs_rx = channels.1;
                        match Host::start(config, active_logs.clone()) {
                            Ok(h) => {
                                host = Some(h);
                                let _ = events_tx.send(Event::Running(true));
                            }
                            Err(e) => {
                                let _ = events_tx.send(Event::Error(e));
                                let _ = events_tx.send(Event::Running(false));
                            }
                        }
                    }
                    Ok(Action::Stop) => {
                        host.take();
                        let _ = events_tx.send(Event::Url(String::new()));
                        let _ = events_tx.send(Event::Tunnel(false));
                        let _ = events_tx.send(Event::Running(false));
                    }
                    Ok(Action::TunnelStart) => {
                        if let Some(h) = &mut host {
                            match h.start_tunnel(active_logs.clone()) {
                                Ok(()) => {
                                    let _ = events_tx.send(Event::Tunnel(true));
                                }
                                Err(e) => {
                                    let _ = events_tx.send(Event::Error(e));
                                    let _ = events_tx.send(Event::Tunnel(false));
                                }
                            }
                        }
                    }
                    Ok(Action::TunnelStop) => {
                        if let Some(h) = &mut host {
                            h.stop_tunnel();
                        }
                        let _ = events_tx.send(Event::Tunnel(false));
                        let _ = events_tx.send(Event::Url(String::new()));
                    }
                    Err(mpsc::RecvTimeoutError::Disconnected) => break,
                    Err(mpsc::RecvTimeoutError::Timeout) => {}
                }
                for _ in 0..256 {
                    let Ok(line) = logs_rx.try_recv() else {
                        break;
                    };
                    if line.starts_with("[cloudflared]")
                        && host.as_ref().is_some_and(|h| h.tunnel.is_some())
                    {
                        if let Some(url) = tunnel_url(&line) {
                            let _ = events_tx.send(Event::Url(url));
                        }
                    }
                    let _ = display_logs.try_send(chinese_log(&line));
                }
                if let Some(h) = &mut host {
                    if let Ok(Some(status)) = h.server.try_wait() {
                        let _ = events_tx.send(Event::Error(format!("本地服务已退出：{status}")));
                        host.take();
                        let _ = events_tx.send(Event::Running(false));
                        let _ = events_tx.send(Event::Tunnel(false));
                        let _ = events_tx.send(Event::Url(String::new()));
                    } else if let Some(tunnel) = &mut h.tunnel {
                        if let Ok(Some(status)) = tunnel.try_wait() {
                            h.tunnel.take();
                            let _ = events_tx
                                .send(Event::Error(format!("隧道已退出：{status}；可重新开启")));
                            let _ = events_tx.send(Event::Tunnel(false));
                            let _ = events_tx.send(Event::Url(String::new()));
                        }
                    }
                }
            }
        });
        Self {
            tx: Some(tx),
            events,
            logs,
            worker: Some(worker),
        }
    }
    pub fn send(&self, action: Action) {
        if let Some(tx) = &self.tx {
            let _ = tx.send(action);
        }
    }
}

impl Drop for Supervisor {
    fn drop(&mut self) {
        self.tx.take();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn parses_only_quick_tunnel_origins() {
        assert_eq!(
            tunnel_url("INF | https://a-b-c.trycloudflare.com |"),
            Some("https://a-b-c.trycloudflare.com/mcp".into())
        );
        for bad in [
            "https://trycloudflare.com",
            "https://a.trycloudflare.com.evil.com",
            "http://a.trycloudflare.com",
            "https://u@a.trycloudflare.com",
            "https://a.trycloudflare.com/path",
        ] {
            assert_eq!(tunnel_url(bad), None);
        }
    }
    #[test]
    fn strong_session_tokens() {
        let a = token();
        assert_eq!(a.len(), 64);
        assert_ne!(a, token());
    }

    #[test]
    #[ignore = "requires MCP_SHELL_TEST_BINARY; local Claude workflow and live log verification"]
    fn real_claude_workflow_live_chinese_logs_and_unrestricted_files() {
        let root = tempfile::tempdir().unwrap();
        let outside = tempfile::tempdir().unwrap();
        let socket = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = socket.local_addr().unwrap().port();
        drop(socket);
        let secret = token();
        let supervisor = Supervisor::new();
        supervisor.send(Action::Start(HostConfig {
            server: std::env::var("MCP_SHELL_TEST_BINARY").expect("set MCP_SHELL_TEST_BINARY"),
            cloudflared: "not-used".into(),
            roots: root.path().to_string_lossy().into_owned(),
            port,
            token: secret.clone(),
            bind: "127.0.0.1".into(),
            unrestricted: true,
        }));
        let deadline = Instant::now() + Duration::from_secs(15);
        let mut ready = false;
        while Instant::now() < deadline {
            match supervisor.events.recv_timeout(Duration::from_millis(200)) {
                Ok(Event::Running(true)) => {
                    ready = true;
                    break;
                }
                Ok(Event::Error(e)) => panic!("{e}"),
                _ => {}
            }
        }
        assert!(ready);
        let remote = Remote::new(
            &format!("http://127.0.0.1:{port}"),
            &secret,
            Duration::from_secs(15),
        )
        .unwrap();
        let init=remote.rpc(1,"initialize",json!({"protocolVersion":crate::client::PROTOCOL,"clientInfo":{"name":"claude-code-test","version":"test"},"capabilities":{}})).unwrap();
        assert!(init["instructions"]
            .as_str()
            .unwrap()
            .contains("UNRESTRICTED MODE"));
        assert!(!init["instructions"]
            .as_str()
            .unwrap()
            .contains("Every path must be inside"));
        // This file deliberately lives outside the configured default working directory.
        let path = outside
            .path()
            .join("outside-default.txt")
            .to_string_lossy()
            .into_owned();
        remote
            .call(
                2,
                "write_file",
                json!({"path":path,"content":"Claude 跨目录测试"}),
            )
            .unwrap();
        assert!(remote
            .call(3, "read_file", json!({"path":path}))
            .unwrap()
            .contains("Claude 跨目录测试"));
        remote.call(4, "delete_path", json!({"path":path})).unwrap();
        let command = if cfg!(windows) {
            "Write-Output '流式测试首行'; Start-Sleep -Seconds 3; Write-Output '流式测试末行'"
        } else {
            "printf '流式测试首行\\n'; sleep 3; printf '流式测试末行\\n'"
        };
        let request = thread::spawn(move || {
            remote.call(
                5,
                "run_command",
                json!({"command":command,"timeout_sec":10}),
            )
        });
        let deadline = Instant::now() + Duration::from_secs(12);
        let mut live = false;
        let mut captured = Vec::new();
        while Instant::now() < deadline {
            if let Ok(line) = supervisor.logs.recv_timeout(Duration::from_millis(100)) {
                if line.contains("[实时输出]") && line.ends_with(" | 流式测试首行") {
                    assert!(
                        !request.is_finished(),
                        "output only appeared after command completion"
                    );
                    live = true;
                }
                let completed = line.contains("[操作完成]") && line.contains("run_command");
                captured.push(line);
                if completed {
                    break;
                }
            }
        }
        assert!(live, "live output missing: {captured:?}");
        assert!(request.join().unwrap().unwrap().contains("exit_code: 0"));
        let text = captured.join("\n");
        assert!(text.contains("[MCP连接]") && text.contains("claude-code-test"));
        assert!(text.contains("[操作开始] 写入文件") && text.contains("[操作完成] 成功"));
        if let Ok(path) = std::env::var("MCP_SHELL_TEST_LOG_CAPTURE") {
            std::fs::write(path, &text).unwrap();
        }
        supervisor.send(Action::Stop);
        drop(supervisor);
        assert!(std::net::TcpStream::connect(("127.0.0.1", port)).is_err());
        println!("Claude workflow, unrestricted file operations, live Chinese logs before completion, and shutdown passed");
    }

    #[test]
    #[ignore = "explicit opt-in: opens a temporary public tunnel to an isolated test root"]
    fn live_public_tunnel() {
        let binary = std::env::var("MCP_SHELL_TEST_BINARY").expect("set MCP_SHELL_TEST_BINARY");
        let tunnel_binary = std::env::var("MCP_SHELL_TEST_TUNNEL_BINARY")
            .expect("set MCP_SHELL_TEST_TUNNEL_BINARY");
        let root = tempfile::tempdir().unwrap();
        let socket = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = socket.local_addr().unwrap().port();
        drop(socket);
        let secret = token();
        let (logs, rx) = mpsc::sync_channel(256);
        let mut host = Host::start(
            HostConfig {
                server: binary,
                cloudflared: tunnel_binary,
                roots: root.path().to_string_lossy().into_owned(),
                port,
                token: secret.clone(),
                bind: "127.0.0.1".into(),
                unrestricted: false,
            },
            logs.clone(),
        )
        .unwrap();
        host.start_tunnel(logs).unwrap();
        let deadline = Instant::now() + Duration::from_secs(45);
        let mut address = None;
        let mut recent = std::collections::VecDeque::new();
        while Instant::now() < deadline {
            if let Ok(line) = rx.recv_timeout(Duration::from_millis(200)) {
                if line.starts_with("[cloudflared]") {
                    address = tunnel_url(&line).or(address);
                }
                recent.push_back(line);
                if recent.len() > 12 {
                    recent.pop_front();
                }
            }
            if address.is_some() {
                break;
            }
        }
        let address = address.unwrap_or_else(|| panic!("No tunnel URL: {recent:?}"));
        let remote = Remote::new(&address, &secret, Duration::from_secs(10)).unwrap();
        let deadline = Instant::now() + Duration::from_secs(60);
        let mut connected = false;
        let mut last_error = String::new();
        while Instant::now() < deadline {
            match remote.connect() {
                Ok(_) => {
                    connected = true;
                    break;
                }
                Err(e) => last_error = e,
            }
            thread::sleep(Duration::from_secs(2));
        }
        assert!(connected, "Public tunnel not reachable: {last_error}");
        let wrong = Remote::new(&address, &token(), Duration::from_secs(10)).unwrap();
        assert!(wrong.connect().unwrap_err().contains("认证失败"));
        let result = remote
            .call(
                1,
                "run_command",
                json!({"command":"hostname","timeout_sec":5}),
            )
            .unwrap();
        assert!(result.contains("exit_code: 0"));
        remote
            .call(
                2,
                "write_file",
                json!({"path":"public-test.txt","content":"公网隧道验证"}),
            )
            .unwrap();
        assert!(remote
            .call(3, "read_file", json!({"path":"public-test.txt"}))
            .unwrap()
            .contains("公网隧道验证"));
        host.stop_tunnel();
        assert!(host.tunnel.is_none());
        drop(host);
        assert!(std::net::TcpStream::connect(("127.0.0.1", port)).is_err());
        println!("Public HTTPS tunnel: auth rejection, command, file roundtrip, and local shutdown verified");
    }

    #[test]
    #[ignore = "requires MCP_SHELL_TEST_BINARY pointing to the built Go server"]
    fn real_server_auth_files_commands_cancellation_and_shutdown() {
        let binary = std::env::var("MCP_SHELL_TEST_BINARY").expect("set MCP_SHELL_TEST_BINARY");
        let root = tempfile::tempdir().unwrap();
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        drop(listener);
        let secret = token();
        let (logs, _rx) = mpsc::sync_channel(256);
        let config = HostConfig {
            server: binary.clone(),
            cloudflared: "missing-cloudflared-for-test".into(),
            roots: root.path().to_string_lossy().into_owned(),
            port,
            token: secret.clone(),
            bind: "127.0.0.1".into(),
            unrestricted: false,
        };
        let mut host = Host::start(config, logs.clone()).expect("start real Go server");
        let url = format!("http://127.0.0.1:{port}/mcp");
        let remote = Remote::new(&url, &secret, Duration::from_secs(40)).unwrap();
        assert!(remote.connect().unwrap().contains("default_shell:"));
        let wrong = Remote::new(&url, &token(), Duration::from_secs(2)).unwrap();
        assert!(wrong.connect().unwrap_err().contains("认证失败"));
        assert!(remote
            .call(1, "read_file", json!({"path":"../outside.txt"}))
            .is_err());
        remote
            .call(
                2,
                "write_file",
                json!({"path":"hello.txt","content":"你好，远程电脑\nsecond line"}),
            )
            .unwrap();
        assert!(remote
            .call(
                3,
                "read_file",
                json!({"path":"hello.txt","offset":1,"limit":1})
            )
            .unwrap()
            .contains("你好，远程电脑"));
        remote
            .call(
                4,
                "edit_file",
                json!({"path":"hello.txt","old_string":"second line","new_string":"updated"}),
            )
            .unwrap();
        remote
            .call(5, "move_path", json!({"src":"hello.txt","dst":"moved.txt"}))
            .unwrap();
        assert!(root.path().join("moved.txt").exists());
        remote
            .call(6, "delete_path", json!({"path":"moved.txt"}))
            .unwrap();
        assert!(!root.path().join("moved.txt").exists());
        let command = if cfg!(windows) {
            "Write-Output 'gui-e2e-你好'"
        } else {
            "printf 'gui-e2e-你好'"
        };
        let result = remote
            .call(
                7,
                "run_command",
                json!({"command":command,"timeout_sec":10}),
            )
            .unwrap();
        assert!(
            result.contains("exit_code: 0") && result.contains("gui-e2e-你好"),
            "{result}"
        );
        assert!(host.start_tunnel(logs).is_err()); // Tunnel failure must leave the local service alive.
        assert!(remote.connect().is_ok());

        let wait_command = if cfg!(windows) {
            "Set-Content cancellation.ready ready; Start-Sleep -Seconds 30"
        } else {
            "touch cancellation.ready; sleep 30"
        };
        let request_remote = remote.clone();
        let request = thread::spawn(move || {
            request_remote.call(
                99,
                "run_command",
                json!({"command":wait_command,"timeout_sec":35}),
            )
        });
        let deadline = Instant::now() + Duration::from_secs(10);
        while !root.path().join("cancellation.ready").exists() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(50));
        }
        assert!(
            root.path().join("cancellation.ready").exists(),
            "command never started"
        );
        let before = Instant::now();
        remote
            .notify(
                "notifications/cancelled",
                json!({"requestId":99,"reason":"integration test"}),
            )
            .unwrap();
        let result = request.join().unwrap().unwrap();
        assert!(result.contains("exit_code: -1"), "{result}");
        assert!(before.elapsed() < Duration::from_secs(10));

        let background = if cfg!(windows) {
            "Start-Sleep -Seconds 60; Set-Content leaked.txt leaked"
        } else {
            "sleep 60; touch leaked.txt"
        };
        let started = remote
            .call(100, "start_process", json!({"command":background}))
            .unwrap();
        let pid: u32 = started
            .split("(pid ")
            .nth(1)
            .unwrap()
            .split(')')
            .next()
            .unwrap()
            .parse()
            .unwrap();
        assert!(remote
            .call(101, "list_processes", json!({}))
            .unwrap()
            .contains("p1"));
        let config_path = host._temp.path().to_owned();
        assert!(!std::fs::read_to_string(config_path.join("config.json"))
            .unwrap()
            .contains(&secret));
        drop(host);
        assert!(!config_path.exists(), "temporary config not removed");
        assert!(
            std::net::TcpStream::connect(("127.0.0.1", port)).is_err(),
            "service survived shutdown"
        );
        #[cfg(windows)]
        {
            let output = hidden(Command::new("tasklist.exe").args([
                "/FI",
                &format!("PID eq {pid}"),
                "/FO",
                "CSV",
                "/NH",
            ]))
            .output()
            .unwrap();
            assert!(
                !String::from_utf8_lossy(&output.stdout).contains(&format!("\"{pid}\"")),
                "background command survived shutdown"
            );
        }
        #[cfg(not(windows))]
        {
            assert!(!Command::new("kill")
                .args(["-0", &pid.to_string()])
                .status()
                .unwrap()
                .success());
        }
    }
}
