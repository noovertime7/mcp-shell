package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/noovertime7/mcp-shell/internal/exectools"
	"github.com/noovertime7/mcp-shell/internal/fstools"
	"github.com/noovertime7/mcp-shell/internal/history"
	"github.com/noovertime7/mcp-shell/internal/mcp"
	"github.com/noovertime7/mcp-shell/internal/sysinfo"
)

type listRootsIn struct{}

type listDirIn struct {
	Path       string `json:"path,omitempty" jsonschema:"directory to list; defaults to the first root"`
	Depth      int    `json:"depth,omitempty" jsonschema:"how many levels to descend (1-6, default 1)"`
	ShowHidden bool   `json:"show_hidden,omitempty" jsonschema:"include entries starting with a dot"`
}

type readFileIn struct {
	Path   string `json:"path" jsonschema:"file to read"`
	Offset int    `json:"offset,omitempty" jsonschema:"1-based line number to start at (default 1)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of lines to return (default 2000)"`
}

type writeFileIn struct {
	Path       string `json:"path" jsonschema:"file to create or overwrite"`
	Content    string `json:"content" jsonschema:"full new content of the file"`
	CreateDirs bool   `json:"create_dirs,omitempty" jsonschema:"create missing parent directories"`
}

type editFileIn struct {
	Path       string `json:"path" jsonschema:"file to edit"`
	OldString  string `json:"old_string" jsonschema:"exact text to replace; must be unique in the file unless replace_all is set"`
	NewString  string `json:"new_string" jsonschema:"replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence instead of requiring a unique match"`
}

type deletePathIn struct {
	Path      string `json:"path" jsonschema:"file or directory to delete"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"delete a non-empty directory and everything in it"`
}

type movePathIn struct {
	Src       string `json:"src" jsonschema:"existing file or directory"`
	Dst       string `json:"dst" jsonschema:"new path"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"replace dst if it is an existing file"`
}

type multiEditIn struct {
	Path  string         `json:"path" jsonschema:"file to edit"`
	Edits []fstools.Edit `json:"edits" jsonschema:"replacements applied in order, each to the result of the previous one; the file is written only if all of them apply"`
}

type globIn struct {
	Pattern        string `json:"pattern" jsonschema:"glob relative to path, e.g. **/*.go or src/**/*.{ts,tsx}"`
	Path           string `json:"path,omitempty" jsonschema:"directory to search in; defaults to the first root"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"also list files ignored by .gitignore (build output, dependencies)"`
}

type grepIn struct {
	Pattern        string `json:"pattern" jsonschema:"regular expression (Go RE2 syntax)"`
	Path           string `json:"path,omitempty" jsonschema:"file or directory to search; defaults to the first root"`
	Glob           string `json:"glob,omitempty" jsonschema:"only search files matching this glob, e.g. *.go or **/*.{ts,vue}"`
	IgnoreCase     bool   `json:"ignore_case,omitempty" jsonschema:"case-insensitive match"`
	Context        int    `json:"context,omitempty" jsonschema:"lines of context around each match (0-10)"`
	MaxResults     int    `json:"max_results,omitempty" jsonschema:"stop after this many matches (default 200)"`
	FilesOnly      bool   `json:"files_only,omitempty" jsonschema:"list matching file paths only"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"also search files ignored by .gitignore (build output such as dist/)"`
}

type runCommandIn struct {
	Command    string            `json:"command" jsonschema:"command or script to run"`
	Cwd        string            `json:"cwd,omitempty" jsonschema:"working directory inside the roots; defaults to the first root"`
	Shell      string            `json:"shell,omitempty" jsonschema:"powershell, pwsh or cmd; defaults to the configured shell"`
	TimeoutSec int               `json:"timeout_sec,omitempty" jsonschema:"kill the command after this many seconds"`
	Env        map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
}

type startProcessIn struct {
	Command string            `json:"command" jsonschema:"long-running command, e.g. pnpm dev"`
	Cwd     string            `json:"cwd,omitempty" jsonschema:"working directory inside the roots"`
	Shell   string            `json:"shell,omitempty" jsonschema:"powershell, pwsh or cmd"`
	Name    string            `json:"name,omitempty" jsonschema:"short label to recognise the process"`
	Env     map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
}

