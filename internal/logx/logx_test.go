package logx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverLeaks(t *testing.T) {
	s := NewSecret("super-secret-token")
	if got := s.Reveal(); got != "super-secret-token" {
		t.Fatalf("Reveal() = %q", got)
	}
	for name, got := range map[string]string{
		"String":  s.String(),
		"Sprintf": fmt.Sprintf("%v", s),
	} {
		if strings.Contains(got, "super-secret") {
			t.Errorf("%s leaked the secret: %q", name, got)
		}
	}
	var b bytes.Buffer
	l := slog.New(NewHandler(&b, false))
	l.Info("using token", "token", s, "url", "https://x/?token=super-secret-token")
	if strings.Contains(b.String(), "super-secret") {
		t.Errorf("slog leaked the secret: %q", b.String())
	}
}

func TestSecretJSONRoundTrip(t *testing.T) {
	type holder struct {
		Token Secret `json:"token"`
	}
	b, err := json.Marshal(holder{NewSecret("abc123")})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"token":"abc123"}` {
		t.Fatalf("marshalled = %s", b)
	}
	var h holder
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	if h.Token.Reveal() != "abc123" {
		t.Fatalf("round trip = %q", h.Token.Reveal())
	}
}

func TestRedact(t *testing.T) {
	for _, in := range []string{
		`GET /x?token=abcdef123456 Authorization: Bearer abcdef123456 {"token":"abcdef123456"}`,
		`{"token": "abcdef123456", "refresh_token":"abcdef123456"}`,
		`X-Colab-Runtime-Proxy-Token: abcdef123456`,
	} {
		out := Redact(in)
		if strings.Contains(out, "abcdef123456") {
			t.Fatalf("Redact leaked: %q", out)
		}
	}
}

// AUDIT-02 S-6: the original patterns missed these credential field names.
// Each field is exercised in JSON, query-string and slog-attribute form, with
// both snake_case and Capitalised spellings. A canary that survives any form
// is a redaction gap.
func TestRedactNoCredentialFields(t *testing.T) {
	fields := []struct{ key, canary string }{
		{"id_token", "IDTOKENCANARY7f3a"},
		{"IDToken", "CAMELIDCANARY7f3a"},
		{"ID_TOKEN", "UPPERIDCANARY7f3a"},
		{"password", "PASSWORDCANARY7f3a"},
		{"Password", "PWDCANARY7f3a"},
		{"passwd", "PASSWDCANARY7f3a"},
		{"PASSWD", "PASSWDUPPERCANARY7f3a"},
		{"secret", "SECRETCANARY7f3a"},
		{"client_secret", "CLIENTSECRETCANARY7f3a"},
		{"code_verifier", "CODEVERCANARY7f3a"},
		{"CodeVerifier", "CAMELCODEVERCANARY7f3a"},
	}
	forms := map[string]func(k, v string) string{
		"json":  func(k, v string) string { return `{"` + k + `":"` + v + `","next":"ok"}` },
		"query": func(k, v string) string { return `https://example.test/cb?next=ok&` + k + `=` + v },
	}
	for _, f := range fields {
		for form, build := range forms {
			in := build(f.key, f.canary)
			out := Redact(in)
			if strings.Contains(out, f.canary) {
				t.Errorf("Redact(%s %s) leaked %q: %q", form, f.key, f.canary, out)
			}
		}
	}
}

// Golden-style slog test: the entire emitted line must match, so a key that is
// silently added to the "logged verbatim" set fails loudly.
func TestSlogRedactsSensitiveKeys(t *testing.T) {
	var b bytes.Buffer
	h := slog.NewTextHandler(&b, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return redactAttr(nil, a)
	}})
	l := slog.New(h)
	l.Info("evt",
		"id_token", "IDTOKC7f3a",
		"IDToken", "CAMELC7f3a",
		"password", "PASSWC7f3a",
		"passwd", "PASSWDC7f3a",
		"secret", "SECRETC7f3a",
		"code_verifier", "VERIFC7f3a",
		"access_token", "ACCESC7f3a",
		"next", "visible",
	)
	want := "level=INFO msg=evt id_token=REDACTED IDToken=REDACTED password=REDACTED " +
		"passwd=REDACTED secret=REDACTED code_verifier=REDACTED access_token=REDACTED next=visible\n"
	if got := b.String(); got != want {
		t.Fatalf("slog line leaked a credential field:\n got: %q\nwant: %q", got, want)
	}
}
