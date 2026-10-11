//go:build windows

package sysinfo

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/noovertime7/mcp-shell/internal/hostenv"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetLogicalDrives     = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW        = kernel32.NewProc("GetDriveTypeW")
	procGetACP               = kernel32.NewProc("GetACP")
	procGetOEMCP             = kernel32.NewProc("GetOEMCP")
)

const currentVersionKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

func osName(context.Context) string {
	product, ok := hostenv.MachineString(currentVersionKey, "ProductName")
	if !ok {
		return "Windows"
	}
	build, _ := hostenv.MachineString(currentVersionKey, "CurrentBuild")
	// Windows 11 still reports "Windows 10" as its product name.
	var n int
	if _, err := fmt.Sscanf(build, "%d", &n); err == nil && n >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	s := product
	if v, ok := hostenv.MachineString(currentVersionKey, "DisplayVersion"); ok {
		s += " " + v
	}
	if build != "" {
		s += " (build " + build
		if ubr, ok := hostenv.MachineDWORD(currentVersionKey, "UBR"); ok {
			s += fmt.Sprintf(".%d", ubr)
		}
		s += ")"
	}
	return s
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memory(context.Context) (total, free uint64) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, 0
	}
	return m.TotalPhys, m.AvailPhys
}

func uptime(context.Context) time.Duration {
	r, _, _ := procGetTickCount64.Call()
	return time.Duration(r) * time.Millisecond
}

func encoding() string {
	acp, _, _ := procGetACP.Call()
	oem, _, _ := procGetOEMCP.Call()
	return fmt.Sprintf("ANSI code page %d, OEM code page %d (mcp-shell runs PowerShell with UTF-8 output)", acp, oem)
}

func diskSpace(path string) (total, free uint64, ok bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var avail, tot, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&tot)), uintptr(unsafe.Pointer(&totalFree)))
	return tot, avail, r != 0
}

func disks(paths []string) []Disk {
	const driveFixed = 3
	seen := map[string]bool{}
	var out []Disk
	add := func(vol string) {
		vol = strings.ToUpper(vol)
		if seen[vol] {
			return
		}
		seen[vol] = true
		if total, free, ok := diskSpace(vol); ok {
			out = append(out, Disk{Path: vol, Total: total, Free: free})
		}
	}
	mask, _, _ := procGetLogicalDrives.Call()
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		vol := string(rune('A'+i)) + `:\`
		p, _ := syscall.UTF16PtrFromString(vol)
		if t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(p))); t == driveFixed {
			add(vol)
		}
	}
	for _, p := range paths {
		if v := filepath.VolumeName(p); v != "" {
			add(v + `\`)
		}
	}
	return out
}
