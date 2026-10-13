use tray_icon::{
    menu::{Menu, MenuEvent, MenuItem, PredefinedMenuItem},
    Icon, TrayIcon, TrayIconBuilder, TrayIconEvent,
};

pub enum TrayCommand {
    ShowWindow,
    ToggleService,
    Quit,
}

pub struct TrayManager {
    _tray: TrayIcon,
    status_item: MenuItem,
    toggle_item: MenuItem,
    show_item: MenuItem,
    quit_item: MenuItem,
}

impl TrayManager {
    pub fn new() -> Result<Self, String> {
        let menu = Menu::new();
        let status_item = MenuItem::new("MCP Shell · 未启动", false, None);
        let show_item = MenuItem::new("显示主界面", true, None);
        let toggle_item = MenuItem::new("启动服务", true, None);
        let sep = PredefinedMenuItem::separator();
        let quit_item = MenuItem::new("退出程序", true, None);

        let _ = menu.append_items(&[
            &status_item,
            &sep,
            &show_item,
            &toggle_item,
            &sep,
            &quit_item,
        ]);

        let icon = create_status_icon(false, false);
        let tray = TrayIconBuilder::new()
            .with_menu(Box::new(menu))
            .with_tooltip("MCP Shell · Claude 主机服务")
            .with_icon(icon)
            .with_icon_as_template(cfg!(target_os = "macos"))
            .build()
            .map_err(|e| format!("无法创建状态栏图标：{e}"))?;

        Ok(Self {
            _tray: tray,
            status_item,
            toggle_item,
            show_item,
            quit_item,
        })
    }

    pub fn update_status(&mut self, running: bool, active_request: bool, bind: &str, port: u16) {
        let icon = create_status_icon(running, active_request);
        let _ = self._tray.set_icon(Some(icon));
        // tray-icon 0.24 resets the NSImage template flag when replacing it.
        self._tray.set_icon_as_template(cfg!(target_os = "macos"));
        let title = if running && active_request {
            format!("MCP Shell · 正在处理请求 ({bind}:{port})")
        } else if running {
            format!("MCP Shell · 运行中 ({bind}:{port})")
        } else {
            "MCP Shell · 未启动".to_string()
        };
        let _ = self._tray.set_tooltip(Some(&title));
        self.status_item.set_text(title);
        self.toggle_item.set_text(if running {
            "停止服务"
        } else {
            "启动服务"
        });
    }

    pub fn poll_command(&self) -> Option<TrayCommand> {
        if let Ok(event) = MenuEvent::receiver().try_recv() {
            if event.id == self.show_item.id() {
                return Some(TrayCommand::ShowWindow);
            }
            if event.id == self.toggle_item.id() {
                return Some(TrayCommand::ToggleService);
            }
            if event.id == self.quit_item.id() {
                return Some(TrayCommand::Quit);
            }
        }
        if let Ok(TrayIconEvent::Click { .. }) = TrayIconEvent::receiver().try_recv() {
            return Some(TrayCommand::ShowWindow);
        }
        None
    }
}

// Render at 2x resolution. macOS scales this to an 18-point template image,
// applying the system foreground color for light/dark menu bars.
// The terminal silhouette stays visible in every state; no colored status dot.
pub fn create_status_icon(running: bool, active: bool) -> Icon {
    let size = 36;
    let mut rgba = Vec::with_capacity((size * size * 4) as usize);
    let opacity = if running { 255.0 } else { 160.0 };
    let (r, g, b) = if cfg!(target_os = "macos") {
        (0, 0, 0)
    } else if running && active {
        (241, 196, 15)
    } else if running {
        (46, 204, 113)
    } else {
        (149, 165, 166)
    };
    for y in 0..size {
        for x in 0..size {
            let mut coverage = 0;
            // Supersampling keeps the diagonal and rounded frame crisp at 18pt.
            for sy in 0..4 {
                for sx in 0..4 {
                    let px = (x as f32 + (sx as f32 + 0.5) / 4.0) / 2.0;
                    let py = (y as f32 + (sy as f32 + 0.5) / 4.0) / 2.0;
                    if terminal_ink(px, py, running && active) {
                        coverage += 1;
                    }
                }
            }
            rgba.extend_from_slice(&[r, g, b, (opacity * coverage as f32 / 16.0).round() as u8]);
        }
    }
    Icon::from_rgba(rgba, size, size).expect("fixed-size tray icon has valid RGBA data")
}

fn terminal_ink(x: f32, y: f32, active: bool) -> bool {
    // Rounded rectangle centered at (9,9), 15x12, with 2pt corner radii.
    let qx = (x - 9.0).abs() - 5.5;
    let qy = (y - 9.0).abs() - 4.0;
    let frame_distance = qx.max(0.0).hypot(qy.max(0.0)) + qx.max(qy).min(0.0) - 2.0;
    frame_distance.abs() <= 0.65
        || segment_distance(x, y, [4.5, 6.5], [7.5, 9.0]) <= 0.75
        || segment_distance(x, y, [7.5, 9.0], [4.5, 11.5]) <= 0.75
        || segment_distance(x, y, [9.5, 11.5], [12.8, 11.5]) <= 0.75
        // A second cursor line signals activity without a flashing status dot.
        || (active && segment_distance(x, y, [9.5, 8.5], [12.8, 8.5]) <= 0.65)
}

fn segment_distance(x: f32, y: f32, a: [f32; 2], b: [f32; 2]) -> f32 {
    let dx = b[0] - a[0];
    let dy = b[1] - a[1];
    let t = (((x - a[0]) * dx + (y - a[1]) * dy) / (dx * dx + dy * dy)).clamp(0.0, 1.0);
    (x - a[0] - t * dx).hypot(y - a[1] - t * dy)
}
