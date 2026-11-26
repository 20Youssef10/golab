package vmstat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// JSONBegin and JSONEnd delimit the structured result a snippet prints to
// stdout. They are plain ASCII so render.Sanitize never alters them.
const (
	JSONBegin = "__GOLAB_VMSTAT_BEGIN__"
	JSONEnd   = "__GOLAB_VMSTAT_END__"
)

// pyPrelude is shared by every snippet. _run is bounded by an explicit timeout
// and never uses a shell; callers that need a pipeline add shell=True with
// shlex.quote explicitly.
const pyPrelude = `import json, shlex, subprocess, os, sys
` + "_BEGIN = " + `"__GOLAB_VMSTAT_BEGIN__"` + "\n" +
	"_END = " + `"__GOLAB_VMSTAT_END__"` + `
def _run(argv, timeout=15):
    try:
        p = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        return p.returncode, p.stdout.decode("utf-8", "replace"), p.stderr.decode("utf-8", "replace")
    except subprocess.TimeoutExpired:
        return 124, "", "timeout"
    except FileNotFoundError:
        return 127, "", "not found"
    except Exception as e:
        return 1, "", type(e).__name__
def _emit(obj):
    print(_BEGIN)
    print(json.dumps(obj))
    print(_END)
`

// pyStringLiteral returns a round-trippable Python string literal. JSON
// string escaping is a subset of Python's, so the payload decode is safe.
func pyStringLiteral(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// pyPayload declares _PAYLOAD by decoding user data at runtime. This is the
// single injection barrier: generated code only references decoded values.
func pyPayload(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		raw = []byte("{}")
	}
	return "_PAYLOAD = json.loads(" + pyStringLiteral(string(raw)) + ")\n"
}

// GrepSpec is the plain-value input to BuildGrepCode.
type GrepSpec struct {
	Pattern    string
	Path       string
	Glob       string
	IgnoreCase bool
	MaxMatches int
}

// BuildPSCode collects process table, GPU, memory and disk in ONE bounded
// kernel execution. nvidia-smi is optional: on CPU/TPU hosts its failure is
// recorded as gpu.available=false instead of failing the whole snapshot.
func BuildPSCode() string {
	return pyPrelude + `
def _procs():
    rc, out, err = _run(["ps", "-eo", "pid,pcpu,pmem,etime,cmd", "--sort=-pcpu"])
    rows = []
    for line in out.splitlines()[1:]:
        parts = line.strip().split(None, 4)
        if len(parts) == 5:
            rows.append({"pid": parts[0], "pcpu": parts[1], "pmem": parts[2], "etime": parts[3], "cmd": parts[4]})
        if len(rows) >= 200:
            break
    return rows
def _gpu():
    rc, out, err = _run(["nvidia-smi", "--query-gpu=index,name,utilization.gpu,memory.used,memory.total", "--format=csv,noheader"])
    if rc == 0:
        return {"available": True, "text": out.strip()}
    return {"available": False, "reason": (err or "unavailable").strip()}
def _mem():
    rc, out, err = _run(["free", "-m"])
    return out.strip() if rc == 0 else ""
def _disk():
    rc, out, err = _run(["df", "-h", "/content"])
    return out.strip() if rc == 0 else ""
_emit({"processes": _procs(), "gpu": _gpu(), "memory": _mem(), "disk": _disk()})
`
}

// BuildTailCode implements server-side tail. "initial" reads the last n lines
// and reports the current size as the offset; follow-up calls use `tail -c
// +offset+1` so only new bytes are transferred. Output is capped at 1 MiB.
//
// NOTE: this is NOT byte-perfect under log rotation: if a file is truncated or
// replaced between polls the byte offset may point past the new end (skipping
// early lines) or re-read unrelated bytes. Detecting rotation robustly needs
// inode tracking, which the single cheap execution per interval cannot provide.
func BuildTailCode(path string, n int, offset int64, initial bool) string {
	payload := map[string]any{"path": path, "n": n, "offset": offset, "initial": initial}
	return pyPrelude + pyPayload(payload) + `
p = _PAYLOAD
path = p["path"]; n = int(p["n"]); offset = int(p["offset"]); initial = bool(p["initial"])
def _size():
    try:
        return os.path.getsize(path)
    except OSError:
        return -1
if initial:
    rc, out, err = _run(["tail", "-n", str(n), path])
else:
    start = offset + 1 if offset > 0 else 1
    rc, out, err = _run(["tail", "-c", "+" + str(start), path])
chunk = out[:1048576]
_emit({"offset": _size(), "chunk": chunk, "exists": _size() >= 0, "error": err.strip() if rc != 0 else ""})
`
}

// BuildGrepCode passes the pattern/path/glob as JSON-decoded data and quotes
// every token with shlex.quote before assembling a shell pipeline. "--" stops
// a pattern beginning with '-' from being read as a grep flag; head bounds the
// number of returned lines.
func BuildGrepCode(spec GrepSpec) string {
	payload := map[string]any{
		"pattern": spec.Pattern, "path": spec.Path, "glob": spec.Glob,
		"ignore_case": spec.IgnoreCase, "max_matches": spec.MaxMatches,
	}
	return pyPrelude + pyPayload(payload) + `
p = _PAYLOAD
argv = ["grep", "-R", "-n", "-I"]
if p["ignore_case"]:
    argv.append("-i")
if p["glob"]:
    argv.append("--include=" + p["glob"])
argv += ["--", p["pattern"], p["path"]]
cmd = " ".join(shlex.quote(a) for a in argv) + " | head -n " + str(int(p["max_matches"]))
try:
    pr = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    out = pr.stdout.decode("utf-8", "replace")
except subprocess.TimeoutExpired:
    out = ""
_emit({"matches": out[:1048576]})
`
}

