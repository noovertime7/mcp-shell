use std::process::{Child, Command};

pub struct CaffeinateGuard {
    child: Option<Child>,
    pub enabled: bool,
}

impl CaffeinateGuard {
    pub fn new() -> Self {
        Self {
            child: None,
            enabled: true,
        }
    }

    pub fn set_enabled(&mut self, enabled: bool, running: bool) {
        self.enabled = enabled;
        if enabled && running {
            self.start();
        } else {
            self.stop();
        }
    }

    pub fn on_service_running(&mut self, running: bool) {
        if self.enabled && running {
            self.start();
        } else {
            self.stop();
        }
    }

    pub fn is_active(&self) -> bool {
        self.child.is_some()
    }

    fn start(&mut self) {
        if self.child.is_some() {
            return;
        }
        #[cfg(target_os = "macos")]
        {
            let child = Command::new("/usr/bin/caffeinate")
                .args(["-i", "-s", "-m"])
                .stdin(std::process::Stdio::null())
                .stdout(std::process::Stdio::null())
                .stderr(std::process::Stdio::null())
                .spawn();
            if let Ok(c) = child {
                self.child = Some(c);
            }
        }
    }

    fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

impl Drop for CaffeinateGuard {
    fn drop(&mut self) {
        self.stop();
    }
}
