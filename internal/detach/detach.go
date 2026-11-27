// Package detach builds detached-run payloads and manages the small local
// manifest that indexes them. It takes plain values only and deliberately does
// not import internal/state or internal/cli, so it stays promotable to a public
// module. All remote paths use `path`; the local manifest path uses `filepath`.
//
// ADR-001: detached jobs run REMOTELY with start_new_session=True. Nothing
// persists locally except this credential-free manifest, and observation is
// on-demand polling only — there is no local daemon and no keep-alive.
package detach

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// RunsRoot is the remote (VM) directory that holds every detached run.
	RunsRoot = "/content/.golab/runs"
	// MetaFile, LogFile, ExitFile and PidFile are written on the VM.
	MetaFile = "meta.json"
	LogFile  = "log"
	ExitFile = "exit"
	PidFile  = "pid"

	// PayloadFile, RunnerFile and WatchdogFile are internal launcher scratch
	// files written on the VM. WatchdogLogFile captures watchdog diagnostics.
	PayloadFile     = "payload.b64"
	RunnerFile      = "runner.py"
	WatchdogFile    = "watchdog.py"
	WatchdogLogFile = "watchdog.log"

	// TTLReasonFile records why a run ended; TTLExceeded is the reason written
	// by the remote watchdog when it kills a job at its deadline. TTLExitCode
	// is the exit value recorded for that kill.
	TTLReasonFile = "ttl-reason"
	TTLExceeded   = "ttl_exceeded"
	TTLExitCode   = "124"

	// ManifestSchema is the local index schema.
	ManifestSchema = "golab/detach/1"
)

// WatchInterval is the watchdog's remote polling granularity. It is embedded
// in the payload, so the watchdog needs no clock coordination with the CLI.
const WatchInterval = 5 * time.Second

// ErrCorrupt means the local manifest could not be decoded. It never carries
// file contents.
var ErrCorrupt = errors.New("detach: corrupt manifest")

// idRE admits only safe, single-segment ids. This is a hard security boundary:
// an id becomes a remote path segment, so traversal and separators are refused.
var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Record is the plain-value description of one detached run. It contains no
// credentials and no source code.
type Record struct {
	ID        string    `json:"id"`
	Session   string    `json:"session"`
	Script    string    `json:"script"`
	StartedAt time.Time `json:"started_at"`
	// TTL is the duration string as supplied by the user (e.g. "1h").
	TTL string `json:"ttl"`
	// DeadlineAt is the absolute UTC deadline written into meta.json and
	// enforced by the remote watchdog. It is fixed at submission time; no
	// local timer or daemon depends on it after the launcher cell returns.
	DeadlineAt time.Time `json:"deadline_at"`
}

// Deadline returns the absolute deadline for r. Records written before remote
// TTL enforcement have no DeadlineAt, so they fall back to StartedAt+TTL; a
// record with neither yields no deadline (jobs then reports "unknown").
func (r Record) Deadline() (time.Time, bool) {
	if !r.DeadlineAt.IsZero() {
		return r.DeadlineAt.UTC(), true
	}
	d, err := time.ParseDuration(r.TTL)
	if err != nil || d <= 0 || r.StartedAt.IsZero() {
		return time.Time{}, false
	}
	return r.StartedAt.Add(d).UTC(), true
}

// Manifest is the local on-disk index of detached runs.
type Manifest struct {
	Schema string            `json:"schema"`
	Runs   map[string]Record `json:"runs"`
}

// ValidID reports whether id is safe to use as a remote path segment.
func ValidID(id string) bool { return idRE.MatchString(id) }

// NewID returns a fresh, valid run id. The crypto/rand path is preferred; the
// time-based fallback keeps id generation total and never returns an unsafe id.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "run-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("run-%016x", time.Now().UnixNano())
}

// RunDir returns the VM directory for id. Callers must validate id first with
// ValidID; NewID always produces a valid id.
func RunDir(id string) string { return path.Join(RunsRoot, id) }

// RemoteFile returns the VM path of one file inside a run directory.
func RemoteFile(id, name string) string { return path.Join(RunDir(id), name) }

