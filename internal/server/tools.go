package server

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/noovertime7/mcp-shell/internal/exectools"
	"github.com/noovertime7/mcp-shell/internal/fstools"
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

type globIn struct {
	Pattern string `json:"pattern" jsonschema:"glob relative to path, e.g. **/*.go or src/**/*.{ts,tsx}"`
	Path    string `json:"path,omitempty" jsonschema:"directory to search in; defaults to the first root"`
}

type grepIn struct {
	Pattern    string `json:"pattern" jsonschema:"regular expression (Go RE2 syntax)"`
	Path       string `json:"path,omitempty" jsonschema:"file or directory to search; defaults to the first root"`
	Glob       string `json:"glob,omitempty" jsonschema:"only search files matching this glob, e.g. *.go or **/*.{ts,vue}"`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema:"case-insensitive match"`
	Context    int    `json:"context,omitempty" jsonschema:"lines of context around each match (0-10)"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"stop after this many matches (default 200)"`
	FilesOnly  bool   `json:"files_only,omitempty" jsonschema:"list matching file paths only"`
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
	ID          string `json:"id" jsonschema:"process id returned by start_process"`
	SinceOffset int64  `json:"since_offset,omitempty" jsonschema:"next_offset from the previous read; 0 reads from the start"`
	WaitMs      int    `json:"wait_ms,omitempty" jsonschema:"wait up to this long (max 30000) for new output or exit"`
}

type procIDIn struct {
	ID string `json:"id" jsonschema:"process id returned by start_process"`
}

type listProcessesIn struct{}

func (s *Server) registerTools() {
	addTool(s, "list_roots", "Show the host OS, the allowed root directories, path aliases and the default shell. Call this first.",
		func(ctx context.Context, _ listRootsIn) (string, error) {
			host, _ := os.Hostname()
			var b strings.Builder
			if s.cfg.Unrestricted {
				b.WriteString("access_mode: unrestricted (all paths accessible to the OS user; no host-side approval)\n")
			} else {
				b.WriteString("access_mode: restricted (configured roots only)\n")
			}
			fmt.Fprintf(&b, "host: %s (%s/%s)\ndefault_shell: %s\ncommand_timeout_sec: %d (max %d)\nroots:\n",
				host, runtime.GOOS, runtime.GOARCH, s.cfg.DefaultShell, s.cfg.CommandTimeoutSec, s.cfg.MaxCommandTimeoutSec)
			for _, r := range s.guard.Roots() {
				fmt.Fprintf(&b, "  %s\n", r)
			}
			if len(s.cfg.PathAliases) > 0 {
				b.WriteString("path_aliases:\n")
				for from, to := range s.cfg.PathAliases {
					fmt.Fprintf(&b, "  %s -> %s\n", from, to)
				}
			}
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

	addTool(s, "delete_path", "Delete a file, an empty directory, or (with recursive=true) a directory tree. In restricted mode configured root directories cannot be deleted; unrestricted mode allows any path the OS user can remove.",
		func(ctx context.Context, in deletePathIn) (string, error) {
			return s.fs.DeletePath(in.Path, in.Recursive)
		})

	addTool(s, "move_path", "Move or rename a file or directory. Missing parent directories of dst are created. In restricted mode configured root directories cannot be moved; unrestricted mode allows any path the OS user can move.",
		func(ctx context.Context, in movePathIn) (string, error) {
			return s.fs.MovePath(in.Src, in.Dst, in.Overwrite)
		})

	addTool(s, "glob", "Find files by glob pattern (** and {a,b} supported), newest first. Skips .git and node_modules unless the pattern names them.",
		func(ctx context.Context, in globIn) (string, error) {
			return s.fs.Glob(in.Pattern, in.Path)
		})

	addTool(s, "grep", "Search file contents with a regular expression. Output is path:line:text (context lines use '-'). Skips binary files, .git and node_modules.",
		func(ctx context.Context, in grepIn) (string, error) {
			return s.fs.Grep(fstools.GrepOptions{
				Pattern: in.Pattern, Path: in.Path, Glob: in.Glob, IgnoreCase: in.IgnoreCase,
				Context: in.Context, MaxResults: in.MaxResults, FilesOnly: in.FilesOnly,
			})
		})

	addTool(s, "run_command", "Run a shell command on the host and wait for it. Returns exit code, stdout and stderr. The process tree is killed on timeout. Stdin is empty, so interactive prompts fail. Use start_process for servers and watchers.",
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
			return out + fmt.Sprintf("next: read_process_output id=%s wait_ms=3000", p.ID), nil
		})

	addTool(s, "read_process_output", "Read new output of a background process since an offset, plus whether it is still running. Pass the returned next_offset to the following call.",
		func(ctx context.Context, in readProcessIn) (string, error) {
			return s.procs.Read(in.ID, in.SinceOffset, in.WaitMs)
		})

	addTool(s, "list_processes", "List background processes started with start_process.",
		func(ctx context.Context, _ listProcessesIn) (string, error) {
			return s.procs.List(), nil
		})

	addTool(s, "stop_process", "Kill a background process and all its children, and forget it.",
		func(ctx context.Context, in procIDIn) (string, error) {
			return s.procs.Stop(in.ID)
		})
}
