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
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/20Youssef10/golab/internal/agentinstall"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/version"
)

//go:embed docs/SKILL.md
var skillDoc string

//go:embed docs/USAGE.md
var usageDoc string

// Config configures the MCP server. An empty GolabBin defaults to "golab"
// resolved on PATH at the time a tool is called. HomeDir and ProjectDir are
// injected roots for the install tools; when empty they are resolved from the
// host at call time. SkillsFS defaults to the embedded portable skill trees.
type Config struct {
	GolabBin   string
	HomeDir    string
	ProjectDir string
	SkillsFS   fs.FS
	DirSync    func(string) error
}

// NewServer builds a server with the full tool suite and bundled resources.
func NewServer(cfg Config) (*mcp.Server, error) {
	s := mcp.NewServer(&mcp.Implementation{
		Name:        "golab",
		Version:     version.Number,
		Title:       "Google Colab via golab",
		Description: "Manage Colab runtimes, execute code and inspect state through the golab CLI. Billable and destructive tools require confirm:true.",
	}, nil)
	registerTools(s, NewRunner(cfg.GolabBin), cfg)
	addResources(s)
	return s, nil
}

// installerFor resolves the injected roots lazily so tools/list never depends
// on a user home directory being available.
func (cfg Config) installerFor() (*agentinstall.Installer, error) {
	home := cfg.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
	}
	project := cfg.ProjectDir
	if project == "" {
		var err error
		project, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
	}
	skills := cfg.SkillsFS
	if skills == nil {
		skills = agentinstall.SkillsFS()
	}
	inst := agentinstall.New(home, project, skills)
	inst.DirSync = cfg.DirSync
	return inst, nil
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
