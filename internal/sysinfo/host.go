package sysinfo

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"
)

// Host is what system_info reports about the machine.
type Host struct {
	Hostname   string
	OS         string
	Arch       string
	User       string
	CPUs       int
	MemTotal   uint64 // bytes, 0 when unknown
	MemFree    uint64
	Uptime     time.Duration // 0 when unknown
	Encoding   string        // code pages on Windows, locale elsewhere
	TimeZone   string
	Disks      []Disk
	ServerPID  int
	ServerExe  string
	ServerFrom time.Time
}

// Disk is the space on one volume.
type Disk struct {
	Path  string
	Total uint64
	Free  uint64
}

var started = time.Now()

// Describe gathers Host. paths are directories whose volumes to report
// (the roots); fixed drives are added on Windows.
func Describe(ctx context.Context, paths []string) Host {
	h := Host{
		OS:         osName(ctx),
		Arch:       runtime.GOARCH,
		CPUs:       runtime.NumCPU(),
		ServerPID:  os.Getpid(),
		ServerFrom: started,
	}
	h.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		h.User = u.Username
	}
	h.ServerExe, _ = os.Executable()
	h.MemTotal, h.MemFree = memory(ctx)
	h.Uptime = uptime(ctx)
	h.Encoding = encoding()
	name, offset := time.Now().Zone()
	h.TimeZone = fmt.Sprintf("%s (UTC%+03d:%02d)", name, offset/3600, abs(offset%3600)/60)
	h.Disks = disks(paths)
	return h
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// String renders Host and tools for the model.
func (h Host) String(tools []Tool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "host: %s\nos: %s (%s)\nuser: %s\ncpus: %d logical\n", h.Hostname, h.OS, h.Arch, h.User, h.CPUs)
	if h.MemTotal > 0 {
		fmt.Fprintf(&b, "memory: %s free of %s\n", Bytes(h.MemFree), Bytes(h.MemTotal))
	}
	if h.Uptime > 0 {
		fmt.Fprintf(&b, "uptime: %s\n", roundUptime(h.Uptime))
	}
	if h.Encoding != "" {
		fmt.Fprintf(&b, "encoding: %s\n", h.Encoding)
	}
	fmt.Fprintf(&b, "time: %s, zone %s\n", time.Now().Format("2006-01-02 15:04:05"), h.TimeZone)
	if len(h.Disks) > 0 {
		b.WriteString("disks:\n")
		for _, d := range h.Disks {
			fmt.Fprintf(&b, "  %s  %s free of %s\n", d.Path, Bytes(d.Free), Bytes(d.Total))
		}
	}
	fmt.Fprintf(&b, "mcp-shell: pid %d, running since %s, %s\n", h.ServerPID, h.ServerFrom.Format("2006-01-02 15:04:05"), h.ServerExe)
	if len(tools) > 0 {
		b.WriteString("tools on PATH:\n")
		for _, t := range tools {
			switch {
			case t.Path == "":
				fmt.Fprintf(&b, "  %-10s not found\n", t.Name)
			case t.Version != "":
				fmt.Fprintf(&b, "  %-10s %s  (%s)\n", t.Name, t.Version, t.Path)
			default:
				fmt.Fprintf(&b, "  %-10s %s\n", t.Name, t.Path)
			}
		}
	}
	return b.String()
}

func roundUptime(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, int(d/time.Hour))
	}
	return d.Round(time.Minute).String()
}

// Bytes formats a byte count for people.
func Bytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
