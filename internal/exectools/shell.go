package exectools

import (
	"os"
)

// psPrologue makes PowerShell write UTF-8 without a BOM and hides progress
// bars, which would otherwise show up as noise on stderr.
const psPrologue = `$ProgressPreference = 'SilentlyContinue'
try {
  $__mcpshell_enc = New-Object System.Text.UTF8Encoding $false
  [Console]::OutputEncoding = $__mcpshell_enc
  $OutputEncoding = $__mcpshell_enc
} catch {}
`

// psEpilogue propagates native exit codes and PowerShell failures as the
// process exit code.
const psEpilogue = `
$__mcpshell_ok = $?
if ($global:LASTEXITCODE) { exit $global:LASTEXITCODE }
if (-not $__mcpshell_ok) { exit 1 }
exit 0
`

func wrapPowerShell(command string) string {
	return psPrologue + command + "\n" + psEpilogue
}

// powershellArgs writes the command to a temporary .ps1 file and returns the
// arguments that run it with -File, plus a cleanup that deletes the file.
//
// A script file is used instead of -EncodedCommand because Windows
// PowerShell 5 serialises the error stream as "#< CLIXML" XML when started
// with -EncodedCommand, and because it has no command-line length limit.
// The file gets a UTF-8 BOM, which PowerShell 5 needs to read non-ASCII text.
func powershellArgs(command string) ([]string, func(), error) {
	f, err := os.CreateTemp("", "mcp-shell-*.ps1")
	if err != nil {
		return nil, nil, err
	}
	_, werr := f.WriteString(utf8BOM + wrapPowerShell(command))
	cerr := f.Close()
	cleanup := func() { os.Remove(f.Name()) }
	if werr != nil || cerr != nil {
		cleanup()
		if werr != nil {
			return nil, nil, werr
		}
		return nil, nil, cerr
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", f.Name()}, cleanup, nil
}

func noCleanup() {}

// utf8BOM is spelled as bytes so no tool can turn it into a raw BOM in this file.
var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})
