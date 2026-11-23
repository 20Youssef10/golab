package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Ports upstream tests/test_url.py::test_url_open_flag_launches_browser,
// test_url_open_flag_includes_fragment and test_url_no_open_by_default.
// The launcher is injected so no real browser is spawned and stdout must stay
// byte-exact (the URL only) in both modes.
func TestURLOpenFlagLaunchesBrowser(t *testing.T) {
	h := newHarness(t)
	h.putSession(t, "s1")
	orig := launchBrowserFunc
	var launched []string
	launchBrowserFunc = func(target string) bool {
		launched = append(launched, target)
		return true
	}
	defer func() { launchBrowserFunc = orig }()

	if code := h.run(t, "", "url", "-s", "s1", "--open"); code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	printed := strings.TrimSpace(h.stdout.String())
	if len(launched) != 1 {
		t.Fatalf("launcher calls = %d; want 1 (stdout=%q)", len(launched), h.stdout.String())
	}
	// Upstream asserts webbrowser.open() got the very same URL, fragment
	// included, that was printed on stdout.
	if launched[0] != printed {
		t.Fatalf("launched %q; printed %q", launched[0], printed)
	}
	for _, want := range []string{"dbu=%2Ftun%2Fm%2Fendpoint-test", "#datalabBackendUrl=https://colab.research.google.com/tun/m/endpoint-test"} {
		if !strings.Contains(launched[0], want) {
			t.Fatalf("launched URL %q missing %q", launched[0], want)
		}
	}
	if strings.Contains(h.stdout.String(), "[colab]") {
		t.Fatalf("chatter leaked to stdout: %q", h.stdout.String())
	}

	// Without --open the command is print-only, so it stays pipeable.
	launched = nil
	h.stdout.Reset()
	if code := h.run(t, "", "url", "-s", "s1"); code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if len(launched) != 0 {
		t.Fatalf("launcher called without --open: %q", launched)
	}
}

// Ports the cross-platform intent of utility.py's webbrowser.open: each host
// gets a launcher that never writes to the command's stdout.
func TestBrowserLaunchCommandSelection(t *testing.T) {
	const target = "https://colab.example/notebooks/empty.ipynb#datalabBackendUrl=https://colab.example/tun/m/ep"
	look := func(name string) (string, error) {
		switch name {
		case "termux-open-url", "wslview":
			return "/bin/" + name, nil
		default:
			return "", errors.New("not found")
		}
	}
	for _, tc := range []struct {
		name string
		goos string
		wsl  bool
		want []string
	}{
		{"linux", "linux", false, []string{"xdg-open", target}},
		{"wsl", "linux", true, []string{"wslview", target}},
		{"termux", "linux", false, []string{"termux-open-url", target}},
		{"mac", "darwin", false, []string{"open", target}},
		{"windows", "windows", false, []string{"rundll32", "url.dll,FileProtocolHandler", target}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "termux" {
				// Termux is detected by the presence of termux-open-url.
				look = func(name string) (string, error) {
					if name == "termux-open-url" {
						return "/data/data/com.termux/files/usr/bin/termux-open-url", nil
					}
					return "", errors.New("not found")
				}
			} else {
				look = func(name string) (string, error) {
					switch name {
					case "wslview":
						return "/bin/wslview", nil
					default:
						return "", errors.New("not found")
					}
				}
			}
			name, args := browserLaunchCommand(target, tc.goos, tc.wsl, look)
			got := append([]string{name}, args...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("launcher = %q; want %q", got, tc.want)
			}
		})
	}
}

// Ports upstream tests/test_cli.py::test_cli_help unknown-command branch:
// the raw message (no [colab] prefix) is what upstream prints. Stream stays
// stderr per our documented Δ1.
func TestHelpUnknownCommandMessage(t *testing.T) {
	h := newHarness(t)
	if code := h.run(t, "", "help", "nope"); code != 2 {
		t.Fatalf("exit = %d; stderr=%q", code, h.stderr.String())
	}
	if strings.TrimSpace(h.stderr.String()) != "No such command 'nope'." {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}
