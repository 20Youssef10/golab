# Dependencies

Each direct dependency needs a one-line justification (AGENTS.md).

| Module | Why |
|---|---|
| `github.com/spf13/cobra` | CLI commands, flag parsing, `--` handling and shell completion (ADR-003). |
| `github.com/coder/websocket` | Context-aware WS with `NetConn()` for SSH and configurable read limit (ADR-007). |
| `golang.org/x/oauth2` | OAuth2 remote flow and token refresh for the control plane. |
| `golang.org/x/crypto` | Embedded SSH client, terminal primitives and embedded CA roots fallback. |
| `golang.org/x/term` | PTY/raw mode for REPL and interactive SSH. |
| `golang.org/x/sys` | Windows console modes, VT processing and file ACLs. |
| `golang.org/x/sync` | Bounded concurrency for update checks and retries. |
| `golang.org/x/crypto/x509roots/fallback` | CA roots for distroless/Termux hosts with an empty system store. |
| `github.com/zalando/go-keyring` | OS keyring tier of the token store (ADR-006). |
| `github.com/gofrs/flock` | Cross-process shared/exclusive locks for state files. |
| `github.com/google/uuid` | Notebook hashes, session ids and client-generated kernel session ids. |
| `github.com/charmbracelet/glamour` | Markdown rendering on a TTY (ADR-010). |
| `github.com/JohannesKaufmann/html-to-markdown` | `text/html` display data → terminal text. |
| `github.com/alecthomas/chroma/v2` | Python syntax highlighting in the REPL. |
| `github.com/sigstore/sigstore-go` | Real cosign keyless verification: Fulcio chain, Rekor proof/SET, identity and digest binding. |
| `github.com/sigstore/protobuf-specs` | Sigstore bundle/trust-root wire types used by the verifier and its fake bundles. |
| `github.com/theupdateframework/go-tuf/v2` | TUF fetch of rotating public-good Fulcio/Rekor roots. |
| `google.golang.org/protobuf` | Marshalling Sigstore bundle JSON. |
| `github.com/kballard/go-shellquote` | POSIX quoting for `ssh -- CMD` argv forwarding. |
| `github.com/mattn/go-isatty` | Mockable TTY detection across Unix, Windows and Termux. |
| `github.com/stretchr/testify` | Test assertions (stdlib `testing` preferred where sufficient). |
| `go.uber.org/goleak` | End-of-suite goroutine-leak gate for the websocket/goroutine-heavy packages (AUDIT-03 §4.2). |
| `go.yaml.in/yaml/v3` | Strict `KnownFields(true)` decoding of `golab.yaml` budget profiles, replacing a hand-rolled YAML subset parser. |

Policy: `govulncheck` clean, `go mod verify` green, no new dependency without a row above.
