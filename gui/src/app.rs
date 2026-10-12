use crate::caffeinate::CaffeinateGuard;
use crate::client::Remote;
use crate::dashboard::{self, DaySummary};
use crate::host::{self, Action, Event, HostConfig, Supervisor};
use crate::tray::{TrayCommand, TrayManager};
use eframe::egui::{self, Color32, RichText, TextEdit};
use serde_json::json;
use std::{
    collections::{HashMap, VecDeque},
    net::Ipv4Addr,
    path::PathBuf,
    time::{Duration, Instant},
};

const BLUE: Color32 = Color32::from_rgb(45, 99, 221);
const MAX_LOGS: usize = 2000;

#[derive(Clone, serde::Deserialize, Default)]
pub struct DesktopProc {
    pub id: String,
    #[serde(default)]
    pub name: String,
    pub command: String,
    #[serde(default)]
    pub shell: String,
    #[serde(default)]
    pub cwd: String,
    pub pid: i32,
    #[serde(default)]
    pub started: String,
    pub running: bool,
    #[serde(default)]
    pub status: String,
    pub log_file: Option<String>,
}

#[derive(PartialEq)]
enum Page {
    Console,
    Dashboard,
    Processes,
    Claude,
    Settings,
}

pub struct Desktop {
    page: Page,
    supervisor: Supervisor,
    host: HostConfig,
    running: bool,
    busy: bool,
    notice: String,
    error: bool,
    logs: VecDeque<String>,
    paused_logs: Option<VecDeque<String>>,
    follow: bool,
    filter: String,
    operations: usize,
    interfaces: Vec<(String, String)>,
    address: String,
    server_name: String,
    local_connection: bool,
    show_token: bool,
    tray: Option<TrayManager>,
    caffeinate: CaffeinateGuard,
    keep_awake: bool,
    procs: Vec<DesktopProc>,
    proc_loading: bool,
    last_proc_poll: Option<Instant>,
    proc_input_map: HashMap<String, String>,
    available_dates: Vec<(String, PathBuf)>,
    selected_date: String,
    day_summary: Option<DaySummary>,
    dashboard_tab: usize,
}

impl Desktop {
    pub fn new(cc: &eframe::CreationContext<'_>) -> Self {
        setup_style(&cc.egui_ctx);
        let (host, notice, error) = match HostConfig::load() {
            Ok(c) => (c, String::new(), false),
            Err(e) => (
                HostConfig::defaults(),
                format!("加载设置失败：{e}；请在设置中保存配置"),
                true,
            ),
        };
        let interfaces = interfaces();
        let address = interfaces
            .iter()
            .find(|(n, ip)| n.starts_with("tailscale") || ip.starts_with("100."))
            .or_else(|| interfaces.iter().find(|(n, _)| n == "en0" || n == "en1"))
            .or_else(|| interfaces.first())
            .map(|(_, ip)| ip.clone())
            .unwrap_or_default();
        let local_connection = host.bind == "127.0.0.1";
        let tray = TrayManager::new().ok();
        let mut caffeinate = CaffeinateGuard::new();
        caffeinate.set_enabled(true, false);

        let mut desktop = Self {
            page: Page::Console,
            supervisor: Supervisor::new(),
            host,
            running: false,
            busy: false,
            notice,
            error,
            logs: VecDeque::new(),
            paused_logs: None,
            follow: true,
            filter: String::new(),
            operations: 0,
            interfaces,
            address,
            server_name: if cfg!(target_os = "macos") {
                "office-mac"
            } else {
                "host-pc"
            }
            .into(),
            local_connection,
            show_token: false,
            tray,
            caffeinate,
            keep_awake: true,
            procs: Vec::new(),
            proc_loading: false,
            last_proc_poll: None,
            proc_input_map: HashMap::new(),
            available_dates: Vec::new(),
            selected_date: String::new(),
            day_summary: None,
            dashboard_tab: 0,
        };
        desktop.load_dashboard_dates();
        #[cfg(feature = "screenshot")]
        let desktop = {
            let mut desktop = desktop;
            match std::env::var("MCP_SHELL_PREVIEW_THEME").as_deref() {
                Ok("light") => cc.egui_ctx.set_theme(egui::ThemePreference::Light),
                Ok("dark") => cc.egui_ctx.set_theme(egui::ThemePreference::Dark),
                _ => {}
            }
            desktop.page = match std::env::var("MCP_SHELL_PREVIEW_PAGE").as_deref() {
                Ok("connection") => Page::Claude,
                Ok("settings") => Page::Settings,
                _ => Page::Console,
            };
            if let Ok(path) = std::env::var("MCP_SHELL_PREVIEW_LOGS") {
                if let Ok(text) = std::fs::read_to_string(path) {
                    for line in text.lines() {
                        desktop.push_log(line.into());
                    }
                    desktop.notice = "本地集成测试日志回放；当前未启动服务".into();
                }
            }
            desktop
        };
        desktop
    }

    fn message(&mut self, text: impl Into<String>, error: bool) {
        self.notice = text.into();
        self.error = error;
    }

    fn copy(&mut self, ui: &egui::Ui, text: String, notice: &str) {
        ui.ctx().copy_text(text);
        self.message(notice, false);
    }

    fn push_log(&mut self, line: String) {
        if line.contains("[操作开始]") {
            self.operations += 1;
            if let Some(tray) = &mut self.tray {
                tray.update_status(self.running, true, &self.host.bind, self.host.port);
            }
        } else if line.contains("[操作完成]") || line.contains("[错误]") {
            if let Some(tray) = &mut self.tray {
                tray.update_status(self.running, false, &self.host.bind, self.host.port);
            }
        }
        self.logs.push_back(line);
        if self.logs.len() > MAX_LOGS {
            self.logs.pop_front();
        }
    }

