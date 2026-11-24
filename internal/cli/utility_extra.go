package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/logx"
)

const payURL = "https://colab.research.google.com/signup"
const tokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"

//go:embed resources/README.md
var bundledReadme string

//go:embed resources/SKILL.md
var bundledSkill string

// openURL makes a best-effort attempt to launch the system browser. The URL is
// always printed by the caller first, so a headless host stays usable. It
// delegates to the shared launcher in utility.go so Termux/WSL detection and
// stubbing behave identically for `pay`, `url --open` and Drive consent URLs.
func openURL(target string) bool { return openBrowser(target) }

func payCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pay",
		Short: "Open the Colab sign-up page to manage compute units",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			fmt.Fprintf(d.Stderr, "[colab] Opening %s...\n", payURL)
			openURL(payURL)
			return nil
		},
	}
}

// bearerCapture extracts the access token from the Authorization header the
// oauth2 transport attaches to an outgoing request, then short-circuits the
// request with a synthetic response. The token is never formatted into a log,
// an error or stdout; it is used only as the tokeninfo query parameter.
type bearerCapture struct{ token string }

func (c *bearerCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	if h := req.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		c.token = strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
}

func accessTokenFromAuth(ctx context.Context, d *app.Deps) (string, error) {
	client, err := d.Auth.HTTPClient(ctx, nil)
	if err != nil {
		return "", err
	}
	capture := &bearerCapture{}
	c := *client
	if tr, ok := c.Transport.(*oauth2.Transport); ok {
		// Keep the oauth2 transport: it is what attaches the
		// Authorization: Bearer header. Swap only its Base for the capture so
		// the header is seen before the synthetic 200 {} short-circuit. Copy
		// the struct to avoid mutating a transport shared with other callers.
		wrapped := *tr
		wrapped.Base = capture
		c.Transport = &wrapped
	} else {
		// No oauth2 transport means no bearer header can be attached. Keep
		// the capture as the transport so no real request is made; the
		// empty-token check below then fails closed without leaking anything.
		c.Transport = capture
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenInfoURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	if capture.token == "" {
		return "", errors.New("credentials have no access token after refresh")
	}
	return capture.token, nil
}

// formatExpiry accepts both the string and numeric forms tokeninfo returns.
func formatExpiry(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return "<unknown>"
		}
		text = string(number)
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || seconds <= 0 {
		return "<unknown>"
	}
	return fmt.Sprintf("%dm", seconds/60)
}

// printWhoami queries Google's tokeninfo endpoint for identity, scopes and
// expiry. The access token is passed only as a query parameter; no response
// body is ever echoed on a failure path because it can contain the token.
func printWhoami(ctx context.Context, d *app.Deps, provider, token, endpoint string) error {
	target, err := url.Parse(endpoint)
	if err != nil {
		return exitf(1, "[colab] whoami: invalid tokeninfo endpoint.")
	}
	q := target.Query()
	q.Set("access_token", token)
	target.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return exitf(1, "[colab] whoami: could not build request.")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: http.DefaultTransport}
	resp, err := client.Do(req)
	if err != nil {
		return exitf(1, "[colab] whoami: tokeninfo request failed: %s", logx.Redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return exitf(1, "[colab] whoami: tokeninfo returned HTTP %d.", resp.StatusCode)
	}
	var info struct {
		Email     string          `json:"email"`
		Audience  string          `json:"audience"`
		Aud       string          `json:"aud"`
		ExpiresIn json.RawMessage `json:"expires_in"`
		Scope     string          `json:"scope"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return exitf(1, "[colab] whoami: could not parse tokeninfo response.")
	}
	email := info.Email
	if email == "" {
		email = "<unavailable: token has no userinfo.email scope>"
	}
	audience := info.Audience
	if audience == "" {
		audience = info.Aud
	}
	if audience == "" {
		audience = "<none>"
	}
	fmt.Fprintf(d.Stdout, "Auth provider: %s\n", provider)
	fmt.Fprintf(d.Stdout, "Email:         %s\n", email)
	fmt.Fprintf(d.Stdout, "Audience:      %s\n", audience)
	fmt.Fprintf(d.Stdout, "Expires in:    %s\n", formatExpiry(info.ExpiresIn))
	scopes := strings.Fields(info.Scope)
	if len(scopes) > 0 {
		sort.Strings(scopes)
		fmt.Fprintln(d.Stdout, "Scopes:")
		for _, s := range scopes {
			fmt.Fprintf(d.Stdout, "  - %s\n", s)
		}
	} else {
		fmt.Fprintln(d.Stdout, "Scopes:        <none>")
	}
	return nil
}

func whoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "whoami",
		Short:  "Print the active credentials' identity, scopes and expiry",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			token, err := accessTokenFromAuth(cmd.Context(), d)
			if err != nil {
				return exitf(1, "[colab] whoami: %s", logx.Redact(err.Error()))
			}
			return printWhoami(cmd.Context(), d, string(d.Config.AuthProvider), token, tokenInfoURL)
		},
	}
}

func printResourceCommand(use string, hidden bool, content string) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: "Print bundled documentation", Hidden: hidden, Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(depsOf(cmd).Stdout, strings.TrimSpace(content))
			return nil
		},
	}
}

func testPublicAPICommand() *cobra.Command {
	return &cobra.Command{
		Use: "test-public-api", Short: "Probe Public and Tunnel API access",
		Hidden: true, Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			client, err := d.ColabClient(cmd.Context())
			if err != nil {
				return err
			}
			publicOK, tunnelOK, err := client.CheckAPIAccess(cmd.Context())
			if err != nil {
				return err
			}
			if d.SettingsStore != nil {
				settings, loadErr := d.SettingsStore.Load()
				if loadErr == nil {
					p, t := publicOK, tunnelOK
					settings.PublicAPISupported = &p
					settings.TunnelAPISupported = &t
					_ = d.SettingsStore.Save(settings)
				}
			}
			fmt.Fprintf(d.Stdout, "Public API (colaboratory.googleapis.com): %s\n", okLabel(publicOK))
			fmt.Fprintf(d.Stdout, "Tunnel API (colab.research.google.com):   %s\n", okLabel(tunnelOK))
			return nil
		},
	}
}

func okLabel(ok bool) string {
	if ok {
		return "OK"
	}
	return "FAILED"
}

func registerExtraUtilityCommands(root *cobra.Command) {
	root.AddCommand(payCommand())
	root.AddCommand(whoamiCommand())
	root.AddCommand(testPublicAPICommand())
	root.AddCommand(printResourceCommand("readme", false, bundledReadme))
	root.AddCommand(printResourceCommand("README", true, bundledReadme))
	root.AddCommand(printResourceCommand("skill", false, bundledSkill))
	root.AddCommand(printResourceCommand("SKILL", true, bundledSkill))
}
