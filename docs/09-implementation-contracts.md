# 09 · Implementation contracts (orchestration)

> **Implementation note (2026-11-26, v0.2.0):** all packages in the ownership map
> below have been implemented and pass `go test -race ./...`. The CLI root wires
> them through `internal/app`. The second command batch (SSH, automation,
> updater, utility) and full cosign/SBOM bundle verification are implemented;
> the signed-update path was exercised by a real v0.1.9 → v0.2.0 self-update.
> Remaining work is human-gated (OAuth consent, Drive browser approval,
> interactive TTY, real devices, induced WS-drop) plus the still-empty
> wire/golden fixture layer. A billable live e2e pass is recorded in
> `CHANGELOG.md` `[0.2.0]` Verification.
>
> **2026-11-26 (six tiers):** the current CLI command surface is the one printed
> by `golab --help` and documented in [USAGE.md](USAGE.md) §1–§6 (notably `reap`,
> `cost`, `wait`, `jobs`, `logs`, the VM inspection commands, `env freeze`,
> `bundle`, `config`, `init`, `use`, `state migrate`, `compat report`, `trace`
> and `doctor --fix`). New leaf/gate packages are `internal/cost`, `vmstat`,
> `detach`, `envlock`, `nbparams`, `compat`, `doctorfix` and `trace`; all are
> fake-tested with **no live e2e proof**. Status is in
> [CONFORMANCE.md](CONFORMANCE.md); detached-run non-goals are in
> [ADR-001](adr-001-detached-runs.md). No new constructor or import contract is
> asserted here — read the package sources for APIs.
>
> Dated: 2026-11-26. This file is the **integration contract** between packages.
> It is intentionally short. The authoritative behaviour specs remain docs/01–08 and
> the upstream Python source cloned at `/tmp/opencode/google-colab-cli` (HEAD).
> Module path: `github.com/20Youssef10/golab`. Binary: `golab`. Go 1.27.4,
> `CGO_ENABLED=0`. Current release: `v0.4.0` (last published: `v0.3.0`).

## Ground rules for every package

1. **Test first.** Write the failing Go test (name the upstream Python test it ports in
   a comment), run it, then implement. Use `go test -race -count=1 ./...`.
2. Protocol packages (`colabapi`, `runtime`, `contents`, `sshx`, `auth`) take plain
   values and **never import `internal/cli`, `internal/app` or `internal/state`**.
3. `internal/cli` may import anything; nothing imports `internal/cli`.
4. No package-level mutable state (one `sync.Once` for the Termux netfix is the only
   exception). All I/O functions take `context.Context`.
5. Typed errors + `errors.As`/`errors.Is`. Never match `"404"`/`"401"` in strings.
6. Explicit HTTP timeouts everywhere (connect/TLS/header/idle). No `InsecureSkipVerify`.
7. Never format a token into a string. Wrap it in `logx.Secret`; `String()` and
   `LogValue()` return `REDACTED`.
8. Remote paths use `path`, local paths use `filepath`.
9. Do **not** edit `go.mod` / `go.sum` — all dependencies are pre-declared. If you need
   a new one, report it instead.
10. Own only the files listed for your work package. Never overwrite another agent's
    files. Coordinate through this document, not by editing it unless you own it.

## Foundation APIs (owned by the orchestrator — stable, import freely)

### `internal/logx`
```go
type Secret struct{ /* unexported */ }
func NewSecret(v string) Secret
func (s Secret) Reveal() string            // the ONLY way to get the value
func (s Secret) String() string            // always "REDACTED"
func (s Secret) LogValue() slog.Value      // slog.StringValue("REDACTED")
func (s Secret) MarshalJSON() ([]byte, error)   // persists the real value
func (s *Secret) UnmarshalJSON([]byte) error

func Setup(toStderr bool, debug bool) error
func Logger() *slog.Logger                 // never nil
func Redact(s string) string               // defensive free-form redaction pass
```

### `internal/version`
```go
const Number = "0.4.0"
const Upstream = "0.8.x+HEAD"
func String() string   // "golab 0.4.0 (compatible with google-colab-cli 0.8.x+HEAD)"
func ClientAgent() string // "github.com/20Youssef10/golab/0.4.0" (single honest identity)
```

