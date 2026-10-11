#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod app;
mod client;
mod host;

fn main() -> eframe::Result {
    eframe::run_native(
        "MCP Shell · Claude 主机服务",
        eframe::NativeOptions {
            viewport: eframe::egui::ViewportBuilder::default()
                .with_inner_size([1160.0, 820.0])
                .with_min_inner_size([900.0, 660.0]),
            ..Default::default()
        },
        Box::new(|cc| Ok(Box::new(app::Desktop::new(cc)))),
    )
}
