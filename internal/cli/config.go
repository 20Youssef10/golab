package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/state"
)

// configKind is the value type of a supported settings key [tier5].
type configKind int

const (
	configBool configKind = iota
	configTriBool
	configString
)

// configKey describes one key of settings.json. Every write goes through
// state.SettingsStore (lock + temp + fsync + rename) and every printed value
// passes through logx.Redact: config must never become a token-printing tool.
type configKey struct {
	name  string
	kind  configKind
	get   func(state.Settings) string
	set   func(*state.Settings, string) error
	unset func(*state.Settings)
}

func boolConfigKey(name string, def bool, get func(state.Settings) bool, set func(*state.Settings, bool)) configKey {
	return configKey{
		name: name, kind: configBool,
		get: func(s state.Settings) string { return strconv.FormatBool(get(s)) },
		set: func(s *state.Settings, v string) error {
			b, err := parseConfigBool(v, name)
			if err != nil {
				return err
			}
			set(s, b)
			return nil
		},
		unset: func(s *state.Settings) { set(s, def) },
	}
}

// stringConfigKey builds a key whose unset restores the Python default.
func stringConfigKey(name string, def string, get func(state.Settings) string, set func(*state.Settings, string)) configKey {
	return configKey{
		name: name, kind: configString, get: get,
		set:   func(s *state.Settings, v string) error { set(s, v); return nil },
		unset: func(s *state.Settings) { set(s, def) },
	}
}

// triBoolConfigKey builds a nullable capability-probe key.
func triBoolConfigKey(name string, get func(state.Settings) *bool, set func(*state.Settings, *bool)) configKey {
	return configKey{
		name: name, kind: configTriBool,
		get: func(s state.Settings) string {
			v := get(s)
			if v == nil {
				return "unset"
			}
			return strconv.FormatBool(*v)
		},
		set: func(s *state.Settings, v string) error {
			b, err := parseConfigBool(v, name)
			if err != nil {
				return err
			}
			set(s, &b)
			return nil
		},
		unset: func(s *state.Settings) { set(s, nil) },
	}
}

// configKeys is the complete supported key set. Aliases are accepted on the
// command line but are not printed by `config list`.
func configKeys() []configKey {
	def := state.DefaultSettings()
	return []configKey{
		boolConfigKey("use_public_api", def.UsePublicAPI,
			func(s state.Settings) bool { return s.UsePublicAPI },
			func(s *state.Settings, b bool) { s.UsePublicAPI = b }),
		boolConfigKey("enable_update_check", def.EnableUpdateCheck,
			func(s state.Settings) bool { return s.EnableUpdateCheck },
			func(s *state.Settings, b bool) { s.EnableUpdateCheck = b }),
		stringConfigKey("update_url", def.UpdateURL,
			func(s state.Settings) string { return s.UpdateURL },
			func(s *state.Settings, v string) { s.UpdateURL = v }),
		triBoolConfigKey("public_api_supported",
			func(s state.Settings) *bool { return s.PublicAPISupported },
			func(s *state.Settings, v *bool) { s.PublicAPISupported = v }),
		triBoolConfigKey("tunnel_api_supported",
			func(s state.Settings) *bool { return s.TunnelAPISupported },
			func(s *state.Settings, v *bool) { s.TunnelAPISupported = v }),
		stringConfigKey("default_session", "",
			func(s state.Settings) string { return s.DefaultSession },
			func(s *state.Settings, v string) { s.DefaultSession = v }),
	}
}

var configAliases = map[string]string{
	"update_check": "enable_update_check",
}

func findConfigKey(name string) (configKey, bool) {
	if canonical, ok := configAliases[strings.ToLower(name)]; ok {
		name = canonical
	}
	for _, k := range configKeys() {
		if k.name == strings.ToLower(name) {
			return k, true
		}
	}
	return configKey{}, false
}

func parseConfigBool(v, key string) (bool, error) {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, usagef("[colab] config: invalid boolean %q for %s.", v, key)
	}
	return b, nil
}

func configUnknownKey(name string) error {
	return usagef("[colab] config: unknown key %q. Run 'golab config list' for supported keys.", name)
}

func configGet(d *app.Deps, rawName string) error {
	k, ok := findConfigKey(rawName)
	if !ok {
		return configUnknownKey(rawName)
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		return exitf(1, "[colab] config: %s", logx.Redact(err.Error()))
	}
	fmt.Fprintln(d.Stdout, logx.Redact(k.get(settings)))
	return nil
}

func configSet(d *app.Deps, rawName, value string) error {
	k, ok := findConfigKey(rawName)
	if !ok {
		return configUnknownKey(rawName)
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		return exitf(1, "[colab] config: %s", logx.Redact(err.Error()))
	}
	if err := k.set(&settings, value); err != nil {
		return err
	}
	if err := d.SettingsStore.Save(settings); err != nil {
		return exitf(1, "[colab] config: could not save settings: %s", logx.Redact(err.Error()))
	}
	// The value is deliberately not echoed: it may be a secret-bearing URL.
	fmt.Fprintf(d.Stderr, "[colab] config: set %s\n", k.name)
	return nil
}

func configUnset(d *app.Deps, rawName string) error {
	k, ok := findConfigKey(rawName)
	if !ok {
		return configUnknownKey(rawName)
	}
	settings, err := d.SettingsStore.Load()
	if err != nil {
		return exitf(1, "[colab] config: %s", logx.Redact(err.Error()))
	}
	k.unset(&settings)
	if err := d.SettingsStore.Save(settings); err != nil {
		return exitf(1, "[colab] config: could not save settings: %s", logx.Redact(err.Error()))
	}
	fmt.Fprintf(d.Stderr, "[colab] config: unset %s\n", k.name)
	return nil
}

func configList(d *app.Deps) error {
	settings, err := d.SettingsStore.Load()
	if err != nil {
		return exitf(1, "[colab] config: %s", logx.Redact(err.Error()))
	}
	keys := configKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].name < keys[j].name })
	for _, k := range keys {
		fmt.Fprintf(d.Stdout, "%s=%s\n", k.name, logx.Redact(k.get(settings)))
	}
	return nil
}

func configCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write local settings.json",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return configList(depsOf(cmd))
		},
	}
	get := &cobra.Command{
		Use: "get KEY", Short: "Print one settings value", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return configGet(depsOf(cmd), args[0])
		},
	}
	set := &cobra.Command{
		Use: "set KEY VALUE", Short: "Set one settings value", Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return configSet(depsOf(cmd), args[0], args[1])
		},
	}
	unset := &cobra.Command{
		Use: "unset KEY", Short: "Reset one value to its default", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return configUnset(depsOf(cmd), args[0])
		},
	}
	list := &cobra.Command{
		Use: "list", Short: "Print all settings values", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return configList(depsOf(cmd))
		},
	}
	path := &cobra.Command{
		Use: "path", Short: "Print the settings.json path", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := depsOf(cmd)
			fmt.Fprintln(d.Stdout, logx.Redact(d.SettingsStore.Path()))
			return nil
		},
	}
	cmd.AddCommand(get, set, unset, list, path)
	return cmd
}

// RegisterConfigCommands is the integration hook for the shared register.go.
func RegisterConfigCommands(root *cobra.Command) { root.AddCommand(configCommand()) }