    fn poll(&mut self) {
        while let Ok(event) = self.supervisor.events.try_recv() {
            match event {
                Event::Running(value) => {
                    self.running = value;
                    self.busy = false;
                    self.caffeinate.on_service_running(value);
                    if let Some(tray) = &mut self.tray {
                        tray.update_status(value, false, &self.host.bind, self.host.port);
                    }
                    let status = if value {
                        "MCP 服务已就绪，等待客户端连接"
                    } else {
                        "MCP 服务已停止"
                    };
                    self.push_log(format!("[服务] {status}"));
                    if !self.error {
                        self.message(status, false);
                    }
                }
                Event::Error(error) => {
                    self.push_log(format!("[错误] {error}"));
                    self.message(error, true);
                }
            }
        }
        // Pausing freezes the view, while the bounded live buffer keeps receiving logs.
        while let Ok(line) = self.supervisor.logs.try_recv() {
            self.push_log(line);
        }
    }

    fn controls(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.horizontal(|ui| {
                ui.vertical(|ui| {
                    ui.horizontal(|ui| {
                        if self.busy {
                            ui.spinner();
                        }
                        badge(
                            ui,
                            if self.busy {
                                if self.running {
                                    "正在停止…"
                                } else {
                                    "正在启动…"
                                }
                            } else if self.running {
                                "服务运行中"
                            } else {
                                "服务未启动"
                            },
                            self.running && !self.busy,
                        );
                    });
                    muted(
                        ui,
                        format!(
                            "{}:{}  ·  {}",
                            self.host.bind,
                            self.host.port,
                            if self.host.bind == "127.0.0.1" {
                                "仅本机"
                            } else {
                                "局域网可连接"
                            }
                        ),
                    );
                });
                ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                    if self.running {
                        if ui
                            .add_enabled(!self.busy, egui::Button::new("停止服务"))
                            .clicked()
                        {
                            self.busy = true;
                            self.message("正在停止服务并清理后台进程…", false);
                            self.supervisor.send(Action::Stop);
                        }
                    } else if ui.add_enabled(!self.busy, primary("启动服务")).clicked() {
                        match self.host.save() {
                            Ok(()) => {
                                self.busy = true;
                                self.message("正在启动 MCP 服务…", false);
                                self.supervisor.send(Action::Start(self.host.clone()));
                            }
                            Err(e) => self.message(format!("保存设置失败：{e}"), true),
                        }
                    }
                    if self.page != Page::Claude && ui.button("连接配置").clicked() {
                        self.page = Page::Claude;
                    }
                });
            });
            ui.separator();
            ui.horizontal_wrapped(|ui| {
                muted(
                    ui,
                    format!(
                        "访问范围：{}",
                        if self.host.unrestricted {
                            "当前用户可访问的全部文件"
                        } else {
                            "指定目录"
                        }
                    ),
                );
                ui.separator();
                muted(ui, "工具自动执行");
                ui.separator();
                muted(ui, format!("本次操作 {}", self.operations));
                ui.separator();
                let caffeinate_label = if self.caffeinate.is_active() {
                    "保持唤醒 (生效中)"
                } else {
                    "保持唤醒 (防止休眠/断网)"
                };
                if ui
                    .checkbox(&mut self.keep_awake, caffeinate_label)
                    .changed()
                {
                    self.caffeinate.set_enabled(self.keep_awake, self.running);
                }
            });
        });
    }

    fn displayed_logs(&self) -> &VecDeque<String> {
        self.paused_logs.as_ref().unwrap_or(&self.logs)
    }

    fn visible_logs(&self) -> String {
        self.displayed_logs()
            .iter()
            .filter(|s| self.filter.is_empty() || s.contains(&self.filter))
            .cloned()
            .collect::<Vec<_>>()
            .join("\n")
    }

    fn console(&mut self, ui: &mut egui::Ui) {
        ui.horizontal_wrapped(|ui| {
            ui.strong("操作日志");
            muted(ui, format!("{} 条", self.displayed_logs().len()));
            ui.separator();
            ui.checkbox(&mut self.follow, "跟随最新");
            let mut paused = self.paused_logs.is_some();
            if ui.checkbox(&mut paused, "暂停显示").changed() {
                self.paused_logs = paused.then(|| self.logs.clone());
            }
            if ui.button("复制").clicked() {
                self.copy(ui, self.visible_logs(), "当前筛选的日志已复制");
            }
            if ui.button("清空").clicked() {
                self.logs.clear();
                if let Some(logs) = &mut self.paused_logs {
                    logs.clear();
                }
                self.message("已清空界面日志，磁盘日志保留", false);
            }
        });
        ui.horizontal(|ui| {
            ui.add(
                TextEdit::singleline(&mut self.filter)
                    .hint_text("搜索日志：命令、路径或状态")
                    .desired_width(ui.available_width() - 65.0),
            );
            if ui
                .add_enabled(!self.filter.is_empty(), egui::Button::new("重置"))
                .clicked()
            {
                self.filter.clear();
            }
        });
        if self.paused_logs.is_some() {
            muted(ui, "显示已暂停，后台仍在接收日志。取消暂停可查看最新内容。");
        } else {
            muted(
                ui,
                "实时显示请求和命令输出，保留最近 2000 条；完整日志持续写入磁盘。",
            );
        }
        let height = (ui.available_height() - 8.0).max(120.0);
        let dark = ui.visuals().dark_mode;
        egui::Frame::new()
            .fill(ui.visuals().extreme_bg_color)
            .stroke(ui.visuals().widgets.noninteractive.bg_stroke)
            .corner_radius(10.0).inner_margin(16.0)
            .show(ui, |ui| {
                ui.set_min_width(ui.available_width());
                egui::ScrollArea::both().id_salt("live-logs")
                    .stick_to_bottom(self.follow && self.paused_logs.is_none())
                    .auto_shrink([false, false]).max_height(height - 32.0)
                    .show(ui, |ui| {
                        ui.set_min_height(height - 32.0);
                        let mut shown = 0;
                        for line in self.displayed_logs() {
                            if !self.filter.is_empty() && !line.contains(&self.filter) { continue; }
                            shown += 1;
                            let color = if line.contains("[操作完成] 失败") || line.contains("[错误]") || line.contains("[认证] 拒绝") {
                                if dark { Color32::from_rgb(255, 148, 148) } else { Color32::from_rgb(174, 40, 52) }
                            } else if line.contains("[操作开始]") || line.contains("[MCP连接]") {
                                if dark { Color32::from_rgb(132, 186, 255) } else { Color32::from_rgb(34, 86, 177) }
                            } else if line.contains("[操作完成] 成功") {
                                if dark { Color32::from_rgb(127, 218, 177) } else { Color32::from_rgb(23, 117, 77) }
                            } else { ui.visuals().text_color() };
                            ui.add(egui::Label::new(RichText::new(line).monospace().color(color)).selectable(true));
                        }
                        if shown == 0 {
                            ui.add_space(28.0);
                            ui.strong(if !self.filter.is_empty() { "没有匹配的日志" }
                                else if self.running { "等待客户端请求" } else { "从启动服务开始" });
                            muted(ui, if !self.filter.is_empty() { "尝试其他关键词，或点击重置查看全部日志。" }
                                else { "启动服务后，在「连接配置」中复制接入命令。客户端的操作将在这里显示。" });
                        }
                    });
            });
    }

    fn claude(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.strong("1. 选择连接方式");
            ui.horizontal_wrapped(|ui| {
                ui.selectable_value(&mut self.local_connection, true, "本机连接");
                ui.selectable_value(&mut self.local_connection, false, "局域网 / 虚拟机");
            });
            if self.local_connection {
                muted(
                    ui,
                    "Claude 与 MCP Shell 运行在同一台电脑，使用 127.0.0.1 连接。",
                );
            } else {
                muted(
                    ui,
                    "填写运行 MCP Shell 的电脑地址；客户端需要能够访问该地址。",
                );
                field(ui, "主机 IPv4 地址", &mut self.address, "例如 192.168.1.20");
                ui.horizontal_wrapped(|ui| {
                    egui::ComboBox::from_id_salt("interfaces")
                        .selected_text("选择本机网卡地址")
                        .show_ui(ui, |ui| {
                            for (name, ip) in &self.interfaces {
                                let tag = interface_tag(name, ip);
                                ui.selectable_value(
                                    &mut self.address,
                                    ip.clone(),
                                    format!("{tag}  {name} · {ip}"),
                                );
                            }
                        });
                    if ui.button("刷新网卡").clicked() {
                        self.interfaces = interfaces();
                    }
                });
                if self.host.bind == "127.0.0.1" {
                    ui.add_space(4.0);
                    ui.horizontal_wrapped(|ui| {
                        ui.colored_label(
                            ui.visuals().warn_fg_color,
                            "⚠️ 当前只接受本机连接 (127.0.0.1)。跨机器 Claude 无法连接。",
                        );
                        if ui.button("一键切换为局域网监听 (0.0.0.0)").clicked() {
                            self.host.bind = "0.0.0.0".into();
                            if let Err(e) = self.host.save() {
                                self.message(format!("保存设置失败：{e}"), true);
                            } else if self.running {
                                self.message("已切换为局域网监听，请停止后重新启动服务生效", false);
                            } else {
                                self.message("已切换为局域网监听 (0.0.0.0)，启动服务即可连接", false);
                            }
                        }
                    });
                }
            }
            ui.add_space(4.0);
            field(
                ui,
                "服务名称",
                &mut self.server_name,
                "office-mac / host-pc",
            );
        });
        ui.add_space(16.0);
        let name = self.server_name.trim().to_owned();
        let endpoint = connection_endpoint(
            self.local_connection,
            &self.address,
            &self.host.bind,
            self.host.port,
        );
        let name_valid = valid_server_name(&name);
        let valid = name_valid && endpoint.is_ok();
        card(ui, |ui| {
            ui.strong("2. 复制到 Claude 所在电脑的终端");
            if !name_valid {
                ui.colored_label(
                    ui.visuals().error_fg_color,
                    "服务名称只能包含英文字母、数字、短横线和下划线。",
                );
            }
            if let Err(error) = &endpoint {
                ui.colored_label(ui.visuals().warn_fg_color, error);
            }
            let mut preview = endpoint
                .as_ref()
                .map(|url| {
                    claude_command(
                        &name,
                        url,
                        if self.show_token {
                            &self.host.token
                        } else {
                            "<访问令牌>"
                        },
                    )
                })
                .unwrap_or_else(|_| "完成上方连接配置后生成接入命令".into());
            ui.add(
                TextEdit::multiline(&mut preview)
                    .code_editor()
                    .interactive(false)
                    .desired_rows(3)
                    .desired_width(f32::INFINITY),
            );
            ui.horizontal_wrapped(|ui| {
                let enabled = valid && self.running && !self.busy;
                if ui.add_enabled(enabled, primary("复制接入命令")).clicked() {
                    self.copy(ui, claude_command(&name, endpoint.as_ref().unwrap(), &self.host.token), "接入命令已复制，包含访问令牌");
                }
                if ui.add_enabled(enabled, egui::Button::new("复制 MCP JSON")).clicked() {
                    let text = serde_json::to_string_pretty(&json!({"mcpServers":{(&name):{"type":"http","url":endpoint.as_ref().unwrap(),"headers":{"Authorization":format!("Bearer {}",self.host.token)}}}})).unwrap();
                    self.copy(ui, text, "MCP JSON 已复制，包含访问令牌");
                }
                ui.checkbox(&mut self.show_token, "显示令牌");
            });
            if !self.running {
                muted(ui, "先启动服务，再复制已验证就绪的连接配置。");
            }
            muted(
                ui,
                "复制内容包含真实令牌。添加后，在 Claude 中执行 /mcp 检查连接。",
            );
        });
        ui.add_space(16.0);
        egui::CollapsingHeader::new("已有配置与工具授权").show(ui, |ui| {
            ui.label("已有同名服务时，先执行 claude mcp remove <服务名称> --scope user，再重新添加。");
            ui.label("本程序收到请求后直接执行。需要自动授权时，将以下片段合并到 Claude 的 ~/.claude/settings.json。");
            if ui.add_enabled(name_valid, egui::Button::new("复制当前服务的权限配置")).clicked() {
                self.copy(ui, serde_json::to_string_pretty(&json!({"permissions":{"allow":[format!("mcp__{name}__*")]}})).unwrap(), "当前服务的工具权限配置已复制");
            }
            muted(ui, "合并 permissions.allow，保留现有配置中的其他内容。");
        });
    }

    fn settings(&mut self, ui: &mut egui::Ui) {
        if self.running || self.busy {
            muted(ui, "服务运行期间设置已锁定；停止服务后可修改。");
        }
        card(ui, |ui| {
            ui.add_enabled_ui(!self.running && !self.busy, |ui| {
                ui.strong("监听范围");
                ui.horizontal_wrapped(|ui| {
                    ui.radio_value(&mut self.host.bind, "127.0.0.1".into(), "仅本机");
                    ui.radio_value(&mut self.host.bind, "0.0.0.0".into(), "局域网");
                });
                muted(
                    ui,
                    if self.host.bind == "127.0.0.1" {
                        "只允许这台电脑上的客户端连接。"
                    } else {
                        "允许可达这台电脑的客户端连接，仍需访问令牌。请仅在可信网络使用。"
                    },
                );
                ui.horizontal(|ui| {
                    ui.label("服务端口");
                    ui.add(egui::DragValue::new(&mut self.host.port).range(1024..=65535));
                });
                ui.add_space(8.0);
                ui.separator();
                ui.strong("文件与工作目录");
                ui.checkbox(
                    &mut self.host.unrestricted,
                    "允许访问当前用户有权限的全部文件",
                );
                ui.label(if self.host.unrestricted {
                    "默认工作目录（每行一个）"
                } else {
                    "允许访问的目录（每行一个）"
                });
                ui.add(
                    TextEdit::multiline(&mut self.host.roots)
                        .desired_width(f32::INFINITY)
                        .desired_rows(3),
                );
                muted(
                    ui,
                    if self.host.unrestricted {
                        "目录用于解析相对路径，不限制文件访问范围。"
                    } else {
                        "文件工具限制在这些目录内；执行的命令仍受操作系统权限约束。"
                    },
                );
                egui::CollapsingHeader::new("高级设置").show(ui, |ui| {
                    field(ui, "服务程序", &mut self.host.server, "mcp-shell");
                    muted(ui, "通常使用应用内附带的服务程序，无需修改。");
                });
                ui.add_space(8.0);
                ui.horizontal_wrapped(|ui| {
                    if ui.add(primary("保存设置")).clicked() {
                        match self.host.save() {
                            Ok(()) => self.message("设置已保存，下次启动服务时生效", false),
                            Err(e) => self.message(e, true),
                        }
                    }
                    if ui.button("更换访问令牌").clicked() {
                        let old = std::mem::replace(&mut self.host.token, host::token());
                        match self.host.save() {
                            Ok(()) => self.message("令牌已更新，请重新复制客户端连接配置", false),
                            Err(e) => {
                                self.host.token = old;
                                self.message(e, true);
                            }
                        }
                    }
                });
            });
        });
        ui.add_space(16.0);
        card(ui, |ui| {
            ui.strong("外观与本地数据");
            muted(ui, "外观跟随系统的浅色 / 深色模式。");
            if let Ok(state) = host::state_dir() {
                ui.label("配置与完整日志目录");
                ui.add(
                    egui::Label::new(RichText::new(state.display().to_string()).monospace()).wrap(),
                );
                if ui.button("复制目录路径").clicked() {
                    self.copy(ui, state.display().to_string(), "配置与日志目录已复制");
                }
            }
            muted(ui, "macOS 的桌面、文稿等目录受系统隐私权限保护，可在系统设置中为应用授予相应访问权限。");
        });
    }

    fn poll_processes(&mut self) {
        if !self.running {
            self.procs.clear();
            return;
        }
        let should_poll = match self.last_proc_poll {
            Some(last) => last.elapsed() > Duration::from_secs(3),
            None => true,
        };
        if should_poll && !self.proc_loading {
            self.refresh_processes();
        }
    }

    fn refresh_processes(&mut self) {
        if !self.running {
            return;
        }
        self.last_proc_poll = Some(Instant::now());
        let url = format!("http://127.0.0.1:{}", self.host.port);
        let token = self.host.token.clone();
        if let Ok(remote) = Remote::new(&url, &token, Duration::from_secs(2)) {
            if let Ok(res) = remote.call(rand::random(), "list_processes", json!({"json": true})) {
                if let Ok(list) = serde_json::from_str::<Vec<DesktopProc>>(&res) {
                    self.procs = list;
                }
            }
        }
    }

    fn stop_one_process(&mut self, id: &str) {
        let url = format!("http://127.0.0.1:{}", self.host.port);
        let token = self.host.token.clone();
        if let Ok(remote) = Remote::new(&url, &token, Duration::from_secs(3)) {
            match remote.call(rand::random(), "stop_process", json!({"id": id})) {
                Ok(msg) => {
                    self.message(format!("已停止后台进程 {id}：{msg}"), false);
                    self.refresh_processes();
                }
                Err(e) => self.message(format!("停止进程失败：{e}"), true),
            }
        }
    }

    fn send_process_input(&mut self, id: &str, input: String) {
        let url = format!("http://127.0.0.1:{}", self.host.port);
        let token = self.host.token.clone();
        if let Ok(remote) = Remote::new(&url, &token, Duration::from_secs(3)) {
            match remote.call(
                rand::random(),
                "write_process_input",
                json!({"id": id, "input": input}),
            ) {
                Ok(msg) => {
                    self.message(format!("已向进程 {id} 发送输入：{msg}"), false);
                }
                Err(e) => self.message(format!("发送输入失败：{e}"), true),
            }
        }
    }

    fn processes(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.strong("后台进程管理 (Process Manager)");
            muted(ui, format!("{} 个进程", self.procs.len()));
            ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                if ui.button("刷新进程列表").clicked() {
                    self.refresh_processes();
                }
            });
        });
        muted(ui, "监控由远端 Claude 通过 start_process 或超时转后台运行的任务。可在此查看状态、输入 stdin 或一键终止。");
        ui.add_space(8.0);

        if !self.running {
            card(ui, |ui| {
                ui.add_space(20.0);
                ui.vertical_centered(|ui| {
                    ui.strong("MCP 服务未启动");
                    muted(ui, "启动服务后，由 Claude 或长任务拉起的后台进程（如 pnpm dev、python 服务等）将实时显示在此处。");
                });
                ui.add_space(20.0);
            });
            return;
        }

        if self.procs.is_empty() {
            card(ui, |ui| {
                ui.add_space(20.0);
                ui.vertical_centered(|ui| {
                    ui.strong("当前没有后台运行的进程");
                    muted(ui, "当客户端调用 start_process（例如开启本地服务器、后台构建）时，会列在此处。");
                });
                ui.add_space(20.0);
            });
            return;
        }

        let mut to_stop: Option<String> = None;
        let mut to_send: Option<(String, String)> = None;

        for proc in &self.procs {
            card(ui, |ui| {
                ui.horizontal(|ui| {
                    badge(ui, if proc.running { "运行中" } else { "已退出" }, proc.running);
                    ui.strong(format!("{} (PID: {})", proc.id, proc.pid));
                    if !proc.name.is_empty() && proc.name != "-" {
                        ui.label(RichText::new(format!("[{}]", proc.name)).color(BLUE));
                    }
                    ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                        if proc.running && ui.button("终止进程").clicked() {
                            to_stop = Some(proc.id.clone());
                        }
                    });
                });
                ui.add_space(4.0);
                ui.label(RichText::new(&proc.command).monospace().size(13.0));
                ui.horizontal_wrapped(|ui| {
                    muted(ui, format!("目录: {}", proc.cwd));
                    if !proc.shell.is_empty() {
                        ui.separator();
                        muted(ui, format!("Shell: {}", proc.shell));
                    }
                    ui.separator();
                    muted(ui, format!("启动: {}", proc.started));
                    ui.separator();
                    muted(ui, &proc.status);
                    if let Some(log) = &proc.log_file {
                        ui.separator();
                        muted(ui, format!("日志: {log}"));
                    }
                });

                if proc.running {
                    ui.separator();
                    ui.horizontal(|ui| {
                        let input_text = self.proc_input_map.entry(proc.id.clone()).or_default();
                        ui.add(TextEdit::singleline(input_text).hint_text("向 stdin 发送输入...").desired_width(280.0));
                        if ui.button("发送输入").clicked() {
                            to_send = Some((proc.id.clone(), input_text.clone()));
                            input_text.clear();
                        }
                    });
                }
            });
            ui.add_space(8.0);
        }

        if let Some(id) = to_stop {
            self.stop_one_process(&id);
        }
        if let Some((id, text)) = to_send {
            self.send_process_input(&id, text);
        }
    }

    fn history_directories(&self) -> Vec<PathBuf> {
        let mut dirs = Vec::new();
        if let Ok(state) = host::state_dir() {
            dirs.push(state.join("output/history"));
            dirs.push(state.join("history"));
        }
        for root in self.host.roots.lines().map(str::trim).filter(|s| !s.is_empty()) {
            dirs.push(PathBuf::from(root).join(".tmp/mcp-shell/history"));
        }
        dirs
    }

    pub fn load_dashboard_dates(&mut self) {
        let dirs = self.history_directories();
        let dates = dashboard::list_history_dates(&dirs);
        self.available_dates = dates;
        if self.selected_date.is_empty()
            || !self.available_dates.iter().any(|(d, _)| d == &self.selected_date)
        {
            if let Some((first_date, _)) = self.available_dates.first() {
                self.selected_date = first_date.clone();
            }
        }
        self.load_selected_day_summary();
    }

    pub fn load_selected_day_summary(&mut self) {
        if self.selected_date.is_empty() {
            self.day_summary = None;
            return;
        }
        if let Some((_, path)) = self
            .available_dates
            .iter()
            .find(|(d, _)| d == &self.selected_date)
        {
            let summary = DaySummary::load_from_file(path, self.selected_date.clone());
            self.day_summary = Some(summary);
        }
    }

    fn dashboard(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.horizontal(|ui| {
                ui.strong("选择日期：");
                let mut changed = false;
                egui::ComboBox::from_id_salt("dashboard_date_select")
                    .selected_text(if self.selected_date.is_empty() {
                        "暂无历史记录".to_string()
                    } else {
                        self.selected_date.clone()
                    })
                    .show_ui(ui, |ui| {
                        for (date, _) in &self.available_dates {
                            if ui
                                .selectable_value(&mut self.selected_date, date.clone(), date)
                                .clicked()
                            {
                                changed = true;
                            }
                        }
                    });

                if changed {
                    self.load_selected_day_summary();
                }

                if ui.button("🔄 刷新统计").clicked() {
                    self.load_dashboard_dates();
                }

                ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
                    if ui.button("📂 在访达中打开记录目录").clicked() {
                        if let Ok(state) = host::state_dir() {
                            let p = state.join("output/history");
                            let _ = std::process::Command::new("open").arg(p).spawn();
                        }
                    }
                });
            });
        });
        ui.add_space(8.0);

        let Some(summary) = &self.day_summary else {
            card(ui, |ui| {
                ui.add_space(20.0);
                ui.vertical_centered(|ui| {
                    ui.strong("所选日期暂无操作记录");
                    muted(
                        ui,
                        "当远端 Claude 发送工具调用时，调用记录将自动按日期归档保存在本地。",
                    );
                });
                ui.add_space(20.0);
            });
            return;
        };

        card(ui, |ui| {
            ui.columns(4, |cols| {
                cols[0].vertical_centered(|ui| {
                    muted(ui, "今日调用总数");
                    ui.label(
                        RichText::new(format!("{}", summary.total_calls))
                            .size(24.0)
                            .strong()
                            .color(BLUE),
                    );
                });
                cols[1].vertical_centered(|ui| {
                    muted(ui, "调用成功率");
                    let rate = if summary.total_calls > 0 {
                        (summary.success_calls as f64 / summary.total_calls as f64) * 100.0
                    } else {
                        100.0
                    };
                    let color = if summary.failed_calls > 0 {
                        Color32::from_rgb(220, 90, 50)
                    } else {
                        Color32::from_rgb(40, 160, 90)
                    };
                    ui.label(
                        RichText::new(format!("{:.1}%", rate))
                            .size(24.0)
                            .strong()
                            .color(color),
                    );
                    muted(
                        ui,
                        format!("{} 成功 / {} 失败", summary.success_calls, summary.failed_calls),
                    );
                });
                cols[2].vertical_centered(|ui| {
                    muted(ui, "总执行耗时");
                    let sec = summary.total_duration_ms as f64 / 1000.0;
                    ui.label(
                        RichText::new(format!("{:.2} s", sec))
                            .size(24.0)
                            .strong(),
                    );
                });
                cols[3].vertical_centered(|ui| {
                    muted(ui, "操作类型分布");
                    ui.horizontal_wrapped(|ui| {
                        ui.label(format!("读:{} 写:{}", summary.read_count, summary.write_count));
                        ui.label(format!("令:{} 统:{}", summary.cmd_count, summary.sys_count));
                    });
                });
            });
        });
        ui.add_space(8.0);

        ui.horizontal(|ui| {
            let tabs = [
                format!("📝 文件变更审计 ({})", summary.modified_files.len()),
                format!("⚡️ 慢命令 Top ({})", summary.slow_commands.len()),
                format!("📊 工具明细 ({})", summary.tool_counts.len()),
                format!("⚠️ 异常与报错 ({})", summary.failed_records.len()),
            ];
            for (idx, label) in tabs.iter().enumerate() {
                if ui.selectable_label(self.dashboard_tab == idx, label).clicked() {
                    self.dashboard_tab = idx;
                }
            }
        });
        ui.add_space(6.0);

        let mut to_reveal: Option<String> = None;

        match self.dashboard_tab {
            0 => {
                if summary.modified_files.is_empty() {
                    card(ui, |ui| {
                        ui.vertical_centered(|ui| {
                            muted(ui, "该日期无任何文件写入、编辑或删除记录");
                        });
                    });
                } else {
                    for file in &summary.modified_files {
                        card(ui, |ui| {
                            ui.horizontal(|ui| {
                                ui.strong(&file.path);
                                ui.with_layout(
                                    egui::Layout::right_to_left(egui::Align::Center),
                                    |ui| {
                                        if ui.button("在访达中定位").clicked() {
                                            to_reveal = Some(file.path.clone());
                                        }
                                        ui.label(
                                            RichText::new(format!("修改 {} 次", file.count))
                                                .color(BLUE),
                                        );
                                        muted(ui, format!("最后变更: {}", file.last_time));
                                    },
                                );
                            });
                            ui.add_space(2.0);
                            ui.horizontal_wrapped(|ui| {
                                muted(ui, "涉及操作:");
                                for act in &file.actions {
                                    ui.label(RichText::new(act).monospace().size(12.0));
                                }
                            });
                        });
                        ui.add_space(4.0);
                    }
                }
            }
            1 => {
                if summary.slow_commands.is_empty() {
                    card(ui, |ui| {
                        ui.vertical_centered(|ui| {
                            muted(ui, "该日期无执行命令耗时记录");
                        });
                    });
                } else {
                    for cmd in &summary.slow_commands {
                        card(ui, |ui| {
                            ui.horizontal(|ui| {
                                let sec = cmd.duration_ms as f64 / 1000.0;
                                let color = if sec > 10.0 {
                                    Color32::from_rgb(220, 80, 50)
                                } else if sec > 3.0 {
                                    Color32::from_rgb(210, 150, 40)
                                } else {
                                    Color32::from_rgb(50, 150, 80)
                                };
                                ui.label(
                                    RichText::new(format!("{:.2} s", sec))
                                        .strong()
                                        .color(color),
                                );
                                if let Some(code) = cmd.exit_code {
                                    badge(ui, &format!("退出码 {code}"), code == 0);
                                }
                                ui.with_layout(
                                    egui::Layout::right_to_left(egui::Align::Center),
                                    |ui| {
                                        muted(ui, &cmd.time);
                                    },
                                );
                            });
                            ui.add_space(4.0);
                            ui.label(RichText::new(&cmd.command).monospace().size(13.0));
                        });
                        ui.add_space(4.0);
                    }
                }
            }
            2 => {
                card(ui, |ui| {
                    egui::Grid::new("tool_counts_grid")
                        .striped(true)
                        .min_col_width(120.0)
                        .show(ui, |ui| {
                            ui.strong("工具名称");
                            ui.strong("调用次数");
                            ui.strong("占比");
                            ui.end_row();

                            for (tool, count) in &summary.tool_counts {
                                ui.label(RichText::new(tool).monospace());
                                ui.label(format!("{count} 次"));
                                let pct = if summary.total_calls > 0 {
                                    (*count as f64 / summary.total_calls as f64) * 100.0
                                } else {
                                    0.0
                                };
                                ui.label(format!("{:.1}%", pct));
                                ui.end_row();
                            }
                        });
                });
            }
            3 => {
                if summary.failed_records.is_empty() {
                    card(ui, |ui| {
                        ui.vertical_centered(|ui| {
                            ui.label(
                                RichText::new("✓ 该日期没有发生任何调用失败")
                                    .color(Color32::from_rgb(40, 160, 90))
                                    .strong(),
                            );
                        });
                    });
                } else {
                    for rec in &summary.failed_records {
                        card(ui, |ui| {
                            ui.horizontal(|ui| {
                                ui.colored_label(Color32::from_rgb(220, 60, 60), "❌ 失败");
                                ui.strong(&rec.tool);
                                if let Some(code) = rec.exit_code {
                                    muted(ui, format!("退出码: {code}"));
                                }
                                ui.with_layout(
                                    egui::Layout::right_to_left(egui::Align::Center),
                                    |ui| {
                                        muted(ui, &rec.time);
                                    },
                                );
                            });
                            if let Some(err) = &rec.error {
                                ui.add_space(2.0);
                                ui.colored_label(Color32::from_rgb(200, 50, 50), err);
                            }
                            if let Some(res) = &rec.result {
                                if !res.is_empty() {
                                    ui.add_space(2.0);
                                    ui.label(RichText::new(res).monospace().size(12.0));
                                }
                            }
                        });
                        ui.add_space(4.0);
                    }
                }
            }
            _ => {}
        }

        if let Some(path) = to_reveal {
            let _ = std::process::Command::new("open")
                .arg("-R")
                .arg(path)
                .spawn();
        }
    }
}

