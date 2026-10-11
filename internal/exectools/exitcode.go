package exectools

import (
	"errors"
	"math"
	"os/exec"
)

// normalizeExitCode turns a Windows exit status read as unsigned (exit -1
// shows up as 4294967295) back into the signed value programs meant.
func normalizeExitCode(code int) int {
	if c := int64(code); c > math.MaxInt32 && c <= math.MaxUint32 {
		return int(int32(uint32(c)))
	}
	return code
}

// exitCodeOf returns the exit code of a finished command, or -1 when it did
// not report one (it was killed or never ran).
func exitCodeOf(cmd *exec.Cmd, waitErr error) int {
	var exitErr *exec.ExitError
	switch {
	case errors.As(waitErr, &exitErr):
		return normalizeExitCode(exitErr.ExitCode())
	case cmd.ProcessState != nil:
		return normalizeExitCode(cmd.ProcessState.ExitCode())
	}
	return -1
}
