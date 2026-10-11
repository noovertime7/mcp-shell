//go:build !windows

package sysinfo

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func hideWindow(*exec.Cmd) {}

func run(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func osName(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		name := run(ctx, "sw_vers", "-productName")
		ver := run(ctx, "sw_vers", "-productVersion")
		if name != "" {
			return strings.TrimSpace(name + " " + ver)
		}
	}
	if f, err := os.Open("/etc/os-release"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
				return strings.Trim(v, `"`) + " " + run(ctx, "uname", "-r")
			}
		}
	}
	if s := run(ctx, "uname", "-sr"); s != "" {
		return s
	}
	return runtime.GOOS
}

func memory(ctx context.Context) (total, free uint64) {
	if runtime.GOOS == "darwin" {
		total, _ = strconv.ParseUint(run(ctx, "sysctl", "-n", "hw.memsize"), 10, 64)
		return total, 0
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			free = kb * 1024
		}
	}
	return total, free
}

func uptime(ctx context.Context) time.Duration {
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			if s, err := strconv.ParseFloat(f[0], 64); err == nil {
				return time.Duration(s * float64(time.Second))
			}
		}
	}
	if runtime.GOOS == "darwin" {
		// "{ sec = 1700000000, usec = 0 } Tue Nov 14 ..."
		s := run(ctx, "sysctl", "-n", "kern.boottime")
		if i := strings.Index(s, "sec = "); i >= 0 {
			rest := s[i+len("sec = "):]
			if j := strings.IndexAny(rest, ", }"); j > 0 {
				if sec, err := strconv.ParseInt(rest[:j], 10, 64); err == nil {
					return time.Since(time.Unix(sec, 0))
				}
			}
		}
	}
	return 0
}

func encoding() string {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return k + "=" + v
		}
	}
	return ""
}

func disks(paths []string) []Disk {
	seen := map[string]bool{}
	var out []Disk
	for _, p := range append([]string{"/"}, paths...) {
		var st syscall.Statfs_t
		if syscall.Statfs(p, &st) != nil {
			continue
		}
		total := uint64(st.Blocks) * uint64(st.Bsize)
		free := uint64(st.Bavail) * uint64(st.Bsize)
		key := strconv.FormatUint(total, 10) + "/" + strconv.FormatUint(uint64(st.Blocks), 10)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Disk{Path: p, Total: total, Free: free})
	}
	return out
}
