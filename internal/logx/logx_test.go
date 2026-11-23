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
