package server

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestChineseLogReportsFailedCommand(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	s := &Server{}
	s.logCall("run_command", runCommandIn{Command: "exit 7"}, "exit_code: 7\n", nil, time.Second)
	if !strings.Contains(out.String(), "[操作完成] 失败") || !strings.Contains(out.String(), "执行命令") || !strings.Contains(out.String(), "命令=") {
		t.Fatal(out.String())
	}
}
