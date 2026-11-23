package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/history"
	"github.com/20Youssef10/golab/internal/version"
)

const defaultFrontendHost = "https://colab.research.google.com"

func urlCommand() *cobra.Command {
	var name, host string
	var open bool
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print a browser URL that connects to an existing session",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			resolved, err := resolveSessionName(cmd.Context(), d, name)
			if err != nil {
				return sessionErr(name, err)
			}
			s, err := d.Store.Get(resolved)
			if err != nil {
				return sessionErr(resolved, err)
			}
			hostClean := strings.TrimRight(host, "/")
			backendPath := "/tun/m/" + s.Endpoint
			connect := fmt.Sprintf("%s/notebooks/empty.ipynb?dbu=%s#datalabBackendUrl=%s%s",
				hostClean, url.QueryEscape(backendPath), hostClean, backendPath)
			fmt.Fprintln(d.Stdout, connect)
			if open {
				openBrowser(connect)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&host, "host", defaultFrontendHost, "Colab frontend origin")
	cmd.Flags().BoolVar(&open, "open", false, "Also open the URL in a browser")
	return cmd
}

func logCommand() *cobra.Command {
	var name, eventType, output string
	var lines int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Manage and view session history logs",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			if name == "" {
				names, err := history.ListSessions(d.HistoryDir)
				if err != nil {
					return err
				}
				if len(names) == 0 {
					fmt.Fprintln(d.Stderr, "[colab] No session history found.")
					return nil
				}
				fmt.Fprintln(d.Stderr, "[colab] Sessions with history logs:")
				for _, n := range names {
					fmt.Fprintf(d.Stdout, "  %s\n", n)
				}
				return nil
			}
			l, err := history.NewAt(d.HistoryDir, name)
			if err != nil {
				return err
			}
			events, err := l.Read()
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Fprintf(d.Stderr, "[colab] No history found for session '%s'.\n", name)
				return nil
			}
			if eventType != "" {
				filtered := events[:0]
				for _, e := range events {
					if e.EventType == eventType {
						filtered = append(filtered, e)
					}
				}
				events = filtered
			}
			if lines > 0 && len(events) > lines {
				events = events[len(events)-lines:]
			}
			if output != "" {
				format := strings.TrimPrefix(strings.ToLower(filepath.Ext(output)), ".")
				if format == "" {
					format = "txt"
				}
				data, err := history.ExportWithSession(events, format, name)
				if err != nil {
					return exitf(1, "[colab] %s", err)
				}
				tmp, err := os.CreateTemp(filepath.Dir(output), ".golab-log-*")
				if err != nil {
					return err
				}
				tmpPath := tmp.Name()
				defer func() { _ = os.Remove(tmpPath) }()
				if _, err := tmp.Write(data); err != nil {
					_ = tmp.Close()
					return err
				}
				if err := tmp.Close(); err != nil {
					return err
				}
				if err := os.Rename(tmpPath, output); err != nil {
					return err
				}
				return nil
			}
			for _, e := range events {
				printHistoryEvent(d, e)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().IntVarP(&lines, "lines", "n", 0, "Number of recent events to show")
	cmd.Flags().StringVarP(&eventType, "type", "t", "", "Filter by event type")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Export to FILE (.ipynb, .md, .txt, .jsonl)")
	return cmd
}

func historyFields(e history.Event) map[string]any {
	m := map[string]any{}
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &m)
	}
	return m
}