// payload is the base64/JSON envelope decoded by the remote runner. Every
// user-controlled value is carried here, never spliced into Python source.
type payload struct {
	Name          string            `json:"name"`
	Script        string            `json:"script"` // base64 of the script bytes
	Argv          []string          `json:"argv"`
	Env           map[string]string `json:"env"`
	RunDir        string            `json:"run_dir"`
	Meta          string            `json:"meta"`           // base64 of the JSON-encoded Record
	Deadline      int64             `json:"deadline"`       // absolute deadline, Unix seconds (ceil)
	WatchInterval float64           `json:"watch_interval"` // watchdog polling granularity, seconds
}

func buildPayload(r Record, script []byte, args []string, env map[string]string) (payload, error) {
	deadline, ok := r.Deadline()
	if !ok {
		return payload{}, fmt.Errorf("detach: run %q has no deadline; --ttl is required", r.ID)
	}
	meta, err := json.Marshal(r)
	if err != nil {
		return payload{}, err
	}
	name := path.Base(r.Script)
	if name == "" || name == "." || name == "/" {
		name = "script.py"
	}
	argv := append([]string{name}, args...)
	envCopy := make(map[string]string, len(env))
	for k, v := range env {
		envCopy[k] = v
	}
	deadlineUnix := deadline.Unix()
	if deadline.Nanosecond() > 0 {
		deadlineUnix++ // ceil: the watchdog must never fire early
	}
	return payload{
		Name:          name,
		Script:        base64.StdEncoding.EncodeToString(script),
		Argv:          argv,
		Env:           envCopy,
		RunDir:        RunDir(r.ID),
		Meta:          base64.StdEncoding.EncodeToString(meta),
		Deadline:      deadlineUnix,
		WatchInterval: WatchInterval.Seconds(),
	}, nil
}

// pyString returns a safe Python string literal. JSON string escaping is a
// subset of Python's, so the result is round-trippable and never injects code.
func pyString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// runnerSource is fixed Python source: it contains no user text. It reads the
// safe payload from disk, executes the script and records the exit code.
const runnerSource = `import base64, json, os, sys, traceback
_R = sys.argv[1]
with open(os.path.join(_R, 'payload.b64')) as _f:
    _p = json.loads(base64.b64decode(_f.read()))
os.environ.update(_p['env'])
sys.argv = _p['argv']
_code = base64.b64decode(_p['script']).decode('utf-8')
_rc = 0
try:
    exec(compile(_code, _p['name'], 'exec'), {'__name__': '__main__'})
except SystemExit as _e:
    if isinstance(_e.code, int):
        _rc = _e.code
    elif _e.code is None:
        _rc = 0
    else:
        _rc = 1
except BaseException:
    traceback.print_exc()
    _rc = 1
with open(os.path.join(_R, 'exit'), 'w') as _f:
    _f.write(str(_rc))
`

// watchdogSource is fixed Python source: it contains no user text. It reads
// its parameters from the same base64/JSON payload as the runner, sleeps until
// the absolute deadline, then kills the job's process group and records the
// exit code and reason. It is started detached on the VM, so no local timer,
// goroutine or process survives `run --detach`.
const watchdogSource = `import base64, json, os, signal, sys, time
_R = sys.argv[1]
with open(os.path.join(_R, 'payload.b64')) as _f:
    _p = json.loads(base64.b64decode(_f.read()))
_deadline = float(_p['deadline'])
_interval = float(_p['watch_interval'])
with open(os.path.join(_R, 'pid')) as _f:
    _pid = int(_f.read().strip())
while True:
    _left = _deadline - time.time()
    if _left <= 0:
        break
    time.sleep(min(_interval, _left))
if os.path.exists(os.path.join(_R, 'exit')):
    sys.exit(0)
try:
    os.kill(_pid, 0)
except OSError:
    sys.exit(0)
try:
    os.killpg(_pid, signal.SIGKILL)
except OSError:
    try:
        os.kill(_pid, signal.SIGKILL)
    except OSError:
        pass
with open(os.path.join(_R, 'ttl-reason'), 'w') as _f:
    _f.write('ttl_exceeded')
with open(os.path.join(_R, 'exit'), 'w') as _f:
    _f.write('124')
`