impl eframe::App for Desktop {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.poll();
        if self.page == Page::Processes {
            self.poll_processes();
        }

        // Cancel close and hide to tray if user closes window
        if ctx.input(|i| i.viewport().close_requested()) {
            ctx.send_viewport_cmd(egui::ViewportCommand::CancelClose);
            ctx.send_viewport_cmd(egui::ViewportCommand::Visible(false));
            self.message("应用已最小化到 macOS 顶部菜单栏，服务在后台继续运行", false);
        }

        // Handle tray actions
        if let Some(tray) = &mut self.tray {
            if let Some(cmd) = tray.poll_command() {
                match cmd {
                    TrayCommand::ShowWindow => {
                        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(true));
                        ctx.send_viewport_cmd(egui::ViewportCommand::Focus);
                    }
                    TrayCommand::ToggleService => {
                        if self.running {
                            self.busy = true;
                            self.message("正在停止服务…", false);
                            self.supervisor.send(Action::Stop);
                        } else {
                            self.busy = true;
                            self.message("正在启动服务…", false);
                            self.supervisor.send(Action::Start(self.host.clone()));
                        }
                    }
                    TrayCommand::Quit => {
                        ctx.send_viewport_cmd(egui::ViewportCommand::Close);
                    }
                }
            }
        }

        ctx.request_repaint_after(Duration::from_millis(100));
        egui::SidePanel::left("navigation")
            .resizable(false)
            .exact_width(180.0)
            .frame(
                egui::Frame::new()
                    .fill(ctx.style().visuals.panel_fill)
                    .inner_margin(16.0),
            )
            .show(ctx, |ui| {
                ui.add_space(12.0);
                ui.label(RichText::new("MCP Shell").size(22.0).strong());
                muted(ui, "本机服务控制台");
                ui.add_space(28.0);
                for (page, label) in [
                    (Page::Console, "操作日志"),
                    (Page::Dashboard, "统计大盘"),
                    (Page::Claude, "连接配置"),
                    (Page::Processes, "运行中进程"),
                    (Page::Settings, "服务设置"),
                ] {
                    let selected = self.page == page;
                    if ui
                        .add_sized(
                            [ui.available_width(), 40.0],
                            egui::Button::new(label).selected(selected),
                        )
                        .clicked()
                    {
                        self.page = page;
                    }
                    ui.add_space(4.0);
                }
                ui.with_layout(egui::Layout::bottom_up(egui::Align::LEFT), |ui| {
                    muted(ui, format!("v{}", env!("CARGO_PKG_VERSION")));
                    muted(ui, "外观跟随系统");
                    ui.separator();
                });
            });
        egui::TopBottomPanel::bottom("status")
            .min_height(36.0)
            .show(ctx, |ui| {
                ui.add_space(5.0);
                ui.horizontal_wrapped(|ui| {
                    let color = if self.error {
                        ui.visuals().error_fg_color
                    } else {
                        ui.visuals().weak_text_color()
                    };
                    ui.colored_label(
                        color,
                        if self.notice.is_empty() {
                            "就绪 · 启动服务后连接 Claude"
                        } else {
                            &self.notice
                        },
                    );
                });
            });
        egui::CentralPanel::default()
            .frame(
                egui::Frame::new()
                    .fill(ctx.style().visuals.faint_bg_color)
                    .inner_margin(24.0),
            )
            .show(ctx, |ui| {
                let (title, subtitle) = match self.page {
                    Page::Console => ("操作日志", "查看客户端请求、命令输出与执行结果。"),
                    Page::Dashboard => ("统计大盘", "持久化审计与每日调用指标，支持跨天筛选与文件变更追踪。"),
                    Page::Claude => ("连接配置", "将这台电脑接入 Claude，本机和局域网均可使用。"),
                    Page::Processes => ("运行中进程", "监控由远端 Claude 启动的后台任务与服务，可发送 stdin 输入或一键终止。"),
                    Page::Settings => ("服务设置", "管理监听范围、工作目录与访问令牌。"),
                };
                ui.label(RichText::new(title).size(26.0).strong());
                muted(ui, subtitle);
                ui.add_space(12.0);
                self.controls(ui);
                ui.add_space(16.0);
                if self.page == Page::Console {
                    self.console(ui);
                } else {
                    egui::ScrollArea::vertical()
                        .id_salt("page-content")
                        .show(ui, |ui| match self.page {
                            Page::Dashboard => self.dashboard(ui),
                            Page::Claude => self.claude(ui),
                            Page::Processes => self.processes(ui),
                            Page::Settings => self.settings(ui),
                            Page::Console => {}
                        });
                }
            });
    }
}

