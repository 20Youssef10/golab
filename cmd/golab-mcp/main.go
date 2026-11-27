// Command golab-mcp serves the golab CLI as a Model Context Protocol server.
// It is a dedicated static binary: `golab-mcp --transport stdio` for desktop
// agents, or `golab-mcp --transport http --addr :8080` for HTTP clients.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/mcpserver"
)

func main() {
	flag.CommandLine.SetOutput(os.Stderr)
	transport := flag.String("transport", "stdio", "transport: stdio or http")
	addr := flag.String("addr", ":8080", "HTTP listen address (http transport only)")
	bin := flag.String("golab-bin", "golab", "path to the golab binary (defaults to golab on PATH)")
	flag.Parse()

	if err := logx.Setup(true, os.Getenv("COLAB_DEBUG") == "1"); err != nil {
		fmt.Fprintln(os.Stderr, "golab-mcp: logger setup:", err)
		os.Exit(1)
	}
	if *transport != "stdio" && *transport != "http" {
		fmt.Fprintln(os.Stderr, "golab-mcp: --transport must be stdio or http")
		os.Exit(2)
	}
	if _, err := exec.LookPath(*bin); err != nil {
		// A missing binary is not fatal at startup: tools/list must still work.
		logx.Logger().Warn("golab binary not found on PATH; tool calls will fail", "binary", *bin)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.Serve(ctx, mcpserver.Config{GolabBin: *bin}, *transport, *addr); err != nil {
		fmt.Fprintln(os.Stderr, "golab-mcp:", logx.Redact(err.Error()))
		os.Exit(1)
	}
}
