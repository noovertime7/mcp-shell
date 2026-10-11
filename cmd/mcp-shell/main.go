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
	log.Printf("config: %s", *configPath)
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("mcp-shell %s listening on http://%s/mcp", server.Version, cfg.Listen)
	log.Printf("roots: %s", strings.Join(srv.Roots(), ", "))
	if len(cfg.AllowedIPs) == 0 {
		log.Printf("WARNING: allowed_ips is empty; any client that knows the token can connect")
	} else {
		log.Printf("allowed clients: %s", strings.Join(cfg.AllowedIPs, ", "))
	}
	if strings.HasPrefix(cfg.Listen, "0.0.0.0:") || strings.HasPrefix(cfg.Listen, ":") {
		log.Printf("WARNING: listening on all interfaces; prefer the LAN IP that the VM uses")
	}
	if cfg.AuditLog != "" {
		log.Printf("audit log: %s", cfg.AuditLog)
	}
	if cfg.LogFile != "" {
		log.Printf("operation log: %s", cfg.LogFile)
	}
	if cfg.OutputDir != "" {
		log.Printf("full command output: %s", cfg.OutputDir)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			srv.Close()
			log.Fatal(err)
		}
	case <-stop:
		log.Printf("shutting down; stopping background processes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