fn interface_tag(name: &str, ip: &str) -> &'static str {
    if name.starts_with("tailscale") || ip.starts_with("100.") {
        "[Tailscale 推荐]"
    } else if name.starts_with("zt") {
        "[ZeroTier 推荐]"
    } else if name == "en0" || name == "en1" {
        "[Wi-Fi/以太网]"
    } else {
        "[局域网]"
    }
}

fn interfaces() -> Vec<(String, String)> {
    let mut result = if_addrs::get_if_addrs()
        .unwrap_or_default()
        .into_iter()
        .filter(|i| i.ip().is_ipv4() && !i.is_loopback())
        .map(|i| (i.name.clone(), i.ip().to_string()))
        .collect::<Vec<_>>();
    result.sort();
    result.dedup();
    result
}

fn valid_server_name(name: &str) -> bool {
    !name.is_empty()
        && name
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
}

fn connection_endpoint(
    local: bool,
    address: &str,
    bind: &str,
    port: u16,
) -> Result<String, String> {
    if port == 0 {
        return Err("服务端口不能为 0".into());
    }
    let address = if local {
        Ipv4Addr::LOCALHOST
    } else {
        if bind == "127.0.0.1" {
            return Err("局域网连接需要先启用局域网监听。".into());
        }
        let ip: Ipv4Addr = address
            .trim()
            .parse()
            .map_err(|_| "请输入有效的主机 IPv4 地址。")?;
        if ip.is_unspecified() || ip.is_loopback() || ip.is_multicast() || ip.is_broadcast() {
            return Err("请选择客户端可达的主机地址，不能使用回环或广播地址。".into());
        }
        ip
    };
    Ok(format!("http://{address}:{port}/mcp"))
}