// BuildLauncher produces the single Python cell submitted by `run --detach`.
// It writes meta.json with the absolute deadline, then starts the user's script
// and a bounded TTL watchdog, both with start_new_session=True so they outlive
// the launching cell and the local CLI. Nothing persists locally: the local
// CLI starts no process and holds no keep-alive.
func BuildLauncher(r Record, script []byte, args []string, env map[string]string) (string, error) {
	if !ValidID(r.ID) {
		return "", fmt.Errorf("detach: unsafe run id %q", r.ID)
	}
	p, err := buildPayload(r, script, args, env)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("import base64, os, subprocess, sys\n")
	fmt.Fprintf(&b, "_R = %s\n", pyString(RunDir(r.ID)))
	b.WriteString("os.makedirs(_R, exist_ok=True)\n")
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'w') as _f:\n", pyString(MetaFile))
	fmt.Fprintf(&b, "    _f.write(base64.b64decode(%s).decode('utf-8'))\n", pyString(p.Meta))
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'w') as _f:\n", pyString(PayloadFile))
	fmt.Fprintf(&b, "    _f.write(%s)\n", pyString(base64.StdEncoding.EncodeToString(encoded)))
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'w') as _f:\n", pyString(RunnerFile))
	fmt.Fprintf(&b, "    _f.write(%s)\n", pyString(runnerSource))
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'w') as _f:\n", pyString(WatchdogFile))
	fmt.Fprintf(&b, "    _f.write(%s)\n", pyString(watchdogSource))
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'wb') as _log:\n", pyString(LogFile))
	fmt.Fprintf(&b, "    _p = subprocess.Popen([sys.executable, os.path.join(_R, %s), _R], start_new_session=True, stdout=_log, stderr=subprocess.STDOUT, cwd='/content')\n", pyString(RunnerFile))
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'w') as _f:\n", pyString(PidFile))
	b.WriteString("    _f.write(str(_p.pid))\n")
	fmt.Fprintf(&b, "with open(os.path.join(_R, %s), 'wb') as _wlog:\n", pyString(WatchdogLogFile))
	fmt.Fprintf(&b, "    subprocess.Popen([sys.executable, os.path.join(_R, %s), _R], start_new_session=True, stdout=_wlog, stderr=subprocess.STDOUT, cwd='/content')\n", pyString(WatchdogFile))
	b.WriteString("print('golab detached run started: ' + str(_p.pid))\n")
	return b.String(), nil
}

// ManifestPath returns the local index path for dir (a local path, so filepath
// is correct here — see scripts/check-paths.sh).
func ManifestPath(dir string) string { return filepath.Join(dir, "detached-runs.json") }

// LoadManifest reads the local index. A missing file is an empty manifest; a
// corrupt file is a typed error and never resets to empty (AGENTS.md state
// hardening applies in spirit here too).
func LoadManifest(p string) (Manifest, error) {
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return Manifest{Schema: ManifestSchema, Runs: map[string]Record{}}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest could not be decoded", ErrCorrupt)
	}
	if m.Runs == nil {
		m.Runs = map[string]Record{}
	}
	if m.Schema == "" {
		m.Schema = ManifestSchema
	}
	// A tampered manifest must never steer a remote read outside RunsRoot:
	// ids become remote path segments, so refuse invalid ones (ErrCorrupt).
	for id, rec := range m.Runs {
		if !ValidID(id) || rec.ID != id {
			return Manifest{}, fmt.Errorf("%w: invalid run id in manifest", ErrCorrupt)
		}
	}
	return m, nil
}

// SaveManifest atomically writes the local index (0600, temp + rename).
func SaveManifest(p string, m Manifest) error {
	if m.Schema == "" {
		m.Schema = ManifestSchema
	}
	if m.Runs == nil {
		m.Runs = map[string]Record{}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p), ".detached-runs-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}
