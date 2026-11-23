// Package app is the composition root: it wires authentication, state,
// control-plane, data-plane and rendering into the dependencies used by
// internal/cli. Nothing outside cli imports it.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/colabapi"
	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
	"github.com/20Youssef10/golab/internal/render"
	"github.com/20Youssef10/golab/internal/runtime"
	"github.com/20Youssef10/golab/internal/state"
	"github.com/20Youssef10/golab/internal/update"
	"github.com/20Youssef10/golab/internal/version"
)

// TokenRefreshMargin is the headroom before proxy-token expiry (docs/01 §5).
const TokenRefreshMargin = 5 * time.Minute

// Config carries only values, never a command or a session store.
type Config struct {
	ClientOAuthConfigPath string
	SessionsFile          string
	SettingsFile          string
	LogToStderr           bool
	Debug                 bool
	AuthProvider          auth.AuthProvider
	// UsePublicAPI nil means "read from settings.json".
	UsePublicAPI *bool
	// ColabDomain/PublicAPI are normally empty (production defaults). They are
	// injectable for the fake server in integration tests.
	ColabDomain string
	PublicAPI   string

	// HTTP client overrides for the data plane. These must NOT carry the
	// control-plane OAuth bearer token.
	RuntimeHTTPClient  *http.Client
	ContentsHTTPClient *http.Client

	Owner, Repo string // pinned self-update source
}

// Deps is the set of dependencies shared by all CLI commands.
type Deps struct {
	Config Config

	Store         *state.Store
	SettingsStore *state.SettingsStore
	Auth          auth.Provider
	Runtime       runtime.Runtime
	HistoryDir    string
	RenderConfig  render.Config

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Log    *slog.Logger

	mu    sync.Mutex
	colab colabapi.Client
}

// New creates the dependency graph. It performs no network I/O: auth is
// established lazily on the first control-plane call, so `version`/`help`
// work without credentials.
func New(ctx context.Context, cfg Config) (*Deps, error) {
	platform.InstallNetFix()
	if err := logx.Setup(cfg.LogToStderr, cfg.Debug); err != nil {
		return nil, err
	}
	if cfg.AuthProvider == "" {
		cfg.AuthProvider = auth.DefaultAuthProvider
	}
	if cfg.Owner == "" {
		cfg.Owner = "golab-cli"
	}
	if cfg.Repo == "" {
		cfg.Repo = "golab"
	}
	store, err := state.NewStore(cfg.SessionsFile)
	if err != nil {
		return nil, err
	}
	settings, err := state.NewSettingsStore(cfg.SettingsFile)
	if err != nil {
		return nil, err
	}
	authProvider, err := auth.New(auth.Config{
		Provider:              cfg.AuthProvider,
		ClientOAuthConfigPath: cfg.ClientOAuthConfigPath,
		ConfigDir:             dirOf(store.Path()),
		Stdin:                 os.Stdin,
		Stdout:                os.Stdout,
		Stderr:                os.Stderr,
		Logger:                logx.Logger(),
	})
	if err != nil {
		return nil, err
	}
	rt, err := runtime.New(runtime.Config{
		Logger:      logx.Logger(),
		HTTPClient:  cfg.RuntimeHTTPClient,
		ClientAgent: version.ClientAgent(),
	})
	if err != nil {
		return nil, err
	}
	histDir, err := platform.HistoryDir()
	if err != nil {
		return nil, err
	}
	d := &Deps{
		Config:        cfg,
		Store:         store,
		SettingsStore: settings,
		Auth:          authProvider,
		Runtime:       rt,
		HistoryDir:    histDir,
		RenderConfig:  render.Config{Markdown: true},
		Stdin:         os.Stdin,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		Log:           logx.Logger(),
	}
	_ = ctx
	return d, nil
}

func platformOS() string { return goruntime.GOOS }

func platformArch() string { return goruntime.GOARCH }

func dirOf(p string) string {
	for len(p) > 1 && p[len(p)-1] != '/' && p[len(p)-1] != '\\' {
		p = p[:len(p)-1]
	}
	return p
}

// SetStreams lets cobra inject its own writers (important for golden tests).
func (d *Deps) SetStreams(in io.Reader, out, errOut io.Writer) {
	d.Stdin, d.Stdout, d.Stderr = in, out, errOut
}

// ColabClient returns the lazily-created control-plane client.
func (d *Deps) ColabClient(ctx context.Context) (colabapi.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.colab != nil {
		return d.colab, nil
	}
	httpClient, err := d.Auth.HTTPClient(ctx, auth.Scopes)
	if err != nil {
		return nil, err
	}
	usePublic := d.Config.UsePublicAPI
	if usePublic == nil {
		settings, err := d.SettingsStore.Load()
		if err != nil {
			return nil, err
		}
		v := settings.UsePublicAPI
		usePublic = &v
	}
	client, err := colabapi.New(colabapi.Config{
		Domain:       d.Config.ColabDomain,
		PublicAPI:    d.Config.PublicAPI,
		HTTPClient:   httpClient,
		ClientAgent:  version.ClientAgent(),
		UsePublicAPI: *usePublic,
		Logger:       d.Log,
	})
	if err != nil {
		return nil, err
	}
	d.colab = client
	return client, nil
}

// History returns a per-session history logger.
func (d *Deps) History(name string) (*history.Logger, error) {
	if err := state.ValidateName(name); err != nil {
		return nil, err
	}
	return history.NewAt(d.HistoryDir, name)
}

