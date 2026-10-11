//go:build windows

package hostenv

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

const (
	machineEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvKey    = `Environment`

	errorNoMoreItems = 259
	regSZ            = 1
	regExpandSZ      = 2
	regDWORD         = 4
)

var (
	advapi32                      = syscall.NewLazyDLL("advapi32.dll")
	procRegEnumValueW             = advapi32.NewProc("RegEnumValueW")
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procExpandEnvironmentStringsW = kernel32.NewProc("ExpandEnvironmentStringsW")
)

// Scope returns the variables stored for "user" or "machine" in the
// registry, with %VAR% references expanded.
func Scope(scope string) (map[string]string, error) {
	switch scope {
	case "machine":
		return readEnvKey(syscall.HKEY_LOCAL_MACHINE, machineEnvKey)
	case "user":
		return readEnvKey(syscall.HKEY_CURRENT_USER, userEnvKey)
	}
	return nil, fmt.Errorf("unknown scope %q (use process, user or machine)", scope)
}

func readEnvKey(root syscall.Handle, path string) (map[string]string, error) {
	h, err := openKey(root, path)
	if err != nil {
		return nil, err
	}
	defer syscall.RegCloseKey(h)
	out := map[string]string{}
	name := make([]uint16, 16384)
	for i := uint32(0); ; i++ {
		n := uint32(len(name))
		r, _, _ := procRegEnumValueW.Call(uintptr(h), uintptr(i), uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&n)), 0, 0, 0, 0)
		if r == errorNoMoreItems {
			break
		}
		if r != 0 {
			return out, syscall.Errno(r)
		}
		key := syscall.UTF16ToString(name[:n])
		if v, ok := queryString(h, key); ok {
			out[key] = v
		}
	}
	return out, nil
}

func openKey(root syscall.Handle, path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var h syscall.Handle
	if err := syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ, &h); err != nil {
		return 0, err
	}
	return h, nil
}

// queryValue returns the raw data and type of a value.
func queryValue(h syscall.Handle, name string) ([]byte, uint32, error) {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, 0, err
	}
	var typ, n uint32
	if err := syscall.RegQueryValueEx(h, p, nil, &typ, nil, &n); err != nil {
		return nil, 0, err
	}
	if n == 0 {
		return nil, typ, nil
	}
	buf := make([]byte, n)
	if err := syscall.RegQueryValueEx(h, p, nil, &typ, &buf[0], &n); err != nil {
		return nil, 0, err
	}
	return buf[:n], typ, nil
}

// queryString reads a REG_SZ or REG_EXPAND_SZ value; the latter is expanded.
func queryString(h syscall.Handle, name string) (string, bool) {
	data, typ, err := queryValue(h, name)
	if err != nil || (typ != regSZ && typ != regExpandSZ) {
		return "", false
	}
	u := make([]uint16, len(data)/2)
	for i := range u {
		u[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	s := string(utf16.Decode(u))
	if typ == regExpandSZ {
		s = expand(s)
	}
	return s, true
}

func expand(s string) string {
	src, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return s
	}
	buf := make([]uint16, 1024)
	for {
		r, _, _ := procExpandEnvironmentStringsW.Call(uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		n := int(r)
		if n == 0 {
			return s
		}
		if n <= len(buf) {
			return syscall.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n)
	}
}

// MachineString reads a REG_SZ value under HKLM, e.g. the Windows product
// name; ok is false when it is missing.
func MachineString(path, name string) (string, bool) {
	h, err := openKey(syscall.HKEY_LOCAL_MACHINE, path)
	if err != nil {
		return "", false
	}
	defer syscall.RegCloseKey(h)
	return queryString(h, name)
}

// MachineDWORD reads a REG_DWORD value under HKLM.
func MachineDWORD(path, name string) (uint32, bool) {
	h, err := openKey(syscall.HKEY_LOCAL_MACHINE, path)
	if err != nil {
		return 0, false
	}
	defer syscall.RegCloseKey(h)
	data, typ, err := queryValue(h, name)
	if err != nil || typ != regDWORD || len(data) < 4 {
		return 0, false
	}
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24, true
}
