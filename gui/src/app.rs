use crate::host::{self, Action, Event, HostConfig, Supervisor};
use eframe::egui::{self, Color32, RichText, TextEdit};
use serde_json::json;
use std::{collections::VecDeque, time::Duration};

const BLUE: Color32 = Color32::from_rgb(45, 99, 221);
const MUTED: Color32 = Color32::from_rgb(108, 122, 145);

#[derive(PartialEq)]
enum Page {
    Console,
    Claude,
    Settings,
}

pub struct Desktop {
    page: Page,
    supervisor: Supervisor,
    host: HostConfig,
    running: bool,
    busy: bool,
    tunnel: bool,
    tunnel_busy: bool,
    tunnel_url: String,
    notice: String,
    error: bool,
    logs: VecDeque<String>,
    paused: bool,
    follow: bool,
    filter: String,
    operations: usize,
    interfaces: Vec<(String, String)>,
    address: String,
    server_name: String,
    use_tunnel: bool,
    show_token: bool,
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
            .first()
            .map(|(_, ip)| ip.clone())
            .unwrap_or_else(|| "127.0.0.1".into());
        let desktop = Self {
            page: Page::Console,
            supervisor: Supervisor::new(),
            host,
            running: false,
            busy: false,
            tunnel: false,
            tunnel_busy: false,
            tunnel_url: String::new(),
            notice,
            error,
            logs: VecDeque::new(),
            paused: false,
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
            use_tunnel: cfg!(target_os = "macos"),
            show_token: false,
        };
        #[cfg(feature = "screenshot")]
        let desktop = {
            let mut desktop = desktop;
            if let Ok(path) = std::env::var("MCP_SHELL_PREVIEW_LOGS") {
                if let Ok(text) = std::fs::read_to_string(path) {
                    for line in text.lines() {
                        desktop.push_log(line.into());
                    }
                    desktop.notice = "本地集成测试实际日志回放；当前未启动共享服务".into();
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
    fn push_log(&mut self, line: String) {
        if line.contains("[操作开始]") {
            self.operations += 1;
        }
        self.logs.push_back(line);
        if self.logs.len() > 2000 {
            self.logs.pop_front();
        }
    }
    fn poll(&mut self) {
        while let Ok(event) = self.supervisor.events.try_recv() {
            match event {
                Event::Running(value) => {
                    self.running = value;
                    self.busy = false;
                    self.push_log(format!(
                        "[服务] {}",
                        if value {
                            "MCP 服务已就绪，等待虚拟机 Claude 连接"
                        } else {
                            "MCP 服务已停止"
                        }
                    ));
                }
                Event::Tunnel(value) => {
                    self.tunnel = value;
                    self.tunnel_busy = false;
                }
                Event::Url(url) => self.tunnel_url = url,
                Event::Error(error) => {
                    self.push_log(format!("[错误] {error}"));
                    self.message(error, true);
                }
            }
        }
        if !self.paused {
            while let Ok(line) = self.supervisor.logs.try_recv() {
                self.push_log(line);
            }
        }
    }

    fn controls(&mut self, ui: &mut egui::Ui) {
        card(ui, |ui| {
            ui.horizontal_wrapped(|ui| {
                badge(
                    ui,
                    if self.running {
                        "MCP 服务运行中"
                    } else if self.busy {
                        "服务处理中…"
                    } else {
                        "MCP 服务未启动"
                    },
                    self.running,
                );
                badge(
                    ui,
                    if !self.tunnel_url.is_empty() {
                        "公网地址已生成"
                    } else if self.tunnel {
                        "隧道连接中…"
                    } else {
                        "隧道未开启"
                    },
                    self.tunnel,
                );
                ui.label(format!("本次操作：{}", self.operations));
            });
            ui.horizontal_wrapped(|ui| {
                if !self.running {
                    if ui
                        .add_enabled(!self.busy, primary("启动 MCP 服务"))
                        .clicked()
                    {
                        match self.host.save() {
                            Ok(()) => {
                                self.busy = true;
                                self.supervisor.send(Action::Start(self.host.clone()));
                                self.message("正在启动 MCP 服务…", false);
                            }
                            Err(e) => self.message(format!("保存设置失败：{e}"), true),
                        }
                    }
                } else if ui
                    .add_enabled(!self.busy, egui::Button::new("停止服务"))
                    .clicked()
                {
                    self.busy = true;
                    self.supervisor.send(Action::Stop);
                }
                if !self.tunnel {
                    if ui
                        .add_enabled(
                            self.running && !self.busy && !self.tunnel_busy,
                            egui::Button::new("开启公网隧道"),
                        )
                        .clicked()
                    {
                        self.tunnel_busy = true;
                        self.tunnel_url.clear();
                        self.supervisor.send(Action::TunnelStart);
                        self.message("正在申请公网地址，可在下方查看隧道进度", false);
                    }
                } else if ui
                    .add_enabled(
                        !self.busy && !self.tunnel_busy,
                        egui::Button::new("关闭隧道"),
                    )
                    .clicked()
                {
                    self.tunnel_busy = true;
                    self.supervisor.send(Action::TunnelStop);
                }
                if ui.button("复制 Claude 连接配置").clicked() {
                    self.page = Page::Claude;
                }
            });
            ui.label(
                RichText::new(format!(
                    "访问范围：{}    ·    工具执行：自动放行    ·    监听：{}:{}",
                    if self.host.unrestricted {
                        "全部文件（当前系统用户权限）"
                    } else {
                        "指定目录"
                    },
                    self.host.bind,
                    self.host.port
                ))
                .small()
                .color(MUTED),
            );
        });
    }

    fn console(&mut self, ui: &mut egui::Ui) {
        self.controls(ui);
        ui.add_space(12.0);
        ui.horizontal_wrapped(|ui| {
            ui.strong("实时中文日志");
            ui.checkbox(&mut self.follow, "跟随最新");
            ui.checkbox(&mut self.paused, "暂停显示");
            if ui.button("清空显示").clicked() {
                self.logs.clear();
            }
            if ui.button("复制日志").clicked() {
                ui.ctx().copy_text(self.visible_logs());
            }
            ui.add(
                TextEdit::singleline(&mut self.filter)
                    .hint_text("筛选命令、路径或状态")
                    .desired_width(200.0),
            );
        });
        ui.label(RichText::new("显示操作开始、实时输出和执行结果；不需要在 GUI 手动执行命令。最多保留 2000 条，完整日志持续写入磁盘。").small().color(MUTED));
        let height = (ui.available_height() - 12.0).max(240.0);
        egui::Frame::new().fill(Color32::from_rgb(17, 26, 43)).corner_radius(8.0).inner_margin(16.0).show(ui, |ui| {
            ui.set_min_width(ui.available_width());
            egui::ScrollArea::both().id_salt("live-logs").stick_to_bottom(self.follow).auto_shrink([false, false]).max_height(height-32.0).show(ui, |ui| {
                ui.set_min_height(height-32.0);
                if self.logs.is_empty() {
                    ui.colored_label(Color32::from_rgb(158, 176, 203), "等待 MCP 请求…\n\n在虚拟机 Claude 中添加本机服务后，操作日志会在这里实时显示。");
                }
                for line in &self.logs {
                    if !self.filter.is_empty() && !line.contains(&self.filter) { continue; }
                    let color = if line.contains("[操作完成] 失败") || line.contains("[错误]") || line.contains("[认证] 拒绝") {
                        Color32::from_rgb(255, 137, 137)
                    } else if line.contains("[操作开始]") || line.contains("[MCP连接]") {
                        Color32::from_rgb(118, 180, 255)
                    } else if line.contains("[操作完成] 成功") { Color32::from_rgb(123, 213, 175) }
                    else { Color32::from_rgb(211, 222, 239) };
                    ui.add(egui::Label::new(RichText::new(line).monospace().color(color)).selectable(true));
                }
            });
        });
    }
    fn visible_logs(&self) -> String {
        self.logs
            .iter()
            .filter(|s| self.filter.is_empty() || s.contains(&self.filter))
            .cloned()
            .collect::<Vec<_>>()
            .join("\n")
    }

    fn claude(&mut self, ui: &mut egui::Ui) {
        ui.heading("接入虚拟机里的 Claude");
        ui.label("你始终在虚拟机中与 Claude 对话。分别添加宿主机和办公 Mac 两个 MCP 服务即可。");
        ui.add_space(12.0);
        card(ui, |ui| {
            field(
                ui,
                "服务名称",
                &mut self.server_name,
                "宿主机 host-pc / 办公 Mac office-mac",
            );
            ui.horizontal(|ui| {
                ui.radio_value(&mut self.use_tunnel, false, "虚拟机直连宿主机");
                ui.radio_value(&mut self.use_tunnel, true, "通过公网隧道连接");
            });
            if !self.use_tunnel {
                ui.horizontal(|ui| {
                    ui.label("宿主机地址");
                    ui.text_edit_singleline(&mut self.address);
                    egui::ComboBox::from_id_salt("interfaces")
                        .selected_text("选择网卡地址")
                        .show_ui(ui, |ui| {
                            for (name, ip) in &self.interfaces {
                                ui.selectable_value(
                                    &mut self.address,
                                    ip.clone(),
                                    format!("{name} · {ip}"),
                                );
                            }
                        });
                });
                ui.label(
                    RichText::new(
                        "VMware 通常选择 VMnet8 / VMnet1 对应地址；桥接网络可选择局域网地址。",
                    )
                    .small()
                    .color(MUTED),
                );
                if self.host.bind == "127.0.0.1" {
                    ui.colored_label(
                        Color32::from_rgb(170, 93, 20),
                        "当前仅监听回环地址。直连请在服务设置中切换为「局域网＋隧道」。",
                    );
                }
            } else {
                ui.label(if self.tunnel_url.is_empty() {
                    "先启动服务并开启公网隧道，生成地址后即可复制命令。"
                } else {
                    &self.tunnel_url
                });
            }
            ui.horizontal(|ui| {
                ui.label("访问令牌");
                let mut token = self.host.token.clone();
                ui.add(
                    TextEdit::singleline(&mut token)
                        .password(!self.show_token)
                        .interactive(false)
                        .desired_width(420.0),
                );
                ui.checkbox(&mut self.show_token, "显示");
            });
            ui.label(
                RichText::new(
                    "令牌会保存并在重启后复用。隧道临时域名可能变化，届时更新 Claude 的服务地址。",
                )
                .small()
                .color(MUTED),
            );
        });
        let endpoint = if self.use_tunnel {
            self.tunnel_url.clone()
        } else {
            format!("http://{}:{}/mcp", self.address.trim(), self.host.port)
        };
        let name = self.server_name.trim();
        let valid = !name.is_empty()
            && name
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
            && !endpoint.is_empty();
        ui.add_space(12.0);
        card(ui, |ui| {
            ui.strong("在虚拟机终端执行");
            let mut preview = claude_command(
                name,
                &endpoint,
                if self.show_token {
                    &self.host.token
                } else {
                    "<访问令牌>"
                },
            );
            ui.add(
                TextEdit::multiline(&mut preview)
                    .code_editor()
                    .interactive(false)
                    .desired_rows(3)
                    .desired_width(f32::INFINITY),
            );
            ui.horizontal(|ui| {
                if ui.add_enabled(valid && self.running, primary("复制接入命令（含令牌）")).clicked() { ui.ctx().copy_text(claude_command(name,&endpoint,&self.host.token)); }
                if ui.add_enabled(valid && self.running, egui::Button::new("复制 MCP JSON（含令牌）")).clicked() {
                    ui.ctx().copy_text(serde_json::to_string_pretty(&json!({"mcpServers":{(name):{"type":"http","url":endpoint,"headers":{"Authorization":format!("Bearer {}",self.host.token)}}}})).unwrap());
                }
            });
            ui.label("添加后在 Claude 中执行 /mcp 检查连接。已有同名配置时，先运行 claude mcp remove <服务名称> --scope user，再重新添加。");
        });
        ui.add_space(12.0);
        card(ui, |ui| {
            ui.strong("默认放行全部 MCP 工具");
            ui.label("本程序收到请求后直接执行，无需 GUI 确认。Claude 自身的工具授权需在虚拟机设置里允许对应 MCP 服务。");
            if ui.button("复制 Claude 权限配置片段").clicked() {
                ui.ctx().copy_text(
                    serde_json::to_string_pretty(
                        &json!({"permissions":{"allow":["mcp__host-pc__*","mcp__office-mac__*"]}}),
                    )
                    .unwrap(),
                );
            }
            ui.label(RichText::new("将片段合并到虚拟机 ~/.claude/settings.json 的现有 permissions.allow，不要覆盖其他设置。自定义服务名时同步替换。").small().color(MUTED));
            ui.label(
                "对话示例：『用 host-pc 检查宿主机项目，再用 office-mac 修改办公 Mac 上的文件。』",
            );
        });
    }

    fn settings(&mut self, ui: &mut egui::Ui) {
        ui.heading("服务设置");
        ui.label("服务运行时设置保持锁定。停止后修改，下一次启动生效。");
        ui.add_space(12.0);
        card(ui, |ui| {
            ui.add_enabled_ui(!self.running && !self.busy, |ui| {
                ui.strong("监听与访问范围");
                ui.horizontal(|ui| {
                    ui.radio_value(
                        &mut self.host.bind,
                        "0.0.0.0".into(),
                        "局域网＋隧道（宿主机）",
                    );
                    ui.radio_value(
                        &mut self.host.bind,
                        "127.0.0.1".into(),
                        "仅隧道 / 本机（办公 Mac）",
                    );
                });
                ui.horizontal(|ui| {
                    ui.label("端口");
                    ui.add(egui::DragValue::new(&mut self.host.port).range(1024..=65535));
                });
                ui.checkbox(
                    &mut self.host.unrestricted,
                    "全文件访问：不限制目录，所有工具直接执行（默认）",
                );
                ui.label(if self.host.unrestricted {
                    "下方目录只决定默认工作目录，不限制可操作的路径。"
                } else {
                    "关闭全文件访问后，文件工具仅能操作下列目录。"
                });
                ui.add(
                    TextEdit::multiline(&mut self.host.roots)
                        .desired_width(f32::INFINITY)
                        .desired_rows(3),
                );
                ui.label("保留操作系统自身权限；程序不会自动提权。macOS 默认使用 zsh。");
                ui.separator();
                field(ui, "服务程序", &mut self.host.server, "mcp-shell");
                field(ui, "隧道程序", &mut self.host.cloudflared, "cloudflared");
                ui.horizontal(|ui| {
                    if ui.add(primary("保存设置")).clicked() {
                        match self.host.save() {
                            Ok(()) => self.message("设置已保存", false),
                            Err(e) => self.message(e, true),
                        }
                    }
                    if ui.button("更换访问令牌").clicked() {
                        self.host.token = host::token();
                        match self.host.save() {
                            Ok(()) => {
                                self.message("令牌已更新，请同步更新虚拟机 Claude 的配置", false)
                            }
                            Err(e) => self.message(e, true),
                        }
                    }
                });
            });
        });
        ui.add_space(12.0);
        if let Ok(state) = host::state_dir() {
            ui.label("配置与完整日志目录");
            ui.monospace(state.display().to_string());
            if ui.button("复制日志目录").clicked() {
                ui.ctx().copy_text(state.display().to_string());
            }
        }
        ui.label("macOS 若访问桌面、文稿等目录遇到系统权限限制，请在系统设置中为应用授予相应文件访问权限。");
    }
}

impl eframe::App for Desktop {
    fn update(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        self.poll();
        ctx.request_repaint_after(Duration::from_millis(100));
        egui::SidePanel::left("navigation")
            .resizable(false)
            .exact_width(196.0)
            .frame(
                egui::Frame::new()
                    .fill(Color32::from_rgb(22, 33, 54))
                    .inner_margin(18.0),
            )
            .show(ctx, |ui| {
                ui.visuals_mut().override_text_color = Some(Color32::from_rgb(226, 234, 248));
                ui.add_space(16.0);
                ui.heading("MCP SHELL");
                ui.label("Claude 主机服务");
                ui.add_space(30.0);
                for (page, label) in [
                    (Page::Console, "服务控制台"),
                    (Page::Claude, "接入 Claude"),
                    (Page::Settings, "服务设置"),
                ] {
                    ui.selectable_value(&mut self.page, page, label);
                    ui.add_space(8.0);
                }
                ui.add_space(28.0);
                ui.separator();
                ui.label("虚拟机 Claude");
                ui.label("     ↓ MCP");
                ui.label("宿主机 / 办公 Mac");
                ui.add_space(20.0);
                ui.label(RichText::new("自动执行 · 中文实时日志").small());
            });
        egui::TopBottomPanel::bottom("status")
            .min_height(38.0)
            .show(ctx, |ui| {
                ui.add_space(5.0);
                ui.colored_label(
                    if self.error {
                        Color32::from_rgb(181, 44, 55)
                    } else {
                        MUTED
                    },
                    if self.notice.is_empty() {
                        "就绪 · 你在虚拟机里对话，Claude 通过 MCP 操作这台电脑"
                    } else {
                        &self.notice
                    },
                );
            });
        egui::CentralPanel::default()
            .frame(
                egui::Frame::new()
                    .fill(Color32::from_rgb(247, 249, 252))
                    .inner_margin(24.0),
            )
            .show(ctx, |ui| {
                if self.page == Page::Console {
                    ui.heading("主机服务控制台");
                    ui.label(
                        RichText::new("让虚拟机 Claude 操作这台电脑，在这里实时查看它正在做什么。")
                            .color(MUTED),
                    );
                    ui.add_space(12.0);
                    self.console(ui);
                } else {
                    egui::ScrollArea::vertical().show(ui, |ui| match self.page {
                        Page::Claude => self.claude(ui),
                        Page::Settings => self.settings(ui),
                        Page::Console => {}
                    });
                }
            });
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
    ctx.set_visuals(egui::Visuals::light());
    let mut style = (*ctx.style()).clone();
    style.spacing.item_spacing = egui::vec2(10.0, 10.0);
    style.spacing.button_padding = egui::vec2(14.0, 8.0);
    style.visuals.selection.bg_fill = BLUE;
    style.visuals.selection.stroke = egui::Stroke::new(1.0, Color32::WHITE);
    for font in style.text_styles.values_mut() {
        font.size += 1.0;
    }
    ctx.set_style(style);
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
        .fill(Color32::WHITE)
        .stroke(egui::Stroke::new(1.0, Color32::from_rgb(222, 228, 238)))
        .corner_radius(8.0)
        .inner_margin(16.0)
        .show(ui, |ui| {
            ui.set_width(ui.available_width());
            body(ui);
        });
}
fn field(ui: &mut egui::Ui, label: &str, value: &mut String, hint: &str) {
    ui.horizontal(|ui| {
        ui.label(label);
        ui.add(
            TextEdit::singleline(value)
                .hint_text(hint)
                .desired_width(f32::INFINITY),
        );
    });
}
fn primary(label: &str) -> egui::Button<'_> {
    egui::Button::new(RichText::new(label).color(Color32::WHITE)).fill(BLUE)
}
fn badge(ui: &mut egui::Ui, label: &str, on: bool) {
    ui.colored_label(
        if on {
            Color32::from_rgb(24, 135, 100)
        } else {
            MUTED
        },
        format!("{} {label}", if on { "●" } else { "○" }),
    );
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn command_quotes_values_for_virtual_machine_shell() {
        let command = claude_command("office-mac", "https://example.com/mcp", "a'b$(test)");
        assert!(command.contains("'Authorization: Bearer a'\\''b$(test)'"));
        assert!(command.contains("--scope user --transport http 'office-mac'"));
    }
}
