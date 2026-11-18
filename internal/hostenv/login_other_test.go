//go:build !windows

package hostenv

import (
	"strings"
	"testing"
)

func TestParseLoginEnvSkipsRcOutputAndSessionVariables(t *testing.T) {
	out := []byte("motd from .zshrc\n" + loginEnvMarker + "PATH=/opt/homebrew/bin:/usr/bin\x00GOROOT=/x/go\x00PWD=/tmp\x00TERM=dumb\x00MULTI=a\nb\x00")
	vars := parseLoginEnv(out)
	if vars["PATH"] != "/opt/homebrew/bin:/usr/bin" || vars["GOROOT"] != "/x/go" || vars["MULTI"] != "a\nb" {
		t.Fatalf("vars = %v", vars)
	}
	if _, ok := vars["PWD"]; ok {
		t.Fatal("session variables must be skipped")
	}
	if _, ok := vars["TERM"]; ok {
		t.Fatal("TERM must be skipped")
	}
	if parseLoginEnv([]byte("no marker")) != nil {
		t.Fatal("output without the marker must give nothing")
	}
}

func TestWithUTF8Locale(t *testing.T) {
	var diff Diff
	env := withUTF8Locale([]string{"PATH=/bin"}, &diff)
	if v, _ := Get(env, "LANG"); !strings.HasSuffix(v, "UTF-8") || strings.Join(diff.VarsAdded, ",") != "LANG" {
		t.Fatalf("env=%v diff=%+v", env, diff)
	}
	diff = Diff{}
	env = withUTF8Locale([]string{"LC_ALL=zh_CN.UTF-8"}, &diff)
	if _, ok := Get(env, "LANG"); ok || len(diff.VarsAdded) != 0 {
		t.Fatalf("a set locale must be kept: env=%v", env)
	}
}

func TestLoginScopeCanBeTurnedOff(t *testing.T) {
	t.Setenv("MCP_SHELL_LOGIN_ENV", "0")
	if loginScope() != nil {
		t.Fatal("MCP_SHELL_LOGIN_ENV=0 must turn the login shell off")
	}
}

func TestLoginScopeOnThisHost(t *testing.T) {
	vars := loginScope()
	if testing.Short() || len(vars) == 0 {
		t.Skip("no login shell environment on this host")
	}
	if !strings.Contains(vars["PATH"], "/") {
		t.Fatalf("login PATH looks wrong: %q", vars["PATH"])
	}
}
