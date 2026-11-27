// Package mcpserver exposes golab's Colab CLI as a Model Context Protocol
// server. It uses the official Go SDK and implements tools by invoking the
// golab binary as a short-lived subprocess, so it never imports internal/cli
// (only cmd/golab is allowed to do that).
package mcpserver

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/version"
)

//go:embed docs/SKILL.md
var skillDoc string

//go:embed docs/USAGE.md
var usageDoc string

// Config configures the MCP server. An empty GolabBin defaults to "golab"
// resolved on PATH at the time a tool is called.
type Config struct {
	GolabBin string
}

// NewServer builds a server with the full tool suite and bundled resources.
func NewServer(cfg Config) (*mcp.Server, error) {
	s := mcp.NewServer(&mcp.Implementation{
		Name:        "golab",
		Version:     version.Number,
		Title:       "Google Colab via golab",
		Description: "Manage Colab runtimes, execute code and inspect state through the golab CLI. Billable and destructive tools require confirm:true.",
	}, nil)
	registerTools(s, NewRunner(cfg.GolabBin))
	addResources(s)
	return s, nil
}

func addResources(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI:         "golab://docs/skill",
		Name:        "golab agent skill",
		Description: "Bundled guidance for agents using golab, including its safety rules and typical workflow.",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: req.Params.URI, MIMEType: "text/markdown", Text: skillDoc,
		}}}, nil
	})
	s.AddResource(&mcp.Resource{
		URI:         "golab://docs/usage",
		Name:        "golab MCP tool usage",
		Description: "How to call the golab MCP tools safely, including confirm gating and cancellation.",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: req.Params.URI, MIMEType: "text/markdown", Text: usageDoc,
		}}}, nil
	})
}

// NewHTTPHandler serves the server using the official SDK's streamable HTTP
// transport. Both /mcp and / are accepted for client compatibility.
func NewHTTPHandler(s *mcp.Server) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s
	}, &mcp.StreamableHTTPOptions{Logger: logx.Logger()})
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/", handler)
	return mux
}

// Serve runs the server over stdio or streamable HTTP until ctx is cancelled.
// In stdio mode nothing but SDK JSON-RPC is written to stdout; logx must be
// configured by the caller with a stderr sink (see cmd/golab-mcp).
func Serve(ctx context.Context, cfg Config, transport, addr string) error {
	s, err := NewServer(cfg)
	if err != nil {
		return err
	}
	switch transport {
	case "", "stdio":
		return s.Run(ctx, &mcp.StdioTransport{})
	case "http":
		if addr == "" {
			addr = ":8080"
		}
		srv := &http.Server{
			Addr:              addr,
			Handler:           NewHTTPHandler(s),
			ReadHeaderTimeout: 10 * time.Second,
		}
		errCh := make(chan error, 1)
		go func() { errCh <- srv.ListenAndServe() }()
		select {
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			err := <-errCh
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	default:
		return fmt.Errorf("unknown transport %q (want stdio or http)", transport)
	}
}
