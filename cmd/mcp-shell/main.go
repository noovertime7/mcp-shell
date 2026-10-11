// Command mcp-shell is an MCP server for the host machine. A Claude Code
// session in a VM connects to it over Streamable HTTP to read and edit code
// and run builds and tests on the host.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/noovertime7/mcp-shell/internal/config"
	"github.com/noovertime7/mcp-shell/internal/server"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON config file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	parentStdin := flag.Bool("parent-stdin", false, "shut down when the desktop parent's stdin closes")
	flag.Parse()
	if *showVersion {
		log.SetFlags(0)
		log.Printf("mcp-shell %s", server.Version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatalf("open log_file: %v", err)
		}
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	log.SetFlags(log.Ldate | log.Ltime)
	log.Printf("[服务] 配置文件：%s", *configPath)
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()

	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return serveCtx },
	}

	log.Printf("[服务] mcp-shell %s 正在监听 http://%s/mcp", server.Version, cfg.Listen)
	log.Printf("[服务] 默认目录：%s", strings.Join(srv.Roots(), ", "))
	if cfg.Unrestricted {
		log.Printf("[服务] 访问范围：全部文件（当前系统用户权限）；全部工具直接执行")
	}
	if len(cfg.AllowedIPs) == 0 {
		log.Printf("[认证] 允许各来源使用正确的访问令牌连接")
	} else {
		log.Printf("[认证] 允许的来源：%s", strings.Join(cfg.AllowedIPs, ", "))
	}
	if strings.HasPrefix(cfg.Listen, "0.0.0.0:") || strings.HasPrefix(cfg.Listen, ":") {
		log.Printf("[服务] 局域网直连已开启；虚拟机请使用宿主机网卡 IP")
	}
	if cfg.AuditLog != "" {
		log.Printf("[日志] 审计记录：%s", cfg.AuditLog)
	}
	if cfg.LogFile != "" {
		log.Printf("[日志] 中文操作日志：%s", cfg.LogFile)
	}
	if cfg.OutputDir != "" {
		log.Printf("[日志] 完整命令输出：%s", cfg.OutputDir)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	parentClosed := make(chan struct{})
	if *parentStdin {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			close(parentClosed)
		}()
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			srv.Close()
			log.Fatal(err)
		}
	case <-stop:
		log.Printf("[服务] 正在关闭，清理后台进程")
	case <-parentClosed:
		log.Printf("[服务] GUI 已关闭，正在停止服务")
	}
	// Cancel active requests too, so running command trees cannot outlive the GUI.
	cancelServe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
