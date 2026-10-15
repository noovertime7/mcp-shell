package server

import (
	"strings"
	"testing"

	"github.com/noovertime7/mcp-shell/internal/webtools"
)

const page = "# 传感器\n\n简介段落。\n\n## 加速度\n\n监听加速度数据。\n\n### 支持明细\n\n| 接口 | 设备 |\n| --- | --- |\n| subscribeAccelerometer | Xiaomi Watch S5 |\n\n## 罗盘\n\n监听罗盘数据，S4 支持。"

func TestFindReturnsMatchingSectionsUnderTheirHeadings(t *testing.T) {
	out := findSections(page, "S4, 加速度", 12000, 50000)
	for _, want := range []string{
		"--- 3 sections mention s4 / 加速度 ---",
		"## 加速度",
		"§ 传感器 > 加速度\n监听加速度数据。",
		"§ 传感器 > 罗盘\n监听罗盘数据，S4 支持。",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("find output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "简介") {
		t.Errorf("unrelated section shown:\n%s", out)
	}
	if out := findSections(page, "心率", 12000, 50000); !strings.Contains(out, "no section mentions 心率") {
		t.Errorf("no match: %s", out)
	}
}

func TestWindowStopsAtCharactersAndBytes(t *testing.T) {
	text := strings.Repeat("字", 100)
	part, end := window(text, 10, 20, 1000)
	if part != strings.Repeat("字", 20) || end != 30 {
		t.Fatalf("window %q %d", part, end)
	}
	// Each 字 is 3 bytes: a 30-byte budget holds 10 of them.
	if part, end := window(text, 0, 50, 30); end != 10 || part != strings.Repeat("字", 10) {
		t.Fatalf("budget window %q %d", part, end)
	}
}

func TestLinksMatchingFindComeAlone(t *testing.T) {
	links := []webtools.Link{{Text: "传感器", URL: "https://x/sensor"}, {Text: "振动", URL: "https://x/vibrator"}}
	out := formatLinks(links, "sensor")
	if !strings.Contains(out, "--- links: 2, 1 mention the find keywords ---") || strings.Contains(out, "vibrator") {
		t.Fatalf("links %s", out)
	}
	if out := formatLinks(links, ""); !strings.Contains(out, "- 振动: https://x/vibrator") {
		t.Fatalf("all links %s", out)
	}
}
