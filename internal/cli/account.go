package cli

// Named accounts are a golab extension: no upstream Python command is ported
// here. Every profile field is non-secret; token material is only ever touched
// through auth's Secret-aware stores and never printed by these commands.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/account"
	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/auth"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/platform"
)

// accountSignIn and accountClearToken are test seams: production runs the real
// auth flow and token clearing; tests substitute in-memory fakes.
var accountSignIn = defaultAccountSignIn
var accountClearToken = defaultAccountClearToken

func accountConfigDir(d *app.Deps) (string, error) {
	if d.ConfigDir != "" {
		return d.ConfigDir, nil
	}
	return platform.ConfigDir()
}

func accountRegistry(d *app.Deps) (*account.Registry, error) {
	if d.Accounts != nil {
		return d.Accounts, nil
	}
	dir, err := accountConfigDir(d)
	if err != nil {
		return nil, err
	}
	return account.New(filepath.Join(dir, account.FileName))
}

func ensuredAccountRegistry(d *app.Deps) (*account.Registry, account.File, error) {
	r, err := accountRegistry(d)
	if err != nil {
		return nil, account.File{}, err
	}
	f, err := r.Ensure()
	return r, f, err
}

// currentAccountName is the profile this invocation is acting on. Injected
// test deps often have no resolved Account, so the env and registry are the
// fallbacks.
func currentAccountName(d *app.Deps) string {
	if v := strings.TrimSpace(d.Account); v != "" {
		return v
	}
	if v := app.ConfiguredAccountName(""); v != "" {
		return v
	}
	return account.DefaultName
}

func defaultAccountSignIn(ctx context.Context, d *app.Deps, name string) error {
	dir, err := accountConfigDir(d)
	if err != nil {
		return err
	}
	provider, err := auth.New(auth.Config{
		Provider:              d.Config.AuthProvider,
		ScopeMode:             d.Config.Scopes,
		ClientOAuthConfigPath: d.Config.ClientOAuthConfigPath,
		ConfigDir:             dir,
		KeyringUser:           account.KeyringUser(name),
		TokenFileName:         account.TokenFileName(name),
		Stdin:                 d.Stdin,
		Stdout:                d.Stdout,
		Stderr:                d.Stderr,
		Logger:                d.Log,
	})
	if err != nil {
		return err
	}
	// HTTPClient is where the interactive OAuth flow actually runs.
	_, err = provider.HTTPClient(ctx, d.Config.Scopes.Scopes())
	return err
}

func defaultAccountClearToken(ctx context.Context, d *app.Deps, name string) error {
	dir, err := accountConfigDir(d)
	if err != nil {
		return err
	}
	_, err = auth.ClearToken(ctx, auth.Config{
		Provider:      d.Config.AuthProvider,
		ConfigDir:     dir,
		KeyringUser:   account.KeyringUser(name),
		TokenFileName: account.TokenFileName(name),
	})
	return err
}

func accountCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage named Colab accounts and their isolated sessions",
		Args:  noArgs,
	}
	cmd.AddCommand(accountAddCommand(), accountListCommand(), accountUseCommand(),
		accountRemoveCommand(), accountShowCommand())
	return cmd
}

func accountAddCommand() *cobra.Command {
	var email, providerFlag string
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a named account and sign in",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			name := strings.TrimSpace(args[0])
			if err := account.ValidateName(name); err != nil {
				return usagef("[colab] account add: %v", err)
			}
			provider, err := auth.ParseAuthProvider(providerFlag)
			if err != nil {
				return usagef("[colab] account add: %v", err)
			}
			r, _, err := ensuredAccountRegistry(d)
			if err != nil {
				return err
			}
			if err := r.Add(account.Profile{
				Name: name, Email: strings.TrimSpace(email),
				Provider: provider.String(), Scopes: d.Config.Scopes.Scopes(),
			}); err != nil {
				return exitf(1, "[colab] account add: %s", logx.Redact(err.Error()))
			}
			fmt.Fprintf(d.Stderr, "[colab] Account '%s' added. Sign-in needs interactive OAuth consent, which is human-only; complete it in your own terminal if prompted.\n", name)
			if err := accountSignIn(cmd.Context(), d, name); err != nil {
				return exitf(1, "[colab] Account '%s' was added but sign-in did not complete (%s). Finish the human-only OAuth consent in your own terminal, then run 'golab account use %s'.",
					name, logx.Redact(err.Error()), name)
			}
			fmt.Fprintf(d.Stderr, "[colab] Account '%s' signed in. Run 'golab account use %s' to make it active.\n", name, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "Optional account email (may be filled after first sign-in)")
	cmd.Flags().StringVar(&providerFlag, "provider", string(auth.DefaultAuthProvider), "Credential provider: oauth2 or adc")
	return cmd
}

type accountListJSON struct {
	Active   string            `json:"active"`
	Accounts []account.Profile `json:"accounts"`
}

func accountListCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List accounts",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			r, _, err := ensuredAccountRegistry(d)
			if err != nil {
				return err
			}
			profiles, err := r.List()
			if err != nil {
				return err
			}
			active, err := r.Active()
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(d.Stdout).Encode(accountListJSON{Active: active, Accounts: profiles})
			}
			for _, p := range profiles {
				marker := " "
				if p.Name == active {
					marker = "*"
				}
				fmt.Fprintf(d.Stdout, "%s %s", marker, p.Name)
				if p.Email != "" {
					fmt.Fprintf(d.Stdout, "  %s", p.Email)
				}
				if p.Provider != "" {
					fmt.Fprintf(d.Stdout, "  [%s]", p.Provider)
				}
				fmt.Fprintln(d.Stdout)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the account list as JSON")
	return cmd
}

func accountUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Select the active account for later invocations",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			name := strings.TrimSpace(args[0])
			if err := account.ValidateName(name); err != nil {
				return usagef("[colab] account use: %v", err)
			}
			r, _, err := ensuredAccountRegistry(d)
			if err != nil {
				return err
			}
			if _, err := r.Get(name); err != nil {
				return usagef("[colab] account use: %s", logx.Redact(err.Error()))
			}
			if err := r.SetActive(name); err != nil {
				return err
			}
			fmt.Fprintf(d.Stderr, "[colab] Active account set to '%s'. Override for one invocation with --account or GOLAB_ACCOUNT.\n", name)
			return nil
		},
	}
}

func accountRemoveCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an account and its isolated credentials",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			name := strings.TrimSpace(args[0])
			if err := account.ValidateName(name); err != nil {
				return usagef("[colab] account remove: %v", err)
			}
			r, _, err := ensuredAccountRegistry(d)
			if err != nil {
				return err
			}
			switch err := r.Remove(name, force); {
			case errors.Is(err, account.ErrActive):
				return usagef("[colab] Account '%s' is active. Re-run with --force to remove it.", name)
			case errors.Is(err, account.ErrLast):
				return usagef("[colab] Refusing to remove the last account.")
			case errors.Is(err, account.ErrNotFound):
				return usagef("[colab] account remove: %s", logx.Redact(err.Error()))
			case err != nil:
				return err
			}
			if err := accountClearToken(cmd.Context(), d, name); err != nil {
				return exitf(1, "[colab] Account '%s' was removed but its token could not be cleared: %s", name, logx.Redact(err.Error()))
			}
			if name != account.DefaultName {
				dir, err := accountConfigDir(d)
				if err != nil {
					return err
				}
				store := filepath.Join(dir, account.SessionsFileName(name))
				if err := os.Remove(store); err != nil && !errors.Is(err, os.ErrNotExist) {
					return exitf(1, "[colab] Account '%s' was removed but its session store could not be deleted: %s", name, logx.Redact(err.Error()))
				}
				_ = os.Remove(store + ".lock")
			}
			fmt.Fprintf(d.Stderr, "[colab] Account '%s' removed.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Remove the active account as well")
	return cmd
}

type accountShowJSON struct {
	account.Profile
	Active       bool   `json:"active"`
	SessionsFile string `json:"sessions_file"`
}

func accountShowCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [NAME]",
		Short: "Show one account (default: active)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			name := currentAccountName(d)
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			}
			if err := account.ValidateName(name); err != nil {
				return usagef("[colab] account show: %v", err)
			}
			r, _, err := ensuredAccountRegistry(d)
			if err != nil {
				return err
			}
			p, err := r.Get(name)
			if err != nil {
				return usagef("[colab] account show: %s", logx.Redact(err.Error()))
			}
			active, err := r.Active()
			if err != nil {
				return err
			}
			dir, err := accountConfigDir(d)
			if err != nil {
				return err
			}
			view := accountShowJSON{
				Profile: p, Active: p.Name == active,
				SessionsFile: filepath.Join(dir, account.SessionsFileName(p.Name)),
			}
			if asJSON {
				return json.NewEncoder(d.Stdout).Encode(view)
			}
			status := ""
			if view.Active {
				status = " (active)"
			}
			fmt.Fprintf(d.Stdout, "Name: %s%s\n", p.Name, status)
			email := p.Email
			if email == "" {
				email = "(not set)"
			}
			fmt.Fprintf(d.Stdout, "Email: %s\n", email)
			provider := p.Provider
			if provider == "" {
				provider = string(auth.DefaultAuthProvider)
			}
			fmt.Fprintf(d.Stdout, "Provider: %s\n", provider)
			fmt.Fprintf(d.Stdout, "Scopes: %s\n", strings.Join(p.Scopes, ", "))
			fmt.Fprintf(d.Stdout, "Created: %s\n", p.CreatedAt)
			fmt.Fprintf(d.Stdout, "Sessions: %s\n", view.SessionsFile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the account as JSON")
	return cmd
}

// RegisterAccountCommands is the integration hook for register.go.
func RegisterAccountCommands(root *cobra.Command) { root.AddCommand(accountCommand()) }
