package server

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

// readOnlyTools only look at things; their output is not echoed to the log.
var readOnlyTools = map[string]bool{
	"list_roots": true, "list_dir": true, "read_file": true, "glob": true, "grep": true,
	"read_process_output": true, "list_processes": true,
	"system_info": true, "get_env": true, "operation_history": true,
}

func toolLabel(name string) string {
	labels := map[string]string{
		"list_roots": "查看主机信息", "list_dir": "列出目录", "read_file": "读取文件",
		"write_file": "写入文件", "edit_file": "修改文件", "multi_edit": "批量修改文件", "delete_path": "删除路径",
		"move_path": "移动路径", "glob": "查找文件", "grep": "搜索文件内容",
		"run_command": "执行命令", "start_process": "启动后台进程",
		"read_process_output": "读取进程输出", "list_processes": "查看后台进程", "stop_process": "停止后台进程",
		"system_info": "查看系统信息", "get_env": "查看环境变量", "operation_history": "查看操作历史",
	}
	if label, ok := labels[name]; ok {
		return label
	}
	return name
}

// logCall writes one human-readable operation-log entry per tool call:
//
//	[tool] edit_file  OK 12ms  path=web/src/a.ts  -3/+5 lines
//	       => replaced 1 occurrence(s) in D:\ws\web\src\a.ts
//	       - old: …
//	       + new: …
func (s *Server) logCall(name string, in any, out string, err error, elapsed time.Duration) {
	status := "成功"
	switch st, _ := callStatus(name, out, err); st {
	case "running":
		status = "转入后台"
	case "timeout":
		status = "超时"
	case "failed", "error":
		status = "失败"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[操作完成] %s | %s (%s) | 耗时=%s | %s", status, toolLabel(name), name, elapsed.Round(time.Millisecond), describeArgs(in))

	switch v := in.(type) {
	case editFileIn:
		fmt.Fprintf(&b, "  删除=%d行 新增=%d行", countLines(v.OldString), countLines(v.NewString))
		if v.ReplaceAll {
			b.WriteString(" (全部替换)")
		}
	case writeFileIn:
		fmt.Fprintf(&b, "  %d 字节, %d 行", len(v.Content), countLines(v.Content))
	case multiEditIn:
		removed, added := 0, 0
		for _, e := range v.Edits {
			removed += countLines(e.OldString)
			added += countLines(e.NewString)
		}
		fmt.Fprintf(&b, "  %d 处修改 删除=%d行 新增=%d行", len(v.Edits), removed, added)
	}

	if err != nil {
		fmt.Fprintf(&b, "\n       => 错误详情: %s", oneLine(err.Error(), 500))
	} else if !readOnlyTools[name] {
		for i, line := range strings.SplitN(strings.TrimRight(out, "\n"), "\n", 4) {
			if i == 3 {
				break
			}
			fmt.Fprintf(&b, "\n       => %s", oneLine(line, 300))
		}
	}
	if v, ok := in.(editFileIn); ok && err == nil {
		fmt.Fprintf(&b, "\n       - 修改前: %s\n       + 修改后: %s", oneLine(v.OldString, 200), oneLine(v.NewString, 200))
	}
	log.Print(b.String())
}

// describeArgs renders the short arguments of a call as key=value pairs;
// file contents and edit strings are left out (they get their own lines).
func describeArgs(in any) string {
	m := summarize(in)
	keys := []string{"path", "src", "dst", "pattern", "glob", "id", "name", "cwd", "shell", "timeout_sec", "command"}
	labels := map[string]string{"path": "路径", "src": "源路径", "dst": "目标路径", "pattern": "匹配规则", "glob": "文件过滤", "id": "进程ID", "name": "名称", "cwd": "工作目录", "shell": "终端", "timeout_sec": "超时秒数", "command": "命令"}
	var parts []string
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		str := fmt.Sprint(v)
		if str == "" || str == "0" || str == "false" {
			continue
		}
		if strings.ContainsAny(str, " \t\n\"") {
			str = fmt.Sprintf("%q", oneLine(str, 300))
		}
		parts = append(parts, labels[k]+"="+str)
	}
	return strings.Join(parts, " ")
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// oneLine collapses whitespace and clips to max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}