// BuildDuCode runs `du -sh` per entry under path and bounds the entry count.
func BuildDuCode(path string, maxEntries int) string {
	payload := map[string]any{"path": path, "max_entries": maxEntries}
	return pyPrelude + pyPayload(payload) + `
p = _PAYLOAD
path = p["path"]; mx = int(p["max_entries"])
lines = []
try:
    names = sorted(os.listdir(path))[:mx]
except OSError:
    names = []
for nm in names:
    rc, out, err = _run(["du", "-sh", os.path.join(path, nm)])
    if rc == 0:
        lines.append(out.strip())
_emit({"lines": lines})
`
}

// BuildFindCode runs a bounded find (maxdepth + head) with a shell-quoted name.
func BuildFindCode(path, name string, maxResults int) string {
	payload := map[string]any{"path": path, "name": name, "max_results": maxResults}
	return pyPrelude + pyPayload(payload) + `
p = _PAYLOAD
argv = ["find", p["path"], "-maxdepth", "12", "-name", p["name"]]
cmd = " ".join(shlex.quote(a) for a in argv) + " | head -n " + str(int(p["max_results"]))
try:
    pr = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    out = pr.stdout.decode("utf-8", "replace")
except subprocess.TimeoutExpired:
    out = ""
_emit({"lines": out[:1048576]})
`
}

// BuildFetchCode downloads with `wget -c`, falls back to `curl -C -`, and
// optionally verifies a SHA-256 digest. The URL and destination arrive as JSON
// data; argv is used (no shell) so nothing is interpolated unquoted.
func BuildFetchCode(rawURL, dest, sha256 string, timeoutSeconds int) string {
	payload := map[string]any{
		"url": rawURL, "dest": dest, "sha256": sha256, "timeout": timeoutSeconds,
	}
	return pyPrelude + "import hashlib\n" + pyPayload(payload) + `
p = _PAYLOAD
url = p["url"]; dest = p["dest"]; want = p["sha256"]; tmo = int(p["timeout"])
rc, out, err = _run(["wget", "-c", "-O", dest, url], tmo)
if rc != 0:
    rc, out, err = _run(["curl", "-fL", "-C", "-", "-o", dest, url], tmo)
ok = rc == 0
digest = ""
if ok and want:
    try:
        h = hashlib.sha256()
        with open(dest, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                h.update(chunk)
        digest = h.hexdigest()
        ok = digest.lower() == want.lower()
    except OSError as e:
        ok = False
        err = str(e)
_emit({"ok": ok, "dest": dest, "sha256": digest, "error": err.strip()})
`
}

// ValidateFetchURL accepts only absolute http/https URLs with a host.
func ValidateFetchURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("fetch: URL must be an absolute http(s) URL with a host")
	}
	return nil
}

// ValidateSHA256 enforces a 64-character hex digest; empty means "no check".
func ValidateSHA256(s string) error {
	if s == "" {
		return nil
	}
	if len(s) != 64 {
		return errors.New("fetch: --sha256 must be 64 hexadecimal characters")
	}
	for _, c := range s {
		if !isHexDigit(c) {
			return errors.New("fetch: --sha256 must be 64 hexadecimal characters")
		}
	}
	return nil
}

func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// --- models and envelope parsing --------------------------------------------

type Process struct {
	PID   string `json:"pid"`
	PCPU  string `json:"pcpu"`
	PMEM  string `json:"pmem"`
	ETime string `json:"etime"`
	Cmd   string `json:"cmd"`
}

type GPUInfo struct {
	Available bool   `json:"available"`
	Text      string `json:"text,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type Snapshot struct {
	Processes []Process `json:"processes"`
	GPU       GPUInfo   `json:"gpu"`
	Memory    string    `json:"memory"`
	Disk      string    `json:"disk"`
}

type TailResult struct {
	Offset int64  `json:"offset"`
	Chunk  string `json:"chunk"`
	Exists bool   `json:"exists"`
	Error  string `json:"error"`
}

type LinesResult struct {
	Lines string `json:"lines"`
}

type TextResult struct {
	Matches string `json:"matches"`
}

type FetchResult struct {
	OK     bool   `json:"ok"`
	Dest   string `json:"dest"`
	SHA256 string `json:"sha256"`
	Error  string `json:"error"`
}

// ExtractJSON returns the payload between the envelope markers, tolerating
// warnings before/after the markers. The last closing marker is used so a
// warning containing the begin token cannot truncate a valid payload.
func ExtractJSON(out string) (string, error) {
	i := strings.Index(out, JSONBegin)
	j := strings.LastIndex(out, JSONEnd)
	if i < 0 || j < 0 || j <= i+len(JSONBegin) {
		return "", fmt.Errorf("vmstat: missing JSON envelope in kernel output")
	}
	return strings.TrimSpace(out[i+len(JSONBegin) : j]), nil
}

func decode(out string, dst any) error {
	payload, err := ExtractJSON(out)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(payload), dst); err != nil {
		return fmt.Errorf("vmstat: decode result: %w", err)
	}
	return nil
}

func ParseSnapshot(out string) (Snapshot, error) {
	var v Snapshot
	err := decode(out, &v)
	return v, err
}

func ParseTail(out string) (TailResult, error) {
	var v TailResult
	err := decode(out, &v)
	return v, err
}

func ParseLines(out string) (LinesResult, error) {
	var v LinesResult
	err := decode(out, &v)
	return v, err
}

func ParseMatches(out string) (TextResult, error) {
	var v TextResult
	err := decode(out, &v)
	return v, err
}

func ParseFetch(out string) (FetchResult, error) {
	var v FetchResult
	err := decode(out, &v)
	return v, err
}