func fieldString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func printHistoryEvent(d *app.Deps, e history.Event) {
	ts := e.Timestamp.UTC().Format("2006-01-02 15:04:05")
	f := historyFields(e)
	switch e.EventType {
	case history.EventExecution:
		code := strings.TrimSpace(fieldString(f, "code"))
		first := strings.SplitN(code, "\n", 2)[0]
		runes := []rune(first)
		if len(runes) > 60 {
			first = string(runes[:60])
		}
		fmt.Fprintf(d.Stdout, "[%s] EXEC: %s...\n", ts, first)
	case history.EventFileOperation:
		target := fieldString(f, "path")
		if target == "" {
			target = fieldString(f, "remote")
		}
		fmt.Fprintf(d.Stdout, "[%s] FILE: %s %s\n", ts, fieldString(f, "op"), target)
	case history.EventAutomation:
		fmt.Fprintf(d.Stdout, "[%s] AUTO: %s\n", ts, fieldString(f, "op"))
	case history.EventStdinRequest:
		fmt.Fprintf(d.Stdout, "[%s] INPT: %s\n", ts, strings.TrimSpace(fieldString(f, "prompt")))
	case history.EventInputReply:
		fmt.Fprintf(d.Stdout, "[%s] RPLY: %s\n", ts, strings.TrimSpace(fieldString(f, "value")))
	default:
		fmt.Fprintf(d.Stdout, "[%s] EVENT: %s\n", ts, e.EventType)
	}
}

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the version of the Colab CLI",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(depsOf(cmd).Stdout, version.String())
			return nil
		},
	}
}

func helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [COMMAND]",
		Short: "Show help for a command",
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			root := cmd.Root()
			if len(args) == 0 {
				fmt.Fprint(d.Stdout, root.UsageString())
				return nil
			}
			target, _, err := root.Find(args)
			if err != nil || target == nil || target.Name() != args[0] {
				return exitf(2, "No such command '%s'.", args[0])
			}
			fmt.Fprint(d.Stdout, target.UsageString())
			return nil
		},
	}
}

func completionCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "completion bash|zsh|fish|powershell",
		Short:     "Generate shell completion scripts",
		Args:      exactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			var buf bytes.Buffer
			switch args[0] {
			case "bash":
				_ = cmd.Root().GenBashCompletion(&buf)
			case "zsh":
				_ = cmd.Root().GenZshCompletion(&buf)
			case "fish":
				_ = cmd.Root().GenFishCompletion(&buf, true)
			case "powershell":
				_ = cmd.Root().GenPowerShellCompletion(&buf)
			default:
				return usagef("[colab] Unsupported shell %q.", args[0])
			}
			_, err := d.Stdout.Write(buf.Bytes())
			return err
		},
	}
}

func registerUtilityCommands(root *cobra.Command) {
	root.AddCommand(completionCommand())
	root.AddCommand(helpCommand())
	root.AddCommand(logCommand())
	root.AddCommand(urlCommand())
	root.AddCommand(versionCommand())
}

// launchBrowserFunc is a package variable so tests can stub it and assert that
// `url --open` launches exactly the URL it printed (and that the default does
// not). Production uses launchSystemBrowser.
var launchBrowserFunc = launchSystemBrowser

// browserLaunchCommand selects a launcher for the current host. lookPath and
// isWSL are parameters so the choice is testable without touching the real
// filesystem. Termux and WSL get their native launchers when available.
func browserLaunchCommand(target, goos string, isWSL bool, lookPath func(string) (string, error)) (string, []string) {
	if goos == "windows" {
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	}
	if _, err := lookPath("termux-open-url"); err == nil {
		return "termux-open-url", []string{target}
	}
	if goos == "darwin" {
		return "open", []string{target}
	}
	if goos == "linux" && isWSL {
		if _, err := lookPath("wslview"); err == nil {
			return "wslview", []string{target}
		}
	}
	return "xdg-open", []string{target}
}

// detectWSL is a cheap, read-only probe; no environment-variable workarounds
// are introduced (AGENTS.md platform rules).
func detectWSL() bool {
	if goruntime.GOOS != "linux" {
		return false
	}
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(b))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
}

// launchSystemBrowser makes a best-effort, bounded attempt to open the URL.
// Start() does not block the command, and with no Stdout/Stderr wired the
// launcher's output goes to os.DevNull — piped stdout stays byte-exact. On a
// headless host the caller has already printed the URL, so a false return is
// harmless; no URL (which may carry a token) is ever put in an error message.
func launchSystemBrowser(target string) bool {
	name, args := browserLaunchCommand(target, goruntime.GOOS, detectWSL(), exec.LookPath)
	if _, err := exec.LookPath(name); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed launcher, URL is an argument
	return cmd.Start() == nil
}

func openBrowser(target string) bool { return launchBrowserFunc(target) }
