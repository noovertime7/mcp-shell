//go:build !windows

package hostenv

import "errors"

var errNotWindows = errors.New("user and machine scopes are only available on Windows; use scope=process")

// Scope has no registry to read outside Windows.
func Scope(scope string) (map[string]string, error) {
	if scope == "machine" || scope == "user" {
		return nil, errNotWindows
	}
	return nil, errors.New("unknown scope " + scope + " (use process)")
}

// MachineString has no registry to read outside Windows.
func MachineString(path, name string) (string, bool) { return "", false }

// MachineDWORD has no registry to read outside Windows.
func MachineDWORD(path, name string) (uint32, bool) { return 0, false }
