use serde::Deserialize;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::fs::File;
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};

#[derive(Clone, Deserialize, Debug)]
#[allow(dead_code)]
pub struct HistoryRecord {
    pub id: i64,
    pub time: String,
    pub tool: String,
    #[serde(default)]
    pub args: Option<serde_json::Value>,
    pub status: String,
    pub exit_code: Option<i32>,
    pub duration_ms: i64,
    pub error: Option<String>,
    pub result: Option<String>,
    pub client: Option<String>,
}

#[derive(Clone, Debug, Default)]
pub struct ModifiedFileInfo {
    pub path: String,
    pub actions: Vec<String>,
    pub last_time: String,
    pub count: usize,
}

#[derive(Clone, Debug, Default)]
pub struct SlowCommandInfo {
    pub command: String,
    pub duration_ms: i64,
    pub time: String,
    pub exit_code: Option<i32>,
}

#[derive(Clone, Debug, Default)]
#[allow(dead_code)]
pub struct DaySummary {
    pub date: String,
    pub total_calls: usize,
    pub success_calls: usize,
    pub failed_calls: usize,
    pub total_duration_ms: i64,

    // Categories
    pub read_count: usize,
    pub write_count: usize,
    pub cmd_count: usize,
    pub sys_count: usize,

    // Tool breakdown: tool -> count
    pub tool_counts: BTreeMap<String, usize>,

    // Modified files
    pub modified_files: Vec<ModifiedFileInfo>,

    // Slowest commands
    pub slow_commands: Vec<SlowCommandInfo>,

    // Errors
    pub failed_records: Vec<HistoryRecord>,

    // Clients
    pub clients: Vec<String>,
}

impl DaySummary {
    pub fn load_from_file(path: &Path, date: String) -> Self {
        let mut summary = DaySummary {
            date,
            ..Default::default()
        };
        let Ok(file) = File::open(path) else {
            return summary;
        };

        let mut file_map: HashMap<String, ModifiedFileInfo> = HashMap::new();
        let mut slow_cmds: Vec<SlowCommandInfo> = Vec::new();
        let mut clients_set: HashSet<String> = HashSet::new();

        for line in BufReader::new(file).lines().map_while(Result::ok) {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            let Ok(record) = serde_json::from_str::<HistoryRecord>(line) else {
                continue;
            };

            summary.total_calls += 1;
            if record.status == "ok" {
                summary.success_calls += 1;
            } else {
                summary.failed_calls += 1;
                summary.failed_records.push(record.clone());
            }
            summary.total_duration_ms += record.duration_ms;

            *summary.tool_counts.entry(record.tool.clone()).or_insert(0) += 1;

            if let Some(c) = &record.client {
                if !c.is_empty() {
                    clients_set.insert(c.clone());
                }
            }

            match record.tool.as_str() {
                "read_file" | "list_dir" | "grep" | "glob" | "list_roots" => {
                    summary.read_count += 1;
                }
                "write_file" | "edit_file" | "multi_edit" | "delete_path" | "move_path" => {
                    summary.write_count += 1;
                    if let Some(args) = &record.args {
                        let path_str = args
                            .get("path")
                            .or_else(|| args.get("src"))
                            .or_else(|| args.get("dst"))
                            .and_then(|v| v.as_str());
                        if let Some(p) = path_str {
                            let entry = file_map.entry(p.to_string()).or_insert_with(|| {
                                ModifiedFileInfo {
                                    path: p.to_string(),
                                    actions: Vec::new(),
                                    last_time: format_time(&record.time),
                                    count: 0,
                                }
                            });
                            entry.actions.push(record.tool.clone());
                            entry.last_time = format_time(&record.time);
                            entry.count += 1;
                        }
                    }
                }
                "run_command" | "start_process" | "stop_process" | "read_process_output"
                | "write_process_input" => {
                    summary.cmd_count += 1;
                    if (record.tool == "run_command" || record.tool == "start_process")
                        && record.duration_ms > 0
                    {
                        let cmd_str = record
                            .args
                            .as_ref()
                            .and_then(|a| a.get("command").and_then(|v| v.as_str()))
                            .unwrap_or("-")
                            .to_string();
                        slow_cmds.push(SlowCommandInfo {
                            command: cmd_str,
                            duration_ms: record.duration_ms,
                            time: format_time(&record.time),
                            exit_code: record.exit_code,
                        });
                    }
                }
                _ => summary.sys_count += 1,
            }
        }

        let mut mod_files: Vec<ModifiedFileInfo> = file_map.into_values().collect();
        mod_files.sort_by(|a, b| b.count.cmp(&a.count));
        summary.modified_files = mod_files;

        slow_cmds.sort_by(|a, b| b.duration_ms.cmp(&a.duration_ms));
        slow_cmds.truncate(15);
        summary.slow_commands = slow_cmds;

        let mut clients: Vec<String> = clients_set.into_iter().collect();
        clients.sort();
        summary.clients = clients;

        summary
    }
}

pub fn list_history_dates(history_dirs: &[PathBuf]) -> Vec<(String, PathBuf)> {
    let mut map: BTreeMap<String, PathBuf> = BTreeMap::new();
    for dir in history_dirs {
        if let Ok(entries) = std::fs::read_dir(dir) {
            for entry in entries.flatten() {
                let path = entry.path();
                if path.extension().and_then(|s| s.to_str()) == Some("jsonl") {
                    if let Some(stem) = path.file_stem().and_then(|s| s.to_str()) {
                        if stem.len() == 10
                            && stem.chars().nth(4) == Some('-')
                            && stem.chars().nth(7) == Some('-')
                        {
                            map.insert(stem.to_string(), path);
                        }
                    }
                }
            }
        }
    }
    let mut list: Vec<(String, PathBuf)> = map.into_iter().collect();
    list.sort_by(|a, b| b.0.cmp(&a.0));
    list
}

pub fn format_time(iso: &str) -> String {
    if let Some((_, time_part)) = iso.split_once('T') {
        if let Some(colon) = time_part.find('+') {
            return time_part[..colon].chars().take(8).collect();
        }
        if let Some(z) = time_part.find('Z') {
            return time_part[..z].chars().take(8).collect();
        }
        return time_part.chars().take(8).collect();
    }
    iso.to_string()
}