### `internal/platform`
```go
func ConfigDir() (string, error)   // $COLAB_CONFIG_DIR, else OS-specific colab-cli dir
func SettingsPath() (string, error)
func SessionsPath() (string, error)
func HistoryDir() (string, error)
func CacheDir() (string, error)
func TempDir() string              // $TMPDIR, $PREFIX/tmp, os.TempDir()
func IsTermux() bool
func IsStdinTTY() bool
func IsStdoutTTY() bool
func IsStderrTTY() bool
func MakePrivate(path string) error
func InstallNetFix()               // Termux DNS/CA shim; no-op elsewhere
func RemotePath(elem ...string) string  // path.Join
```

### `internal/state`
```go
type LastExecution struct { Kind string; Value *string; At string }
type Session struct {
    Name string; Token logx.Secret; URL string; Endpoint string
    TokenExpiresAt *time.Time
    Variant string; Accelerator string; MachineShape string
    KernelID, SessionID string
    LastExecution *LastExecution; Running *string
}
type Settings struct {
    UpdateURL string; LastCheck *time.Time; EnableUpdateCheck bool
    LatestVersion string
    UsePublicAPI bool; PublicAPISupported, TunnelAPISupported *bool
}
type Store struct{ /* ... */ }
func NewStore(path string) (*Store, error)
func (s *Store) List() (map[string]Session, error)
func (s *Store) Get(name string) (*Session, error)
func (s *Store) Put(sess Session) error
func (s *Store) Delete(name string) error
func (s *Store) Path() string
type SettingsStore struct{ /* ... */ }
func NewSettingsStore(path string) (*SettingsStore, error)
func (s *SettingsStore) Load() (Settings, error)
func (s *SettingsStore) Save(v Settings) error
func ValidateName(name string) error
var ErrCorrupt = errors.New("corrupt state file")
var ErrNotFound = errors.New("session not found")
```
State writes: cross-process lock, temp file in same dir, fsync, atomic rename;
`.bak` of last good file; **parse failure returns `ErrCorrupt`, never `{}`**.
`sessions.json` also carries an additive top-level `schema_version` (currently
`1`); a legacy file reads as `0` and a newer version aborts. `golab state
migrate --apply` only stamps that key — session records and unknown fields are
preserved verbatim.

## Package ownership map (who writes what)

| Work package | Owner | Files (and only these) |
|---|---|---|
| Control plane | sub-agent `colabapi` | `internal/colabapi/**`, `internal/testutil/fakecolab/**` |
| Auth | sub-agent `auth` | `internal/auth/**` |
| Kernel/runtime | sub-agent `runtime` | `internal/runtime/**`, `internal/testutil/fakekernel/**` |
| Contents API | sub-agent `contents` | `internal/contents/**` |
| History/notebooks | sub-agent `history` | `internal/history/**`, `internal/nb/**` |
| Rendering | sub-agent `render` | `internal/render/**` |
| SSH | sub-agent `sshx` | `internal/sshx/**`, `internal/testutil/fakesshd/**` |
| REPL | sub-agent `repl` | `internal/repl/**` |
| Updater | sub-agent `update` | `internal/update/**` |
| Composition | sub-agent `app` | `internal/app/**` |
| Commands | sub-agent `cli` | `internal/cli/**` |
| Integration & verification | orchestrator | `cmd/golab/**`, `go.mod`, `go.sum`, `docs/**`, CI, Makefiles |

## Conventions sub-agents must follow when designing their package API

* Export small interfaces; accept concrete config structs with exported fields.
* Constructors are `New(cfg Config) (T, error)` (or `NewX(...)`). No global state.
* Return `*Config` fields `Logger *slog.Logger`; when nil, use `logx.Logger()`.
* Expose whatever types your own package needs, but keep token fields as `logx.Secret`.
* Include a `doc.go` with one paragraph describing the package and its upstream source.
* Finish by running `PATH=/tmp/opencode/go/bin:$PATH go test -race -count=1 ./<yourpkg>/...`
  and `go vet ./<yourpkg>/...`. Report the result, the exported API you settled on,
  and any deviation from docs/01–08 in your final message.
