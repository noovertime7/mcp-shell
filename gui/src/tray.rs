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

        let _ = menu.append_items(&[&status_item, &sep, &show_item, &toggle_item, &sep, &quit_item]);

        let icon = create_status_icon(false, false);
        let tray = TrayIconBuilder::new()
            .with_menu(Box::new(menu))
            .with_tooltip("MCP Shell · Claude 主机服务")
            .with_icon(icon)
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
        let title = if running {
            format!("MCP Shell · 运行中 ({bind}:{port})")
        } else {
            "MCP Shell · 未启动".to_string()
        };
        self.status_item.set_text(title);
        self.toggle_item
            .set_text(if running { "停止服务" } else { "启动服务" });
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

pub fn create_status_icon(running: bool, active: bool) -> Icon {
    let (r, g, b) = if active {
        (241, 196, 15) // Yellow: active requests
    } else if running {
        (46, 204, 113) // Green: running
    } else {
        (149, 165, 166) // Grey: stopped
    };

    let width = 18;
    let height = 18;
    let mut rgba = Vec::with_capacity((width * height * 4) as usize);
    for y in 0..height {
        for x in 0..width {
            let dx = (x as f32) - 8.5;
            let dy = (y as f32) - 8.5;
            let dist_sq = dx * dx + dy * dy;
            if dist_sq <= 36.0 {
                rgba.extend_from_slice(&[r, g, b, 255]);
            } else if dist_sq <= 48.0 {
                let alpha = ((48.0 - dist_sq) / 12.0 * 255.0) as u8;
                rgba.extend_from_slice(&[r, g, b, alpha]);
            } else {
                rgba.extend_from_slice(&[0, 0, 0, 0]);
            }
        }
    }
    Icon::from_rgba(rgba, width, height).unwrap()
}
