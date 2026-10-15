package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWebSettingsComeFromConfigThenEnvironmentThenWebJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.json")
	os.WriteFile(path, []byte("\xef\xbb\xbf"+`{"bocha_api_key":" sk-file ","search_url":"http://searx:8888","web_proxy":"socks5://127.0.0.1:7890"}`), 0o600)

	t.Setenv("BOCHA_API_KEY", "")
	c := &Config{}
	c.applyWeb([]string{filepath.Join(dir, "missing.json"), path})
	if c.BochaAPIKey != "sk-file" || c.SearchURL != "http://searx:8888" || c.WebProxy != "socks5://127.0.0.1:7890" {
		t.Fatalf("from web.json: %+v", c)
	}
	t.Setenv("BOCHA_API_KEY", "sk-env")
	c = &Config{SearchURL: "-"}
	c.applyWeb([]string{path})
	c.applyDefaults()
	if c.BochaAPIKey != "sk-env" || c.SearchURL != "" {
		t.Fatalf("environment and config win: %+v", c)
	}
	c = &Config{BochaAPIKey: "sk-config"}
	c.applyWeb([]string{path})
	if c.BochaAPIKey != "sk-config" {
		t.Fatalf("config wins: %+v", c)
	}
}