// UpdateConfig returns the pinned, throttle-aware updater config.
func (d *Deps) UpdateConfig(ctx context.Context) (update.Config, error) {
	settings, err := d.SettingsStore.Load()
	if err != nil {
		return update.Config{}, err
	}
	return update.Config{
		Owner:          d.Config.Owner,
		Repo:           d.Config.Repo,
		AssetPattern:   "golab_*_" + platformOS() + "_" + platformArch() + ".tar.gz",
		CurrentVersion: version.Number,
		LastCheck:      settings.LastCheck,
		Enabled:        settings.EnableUpdateCheck,
		Logger:         d.Log,
	}, nil
}

// RuntimeFor attaches to the session's kernel and persists the effective ids.
func (d *Deps) RuntimeFor(ctx context.Context, s *state.Session) (runtime.Kernel, error) {
	target := runtime.Target{
		URL:      s.URL,
		Token:    s.Token,
		Endpoint: s.Endpoint,
		KernelID: s.KernelID,
	}
	return d.Runtime.Attach(ctx, target, runtime.AttachOptions{
		OnKernelStarted: func(id string) {
			s.KernelID = id
			_ = d.Store.Put(*s)
		},
		OnSessionStarted: func(id string) {
			s.SessionID = id
			_ = d.Store.Put(*s)
		},
	})
}

// ContentsFor returns a Contents API client for the session's data plane.
func (d *Deps) ContentsFor(s *state.Session) (contents.Client, error) {
	return contents.New(contents.Config{
		BaseURL:     s.URL,
		Token:       s.Token,
		HTTPClient:  d.Config.ContentsHTTPClient,
		ClientAgent: version.ClientAgent(),
		Logger:      d.Log,
	})
}

// GetSession loads a session, refreshing its proxy token when near expiry. A
// gone assignment is pruned and reported as ErrNotFound.
func (d *Deps) GetSession(ctx context.Context, name string) (*state.Session, error) {
	s, err := d.Store.Get(name)
	if err != nil {
		return nil, err
	}
	needsRefresh := s.TokenExpiresAt == nil || time.Until(*s.TokenExpiresAt) <= TokenRefreshMargin
	if !needsRefresh {
		return s, nil
	}
	client, err := d.ColabClient(ctx)
	if err != nil {
		return nil, err
	}
	assignments, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range assignments {
		if a.Endpoint != s.Endpoint {
			continue
		}
		s.Token = a.RuntimeProxyInfo.Token
		s.URL = a.RuntimeProxyInfo.URL
		expiry := a.RuntimeProxyInfo.ExpiresAt(time.Now())
		s.TokenExpiresAt = &expiry
		if err := d.Store.Put(*s); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err := d.PruneSession(name); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %s", state.ErrNotFound, name)
}

// ResolveSession implements the shared resolution rule (docs/05).
func (d *Deps) ResolveSession(ctx context.Context, name string) (*state.Session, error) {
	if name != "" {
		return d.GetSession(ctx, name)
	}
	local, err := d.Store.List()
	if err != nil {
		return nil, err
	}
	if len(local) == 0 {
		return nil, errors.New("No active sessions found. Create one with 'golab new'.")
	}
	sessions, _, err := d.SyncSessions(ctx)
	if err != nil {
		return nil, err
	}
	switch len(sessions) {
	case 0:
		return nil, errors.New("No active sessions found. Create one with 'golab new'.")
	case 1:
		for _, s := range sessions {
			fmt.Fprintf(d.Stderr, "[colab] Using unique session '%s'.\n", s.Name)
			unique := s
			return &unique, nil
		}
		return nil, errors.New("No active sessions found. Create one with 'golab new'.")
	default:
		names := make([]string, 0, len(sessions))
		for n := range sessions {
			names = append(names, n)
		}
		return nil, fmt.Errorf("Multiple active sessions found. Specify one with -s: %v", names)
	}
}

// SyncSessions reconciles local state with the server, prunes gone sessions and
// refreshes proxy tokens. It mirrors upstream State.sync_sessions.
func (d *Deps) SyncSessions(ctx context.Context) (map[string]state.Session, []colabapi.ListedAssignment, error) {
	local, err := d.Store.List()
	if err != nil {
		return nil, nil, err
	}
	client, err := d.ColabClient(ctx)
	if err != nil {
		// Upstream treats an auth failure here as "no assignments" so that
		// `sessions` can still show stale local state.
		if len(local) == 0 {
			return map[string]state.Session{}, nil, nil
		}
		return nil, nil, err
	}
	assignments, err := client.List(ctx)
	if err != nil {
		if len(local) == 0 {
			return map[string]state.Session{}, nil, nil
		}
		return nil, nil, err
	}
	byEndpoint := make(map[string]colabapi.ListedAssignment, len(assignments))
	for _, a := range assignments {
		byEndpoint[a.Endpoint] = a
	}
	pruned := 0
	for name, s := range local {
		a, ok := byEndpoint[s.Endpoint]
		if !ok {
			if err := d.PruneSession(name); err != nil {
				return nil, nil, err
			}
			delete(local, name)
			pruned++
			continue
		}
		s.Token = a.RuntimeProxyInfo.Token
		s.URL = a.RuntimeProxyInfo.URL
		expiry := a.RuntimeProxyInfo.ExpiresAt(time.Now())
		s.TokenExpiresAt = &expiry
		if err := d.Store.Put(s); err != nil {
			return nil, nil, err
		}
		local[name] = s
	}
	if pruned > 0 {
		fmt.Fprintf(d.Stderr, "[colab] Pruned %d stale local session(s).\n", pruned)
	}
	return local, assignments, nil
}

// PruneSession removes local state and records the termination.
func (d *Deps) PruneSession(name string) error {
	if err := d.Store.Delete(name); err != nil {
		return err
	}
	logger, err := d.History(name)
	if err != nil {
		return err
	}
	return logger.Append("session_terminated", map[string]any{"reason": "pruned"})
}
