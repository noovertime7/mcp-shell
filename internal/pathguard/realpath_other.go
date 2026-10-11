//go:build !windows

package pathguard

import "path/filepath"

func realPath(p string) (string, error) {
	return filepath.EvalSymlinks(p)
}