type readProcessIn struct {
	ID          string `json:"id" jsonschema:"process id returned by start_process (or by a run_command that moved to the background)"`
	SinceOffset int64  `json:"since_offset,omitempty" jsonschema:"next_offset from the previous read; 0 reads from the start"`
	WaitMs      int    `json:"wait_ms,omitempty" jsonschema:"wait up to this long for new output (capped at the server's max wait, 45000 by default)"`
	UntilExit   bool   `json:"until_exit,omitempty" jsonschema:"wait for the process to exit instead of returning at the first new output"`
	WaitFor     string `json:"wait_for,omitempty" jsonschema:"wait until the new output matches this regexp (Go RE2), e.g. 'listening on|ready in'"`
	Tail        bool   `json:"tail,omitempty" jsonschema:"when the new output is long, return its end instead of its start"`
}

type procIDIn struct {
	ID string `json:"id" jsonschema:"process id returned by start_process"`
}

type listProcessesIn struct {
	JSON bool `json:"json,omitempty" jsonschema:"return structured JSON array of processes"`
}

type systemInfoIn struct{}

type historyIn struct {
	ID          int64  `json:"id,omitempty" jsonschema:"show the arguments and output of this one call"`
	Limit       int    `json:"limit,omitempty" jsonschema:"how many calls to list (default 30, max 200)"`
	Offset      int    `json:"offset,omitempty" jsonschema:"skip this many matching calls (for paging)"`
	Tool        string `json:"tool,omitempty" jsonschema:"only calls of this tool, e.g. run_command or edit_file"`
	Query       string `json:"query,omitempty" jsonschema:"only calls whose arguments, output or error contain this text (case-insensitive), e.g. a file name or a command"`
	FailedOnly  bool   `json:"failed_only,omitempty" jsonschema:"only failed, timed-out or rejected calls"`
	ChangesOnly bool   `json:"changes_only,omitempty" jsonschema:"leave out read-only calls (read_file, grep, list_dir, ...)"`
	Since       string `json:"since,omitempty" jsonschema:"only calls after this: a duration back from now (30m, 2h, 3d) or a time (2026-10-11, 2026-10-11 15:04)"`
}

