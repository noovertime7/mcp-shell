package hostenv

// loginScope is empty on Windows: the registry scopes fill the same gap.
func loginScope() map[string]string { return nil }

// withUTF8Locale leaves the environment alone on Windows, which has no
// locale variables.
func withUTF8Locale(env []string, _ *Diff) []string { return env }

// addedFrom names where Environ's additions come from.
const addedFrom = "the registry"
