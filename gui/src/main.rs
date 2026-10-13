#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod app;
mod caffeinate;
mod client;
mod dashboard;
mod host;
mod tray;

fn main() -> eframe::Result {
    eframe::run_native(
        "MCP Shell · Claude 主机服务",
        native_options(),
        Box::new(|cc| Ok(Box::new(app::Desktop::new(cc)))),
    )
}

fn native_options() -> eframe::NativeOptions {
    // eframe also sets NSApplication's Dock icon at runtime. The bundle ICNS
    // alone is not enough: leaving this unset replaces it with the egui logo.
    let icon =
        eframe::icon_data::from_png_bytes(include_bytes!("../../assets/brand/app-icon-1024.png"))
            .expect("embedded MCP Shell application icon must be valid PNG");
    eframe::NativeOptions {
        viewport: eframe::egui::ViewportBuilder::default()
            .with_icon(icon)
            .with_inner_size([1160.0, 820.0])
            .with_min_inner_size([900.0, 660.0]),
        ..Default::default()
    }
}

#[cfg(test)]
mod icon_tests {
    #[test]
    fn startup_uses_brand_icon_instead_of_egui_default() {
        let options = super::native_options();
        let icon = options.viewport.icon.expect("Dock icon must be explicit");
        assert_eq!((icon.width, icon.height), (1024, 1024));
        assert_eq!(icon.rgba.len(), 1024 * 1024 * 4);
        assert_eq!(icon.rgba[3], 0, "preserve transparent icon margins");
        assert!(icon.rgba.chunks_exact(4).any(|pixel| pixel[3] == 255));
    }
}