fn shell_quote(text: &str) -> String {
    format!("'{}'", text.replace('\'', "'\\''"))
}
fn claude_command(name: &str, url: &str, token: &str) -> String {
    format!(
        "claude mcp add --scope user --transport http {} {} --header {}",
        shell_quote(name),
        shell_quote(url),
        shell_quote(&format!("Authorization: Bearer {token}"))
    )
}

fn setup_style(ctx: &egui::Context) {
    ctx.set_theme(egui::ThemePreference::System);
    ctx.all_styles_mut(|style| {
        style.spacing.item_spacing = egui::vec2(10.0, 10.0);
        style.spacing.button_padding = egui::vec2(14.0, 9.0);
        style.visuals.selection.bg_fill = BLUE;
        style.visuals.selection.stroke = egui::Stroke::new(1.0_f32, Color32::WHITE);
        for font in style.text_styles.values_mut() {
            font.size += 1.0;
        }
    });
    let custom = std::env::var("MCP_SHELL_FONT").unwrap_or_default();
    let win = std::env::var("WINDIR")
        .map(|p| format!("{p}/Fonts/msyh.ttc"))
        .unwrap_or_default();
    let mut fonts = egui::FontDefinitions::default();
    for path in [
        custom.as_str(),
        win.as_str(),
        "/System/Library/Fonts/PingFang.ttc",
        "/System/Library/Fonts/Supplemental/Songti.ttc",
        "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    ] {
        if let Ok(data) = std::fs::read(path) {
            fonts
                .font_data
                .insert("cjk".into(), egui::FontData::from_owned(data).into());
            for family in [egui::FontFamily::Proportional, egui::FontFamily::Monospace] {
                fonts.families.entry(family).or_default().push("cjk".into());
            }
            break;
        }
    }
    ctx.set_fonts(fonts);
}
fn card(ui: &mut egui::Ui, body: impl FnOnce(&mut egui::Ui)) {
    egui::Frame::new()
        .fill(ui.visuals().window_fill())
        .stroke(ui.visuals().widgets.noninteractive.bg_stroke)
        .corner_radius(10.0)
        .inner_margin(16.0)
        .show(ui, |ui| {
            ui.set_width(ui.available_width());
            body(ui);
        });
}
fn field(ui: &mut egui::Ui, label: &str, value: &mut String, hint: &str) {
    ui.label(label);
    ui.add(
        TextEdit::singleline(value)
            .hint_text(hint)
            .desired_width(f32::INFINITY),
    );
}
fn muted(ui: &mut egui::Ui, text: impl Into<String>) {
    ui.label(RichText::new(text).color(ui.visuals().weak_text_color()));
}
fn primary(label: &str) -> egui::Button<'_> {
    egui::Button::new(RichText::new(label).color(Color32::WHITE)).fill(BLUE)
}
fn badge(ui: &mut egui::Ui, label: &str, on: bool) {
    let color = if on {
        if ui.visuals().dark_mode {
            Color32::from_rgb(127, 218, 177)
        } else {
            Color32::from_rgb(23, 117, 77)
        }
    } else {
        ui.visuals().weak_text_color()
    };
    ui.colored_label(
        color,
        RichText::new(format!("{} {label}", if on { "●" } else { "○" })).strong(),
    );
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn command_quotes_values_for_virtual_machine_shell() {
        let command = claude_command("office-mac", "http://192.168.1.20:18765/mcp", "a'b$(test)");
        assert!(command.contains("'Authorization: Bearer a'\\''b$(test)'"));
        assert!(command.contains("--scope user --transport http 'office-mac'"));
    }
    #[test]
    fn local_connection_uses_loopback_regardless_of_selected_interface() {
        assert_eq!(
            connection_endpoint(true, "192.168.1.20", "127.0.0.1", 18765).unwrap(),
            "http://127.0.0.1:18765/mcp"
        );
    }
    #[test]
    fn lan_connection_requires_reachable_listener_and_concrete_address() {
        assert!(connection_endpoint(false, "192.168.1.20", "127.0.0.1", 18765).is_err());
        for bad in [
            "",
            "0.0.0.0",
            "127.0.0.1",
            "255.255.255.255",
            "224.0.0.1",
            "192.168.1.20/path",
            "example.com",
        ] {
            assert!(
                connection_endpoint(false, bad, "0.0.0.0", 18765).is_err(),
                "{bad}"
            );
        }
        assert_eq!(
            connection_endpoint(false, "192.168.1.20", "0.0.0.0", 18765).unwrap(),
            "http://192.168.1.20:18765/mcp"
        );
    }
}
