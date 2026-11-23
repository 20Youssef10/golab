// Package logx provides typed secret redaction and slog setup.
//
// A Secret is safe to pass to fmt/slog: it always renders as REDACTED. The only
// way to obtain the underlying value is Reveal(). JSON marshalling preserves the
// real value so that credentials can be persisted by internal/state.
package logx

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
)

// Secret wraps a credential so it cannot leak through logs or error messages.
type Secret struct{ v string }

// NewSecret wraps v.
func NewSecret(v string) Secret { return Secret{v: v} }

// Reveal returns the underlying credential. Call this only at the point of use
// (e.g. when setting an HTTP header), never on a logging path.
func (s Secret) Reveal() string { return s.v }

// String implements fmt.Stringer. It never reveals the secret.
func (s Secret) String() string { return "REDACTED" }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue("REDACTED") }

// MarshalJSON persists the real value (state files must round-trip).
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.v) }

// UnmarshalJSON restores a persisted secret.
func (s *Secret) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.v = v
	return nil
}

var (
	loggerMu sync.RWMutex
	logger   = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// NewHandler returns the redacting handler used by Setup. Tests use it to
// assert that no secret survives on a log sink.
func NewHandler(w io.Writer, debug bool) slog.Handler {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr})
}

// Setup configures the process logger. Logging is off unless toStderr or debug
// is requested, matching the "off by default" rule in docs/03 §8.
func Setup(toStderr, debug bool) error {
	w := io.Writer(io.Discard)
	if toStderr || debug {
		w = os.Stderr
	}
	setLogger(slog.New(NewHandler(w, debug)))
	return nil
}

func redactAttr(groups []string, a slog.Attr) slog.Attr {
	// Free-form values (URLs, bodies) get a defensive regex pass too.
	if a.Value.Kind() == slog.KindString {
		a.Value = slog.StringValue(Redact(a.Value.String()))
	}
	if isSensitiveKey(a.Key) {
		if a.Value.Kind() != slog.KindAny || !isSecret(a.Value.Any()) {
			a.Value = slog.StringValue("REDACTED")
		}
	}
	return a
}

// isSensitiveKey matches credential-bearing slog keys. The key is normalised
// (lower-cased with separators removed) so snake_case, kebab-case, camelCase and
// SCREAMING_SNAKE spellings of the same field are all covered — callers do not
// have to agree on a naming convention to stay safe.
func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	k = strings.NewReplacer("_", "", "-", "", ".", "").Replace(k)
	switch k {
	case "authorization", "proxyauthorization", "cookie", "setcookie",
		"xcolabruntimeproxytoken",
		"xgoogcolabtoken", "xcolabtoken", "xcolabsshtoken",
		"token", "accesstoken", "refreshtoken", "idtoken", "sessiontoken",
		"clientsecret", "secret", "code", "authcode", "verifier",
		"codeverifier", "password", "passwd", "assertion":
		return true
	}
	return false
}

func isSecret(v any) bool {
	_, ok := v.(Secret)
	return ok
}

func setLogger(l *slog.Logger) {
	loggerMu.Lock()
	logger = l
	loggerMu.Unlock()
}

// Logger returns the process logger, never nil.
func Logger() *slog.Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return logger
}

var freeFormPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/-]{8,}=*`),
	// JSON field forms. [_-]? absorbs snake_case, kebab-case and camelCase;
	// (?i) covers SCREAMING_SNAKE and any other capitalisation.
	regexp.MustCompile(`(?i)("(?:id[_-]?token|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|code[_-]?verifier|password|passwd|secret|assertion|token|code)"\s*:\s*)"[^"]{2,}"`),
	// Query-string forms (proxy tokens, OAuth codes, password/secret fields).
	regexp.MustCompile(`(?i)([?&](?:colab[_-]runtime[_-]proxy[_-]token|id[_-]?token|access[_-]?token|refresh[_-]?token|session[_-]?token|client[_-]?secret|auth[_-]?code|code[_-]?verifier|password|passwd|secret|assertion|token|code|verifier)=)[^&\s"'#]+`),
	regexp.MustCompile(`(?i)(\b(?:x-colab-runtime-proxy-token|x-goog-colab-token|x-colab-token|authorization|proxy-authorization|cookie|set-cookie)\b[ \t]*:[ \t]*)[^\r\n]+`),
}

// Redact is a defensive pass over free-form bodies before they are logged.
func Redact(s string) string {
	for _, re := range freeFormPatterns {
		s = re.ReplaceAllString(s, "${1}REDACTED")
	}
	return s
}
