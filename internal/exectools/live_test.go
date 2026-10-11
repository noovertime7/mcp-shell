package exectools

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestLiveOutputBeforeFlushAndSplitUTF8(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	w := &liveWriter{label: "测试命令"}
	data := []byte("实时中文")
	_, _ = w.Write(data[:4]) // One complete character and one byte of the next.
	if !strings.Contains(out.String(), "实时输出") || !strings.Contains(out.String(), "实") {
		t.Fatal("output was buffered instead of streaming")
	}
	_, _ = w.Write(data[4:])
	w.flush()
	if strings.Contains(out.String(), "�") || len(w.pending) != 0 {
		t.Fatalf("broken UTF-8: %s", out.String())
	}
	if !strings.Contains(out.String(), "时中文") {
		t.Fatalf("missing split character: %s", out.String())
	}
}