func (s *Server) registerTools() {
	addTool(s, "list_roots", "Show the host OS, the allowed root directories, path aliases, the default shell, a scratch directory for temporary files and which developer tools are on PATH. Call this first.",
		func(ctx context.Context, _ listRootsIn) (string, error) {
			host, _ := os.Hostname()
			var b strings.Builder
			if s.cfg.Unrestricted {
				b.WriteString("access_mode: unrestricted (all paths accessible to the OS user; no host-side approval)\n")
			} else {
				b.WriteString("access_mode: restricted (configured roots only)\n")
			}
			fmt.Fprintf(&b, "host: %s (%s/%s)\ndefault_shell: %s\ncommand_timeout_sec: %d (max %d)\n",
				host, runtime.GOOS, runtime.GOARCH, s.cfg.DefaultShell, s.cfg.CommandTimeoutSec, s.cfg.MaxCommandTimeoutSec)
			if s.runner.DetachAfter > 0 {
				fmt.Fprintf(&b, "max_call_wait_sec: %d (run_command moves a longer command to the background)\n", int(s.runner.DetachAfter.Seconds()))
			}
			b.WriteString("roots:\n")
			for _, r := range s.guard.Roots() {
				fmt.Fprintf(&b, "  %s\n", r)
			}
			if len(s.cfg.PathAliases) > 0 {
				b.WriteString("path_aliases:\n")
				for from, to := range s.cfg.PathAliases {
					fmt.Fprintf(&b, "  %s -> %s\n", from, to)
				}
			}
			if s.scratch != "" {
				fmt.Fprintf(&b, "scratch_dir: %s (put temporary files such as commit messages or patches here)\n", s.scratch)
			}
			if s.history != nil {
				fmt.Fprintf(&b, "history: %s (operation_history, kept %d days)\n", s.history.Dir(), s.history.KeepDays())
			}
			fmt.Fprintf(&b, "tools: %s (versions: system_info)\n", sysinfo.Summary(sysinfo.FindTools()))
			return b.String(), nil
		})

	addTool(s, "list_dir", "List a directory (optionally a few levels deep) with sizes and modification times. .git and node_modules are shown but not expanded.",
		func(ctx context.Context, in listDirIn) (string, error) {
			return s.fs.ListDir(in.Path, in.Depth, in.ShowHidden)
		})

	addTool(s, "read_file", "Read a text file. Output lines are prefixed with their line number and a tab; the prefix is not part of the file. Use offset/limit for large files.",
		func(ctx context.Context, in readFileIn) (string, error) {
			return s.fs.ReadFile(in.Path, in.Offset, in.Limit)
		})

	addTool(s, "write_file", "Create a file or replace its whole content (written atomically). Prefer edit_file for changes to existing files.",
		func(ctx context.Context, in writeFileIn) (string, error) {
			return s.fs.WriteFile(in.Path, in.Content, in.CreateDirs)
		})

	addTool(s, "edit_file", "Replace an exact string in a file. old_string must match exactly (whitespace included) and be unique unless replace_all is true. CRLF line endings and a UTF-8 BOM are preserved; you can send LF text.",
		func(ctx context.Context, in editFileIn) (string, error) {
			return s.fs.EditFile(in.Path, in.OldString, in.NewString, in.ReplaceAll)
		})

	addTool(s, "multi_edit", "Make several exact-string replacements in one file in one call. Edits apply in order, each to the result of the previous one, with the same rules as edit_file; if any edit does not apply, nothing is written and the error names the edit.",
		func(ctx context.Context, in multiEditIn) (string, error) {
			return s.fs.MultiEdit(in.Path, in.Edits)
		})

	addTool(s, "delete_path", "Delete a file, an empty directory, or (with recursive=true) a directory tree. In restricted mode configured root directories cannot be deleted; unrestricted mode allows any path the OS user can remove.",
		func(ctx context.Context, in deletePathIn) (string, error) {
			return s.fs.DeletePath(in.Path, in.Recursive)
		})

	addTool(s, "move_path", "Move or rename a file or directory. Missing parent directories of dst are created. In restricted mode configured root directories cannot be moved; unrestricted mode allows any path the OS user can move.",
		func(ctx context.Context, in movePathIn) (string, error) {
			return s.fs.MovePath(in.Src, in.Dst, in.Overwrite)
		})

	addTool(s, "glob", "Find files by glob pattern (** and {a,b} supported), newest first. Skips .git, node_modules and files ignored by .gitignore unless the pattern names them or include_ignored is set.",
		func(ctx context.Context, in globIn) (string, error) {
			return s.fs.Glob(in.Pattern, in.Path, in.IncludeIgnored)
		})

	addTool(s, "grep", "Search file contents with a regular expression. Output is path:line:text (context lines use '-'). Skips binary files, .git, node_modules and files ignored by .gitignore (set include_ignored to search them).",
		func(ctx context.Context, in grepIn) (string, error) {
			return s.fs.Grep(fstools.GrepOptions{
				Pattern: in.Pattern, Path: in.Path, Glob: in.Glob, IgnoreCase: in.IgnoreCase,
				Context: in.Context, MaxResults: in.MaxResults, FilesOnly: in.FilesOnly,
				IncludeIgnored: in.IncludeIgnored,
			})
		})

	addTool(s, "run_command", "Run a shell command on the host and wait for it. Returns exit code, stdout and stderr. The process tree is killed on timeout. A command still running after the server's max wait (45s by default) keeps running in the background and the result says how to read the rest (read_process_output until_exit=true). Stdin is empty, so interactive prompts fail. Use start_process for servers and watchers.",
		func(ctx context.Context, in runCommandIn) (string, error) {
			res, err := s.runner.Run(ctx, exectools.RunRequest{
				Command: in.Command, Cwd: in.Cwd, Shell: in.Shell, TimeoutSec: in.TimeoutSec, Env: in.Env,
			})
			if err != nil {
				return "", err
			}
			return res.String(), nil
		})

	addTool(s, "start_process", "Start a long-running command (dev server, watcher, start-all script) in the background and return its id immediately. Read its output with read_process_output.",
		func(ctx context.Context, in startProcessIn) (string, error) {
			p, err := s.procs.Start(in.Command, in.Cwd, in.Shell, in.Name, in.Env)
			if err != nil {
				return "", err
			}
			out := fmt.Sprintf("started %s (pid %d) in %s\n", p.ID, p.PID, p.Cwd)
			if p.LogFile != "" {
				out += "log: " + p.LogFile + "\n"
			}
			return out + fmt.Sprintf("next: read_process_output id=%s wait_ms=3000 (a server: wait_for=<ready message>; a build or test: until_exit=true)", p.ID), nil
		})

	addTool(s, "read_process_output", "Read new output of a background process since an offset, plus whether it is still running. Pass the returned next_offset to the following call. Waits with wait_ms for new output, until_exit for the process to end, or wait_for until the output matches a regexp; one call waits at most the server's max wait (45s by default), then call again.",
		func(ctx context.Context, in readProcessIn) (string, error) {
			return s.procs.Read(in.ID, exectools.ReadOptions{
				Since: in.SinceOffset, WaitMs: in.WaitMs, UntilExit: in.UntilExit, WaitFor: in.WaitFor, Tail: in.Tail,
			})
		})

	addTool(s, "list_processes", "List background processes started with start_process.",
		func(ctx context.Context, in listProcessesIn) (string, error) {
			if in.JSON {
				data, err := json.MarshalIndent(s.procs.ListInfo(), "", "  ")
				if err != nil {
					return "", err
				}
				return string(data), nil
			}
			return s.procs.List(), nil
		})

	addTool(s, "stop_process", "Kill a background process and all its children, and forget it.",
		func(ctx context.Context, in procIDIn) (string, error) {
			return s.procs.Stop(in.ID)
		})

	addTool(s, "write_process_input", "Write text to the standard input (stdin) of a running background process.",
		func(ctx context.Context, in writeProcessIn) (string, error) {
			input := in.Input
			if !in.Raw && !strings.HasSuffix(input, "\n") {
				input += "\n"
			}
			if err := s.procs.WriteInput(in.ID, input, in.CloseStdin); err != nil {
				return "", err
			}
			msg := fmt.Sprintf("wrote %d bytes to %s stdin", len(input), in.ID)
			if in.CloseStdin {
				msg += " and closed stdin"
			}
			return msg, nil
		})

	addTool(s, "notify_user", "Display a system notification popup on the host screen (e.g. when long task/build finishes).",
		func(ctx context.Context, in notifyUserIn) (string, error) {
			return notifyUser(in.Title, in.Message, in.Sound)
		})

	addTool(s, "get_clipboard", "Get the current text content from the host system clipboard.",
		func(ctx context.Context, _ struct{}) (string, error) {
			return getClipboard()
		})

	addTool(s, "set_clipboard", "Set the host system clipboard text.",
		func(ctx context.Context, in setClipboardIn) (string, error) {
			return setClipboard(in.Text)
		})

	addCallTool(s, "read_image", "Read an image on the host and return it as standard MCP ImageContent along with file metadata TextContent. Supports jpg/jpeg/png/webp/gif/bmp/tiff/heic, auto-orients EXIF, optional crop (original pixel coords), rotate, max_dimension scaling, and <=1MB compression.",
		func(ctx context.Context, in readImageIn) (*mcp.CallResult, string, error) {
			return s.readImage(ctx, in)
		})

	addCallTool(s, "read_images", "Batch read multiple images (up to 10) on the host. Returns alternating TextContent (with file info) and ImageContent for each image, with total payload capped at 4MB.",
		func(ctx context.Context, in readImagesIn) (*mcp.CallResult, string, error) {
			return s.readImages(ctx, in)
		})

	addCallTool(s, "capture_screen", "Capture a screenshot on macOS (quietly without sound). Saves to scratch dir if path is empty. Set return_image=true to receive the screenshot directly as an ImageContent for AI visual inspection.",
		func(ctx context.Context, in captureScreenIn) (*mcp.CallResult, string, error) {
			return s.captureScreen(ctx, in)
		})

	addTool(s, "open_target", "Open a URL in the host browser, or open a file/directory in Finder/Explorer/default application.",
		func(ctx context.Context, in openTargetIn) (string, error) {
			return s.openTarget(in)
		})

	addTool(s, "inspect_port", "Check which process is listening on a TCP port on the host, with optional kill to terminate it.",
		func(ctx context.Context, in inspectPortIn) (string, error) {
			return inspectPort(in.Port, in.Kill)
		})

	addTool(s, "http_request", "Send an HTTP request from the host machine (e.g. to test local dev servers or APIs running on localhost).",
		func(ctx context.Context, in httpRequestIn) (string, error) {
			return s.httpRequest(ctx, in)
		})

	addTool(s, "web_search", "Search the web from the host: titles, URLs and snippets, from the Bocha search API when its key is set, else the configured SearXNG, else Bing (all work from mainland China). Narrow with site (a domain), time_range (day, week, month, year) and language; then read a result with web_fetch.",
		func(ctx context.Context, in webSearchIn) (string, error) {
			return s.webSearch(ctx, in)
		})

	addTool(s, "web_fetch", "Read a web page as Markdown: only its main content (navigation, scripts and page frame removed), with headings, lists, tables, code and absolute links kept; GBK and other charsets are converted. Long pages come in parts: continue with offset from the next: line. find=<keywords> returns only the sections that mention them, under their headings: the cheapest way to answer a question from a long page. links=true lists the page's links (e.g. a documentation sidebar). Pages are kept 15 minutes, so reading in parts fetches once; refresh=true fetches again.",
		func(ctx context.Context, in webFetchIn) (string, error) {
			return s.webFetch(ctx, in)
		})

	addTool(s, "system_info", "Describe the host: OS version, user, CPUs, memory, disks, uptime, code page and time zone, and the developer tools on PATH with their versions (git, go, node, pnpm, python, java, docker, ...).",
		func(ctx context.Context, _ systemInfoIn) (string, error) {
			return s.systemInfo(ctx), nil
		})

	addTool(s, "get_env", "Show environment variables as commands see them: all of them, those matching a filter, or one by name. PATH-like variables are listed one entry per line with missing directories marked. On Windows scope=user or machine shows the values stored in the registry. Values that look like secrets are hidden unless show_secrets is set.",
		func(ctx context.Context, in getEnvIn) (string, error) {
			return s.getEnv(in)
		})

	if s.history != nil {
		addTool(s, "operation_history", "List earlier tool calls on this host, newest first, from every session (kept on disk across restarts): time, tool, result, duration and the main argument. Filter by tool, query text, failed_only, changes_only or since; pass id to see one call's arguments and the start of its output.",
			func(ctx context.Context, in historyIn) (string, error) {
				return s.operationHistory(in)
			})
	}
}

// operationHistory is the operation_history tool.
func (s *Server) operationHistory(in historyIn) (string, error) {
	if in.ID > 0 {
		e, err := s.history.Get(in.ID)
		if err != nil {
			return "", err
		}
		return formatHistoryEntry(e), nil
	}
	since, err := history.ParseSince(in.Since, time.Now())
	if err != nil {
		return "", err
	}
	if in.Limit > 200 {
		in.Limit = 200
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	q := history.Query{
		Limit: in.Limit, Offset: in.Offset, Tool: in.Tool, Text: in.Query,
		FailedOnly: in.FailedOnly, ChangesOnly: in.ChangesOnly, ReadOnly: readOnlyTools,
		Exclude: map[string]bool{"operation_history": true}, Since: since,
	}
	entries, total, err := s.history.Find(q)
	if err != nil {
		return "", err
	}
	return formatHistoryList(s.history, entries, total, in.Offset), nil
}
