// Package envlock stores the parts of a Colab VM environment that can be
// frozen and compared deterministically: interpreter version, `pip freeze`
// output, CUDA version and GPU name. Requirements are installed by the CLI's
// existing `uv pip install --system -r` path; they are deliberately not
// compared before installation (installation is what reconciles them).
//
// The probe output is untrusted kernel text. Only marker-delimited JSON is
// consumed, credential-bearing URL userinfo is scrubbed, and no probe value is
// ever spliced into Python source.
package envlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/20Youssef10/golab/internal/logx"
)

const (
	// Schema identifies a lockfile this build understands.
	Schema = "golab/envlock/1"
	// Marker prefixes the single JSON line emitted by ProbeCode.
	Marker = "__GOLAB_ENV_FREEZE__"
)

var (
	// ErrSchema means a lockfile was written by an incompatible version.
	ErrSchema = errors.New("envlock: unsupported schema")
	// ErrNoProbe means the kernel produced no parseable environment probe.
	ErrNoProbe = errors.New("envlock: no environment probe in kernel output")
	// ErrMismatch means live hardware/interpreter differs from the lockfile.
	ErrMismatch = errors.New("envlock: environment mismatch")
)

// Lockfile is the on-disk document. Field order is fixed so repeated freezes
// of the same VM produce byte-identical files.
type Lockfile struct {
	Schema       string   `json:"schema"`
	Python       string   `json:"python_version"`
	Requirements []string `json:"pip_freeze"`
	CUDA         string   `json:"cuda"`
	GPU          string   `json:"gpu"`
	Accelerator  string   `json:"accelerator"`
}

var urlUserInfoRE = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)

// scrubRequirement removes credential-bearing URL userinfo and applies the
// standard free-form redaction pass. A private dependency cannot be
// reinstalled from a scrubbed URL, which is the honest outcome: never persist
// a credential to a lockfile that is meant to be shared.
func scrubRequirement(s string) string {
	return logx.Redact(urlUserInfoRE.ReplaceAllString(strings.TrimSpace(s), "${1}REDACTED@"))
}

// normalize makes a lockfile canonical: sorted/deduplicated requirements and
// scrubbed values. It never mutates the caller's slices.
func normalize(l *Lockfile) {
	l.Schema = Schema
	l.Python = logx.Redact(strings.TrimSpace(l.Python))
	l.CUDA = logx.Redact(strings.TrimSpace(l.CUDA))
	l.GPU = logx.Redact(strings.TrimSpace(l.GPU))
	l.Accelerator = strings.TrimSpace(l.Accelerator)
	seen := make(map[string]bool, len(l.Requirements))
	out := make([]string, 0, len(l.Requirements))
	for _, r := range l.Requirements {
		r = scrubRequirement(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	l.Requirements = out
}

// Marshal writes a deterministic, newline-terminated lockfile.
func Marshal(l *Lockfile) ([]byte, error) {
	if l == nil {
		return nil, errors.New("envlock: nil lockfile")
	}
	c := *l
	c.Requirements = append([]string(nil), l.Requirements...)
	normalize(&c)
	b, err := json.MarshalIndent(&c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Parse reads a lockfile, rejecting unknown schemas before any VM allocation.
func Parse(data []byte) (*Lockfile, error) {
	var l Lockfile
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("envlock: decode lockfile: %w", err)
	}
	if l.Schema != Schema {
		// Never echo a user-supplied schema string into an error.
		return nil, fmt.Errorf("%w: expected %q", ErrSchema, Schema)
	}
	normalize(&l)
	return &l, nil
}

// ParseProbe extracts the marker-delimited JSON object from kernel output.
// Unrelated chatter (including a marker followed by non-JSON) is ignored.
func ParseProbe(text string) (*Lockfile, error) {
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, Marker)
		if i < 0 {
			continue
		}
		raw := strings.TrimSpace(line[i+len(Marker):])
		if raw == "" || raw[0] != '{' {
			continue
		}
		var l Lockfile
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			continue
		}
		l.Schema = Schema // the probe is ours; do not trust a schema field
		normalize(&l)
		return &l, nil
	}
	return nil, ErrNoProbe
}

// RequirementsBytes returns the pip freeze list as a requirements file body.
func RequirementsBytes(l *Lockfile) []byte {
	if l == nil || len(l.Requirements) == 0 {
		return nil
	}
	reqs := append([]string(nil), l.Requirements...)
	sort.Strings(reqs)
	return []byte(strings.Join(reqs, "\n") + "\n")
}

// Compare reports interpreter, CUDA, GPU and accelerator drift. Requirements
// are excluded: --lockfile installs them rather than requiring them upfront.
func Compare(current, locked *Lockfile) error {
	if current == nil || locked == nil {
		return fmt.Errorf("%w: missing lockfile or probe", ErrMismatch)
	}
	var parts []string
	mismatch := func(field, want, got string) {
		parts = append(parts, fmt.Sprintf("%s: recorded %q, current %q", field,
			logx.Redact(want), logx.Redact(got)))
	}
	if current.Python != locked.Python {
		mismatch("python_version", locked.Python, current.Python)
	}
	if current.CUDA != locked.CUDA {
		mismatch("cuda", locked.CUDA, current.CUDA)
	}
	if current.GPU != locked.GPU {
		mismatch("gpu", locked.GPU, current.GPU)
	}
	if locked.Accelerator != "" && current.Accelerator != locked.Accelerator {
		mismatch("accelerator", locked.Accelerator, current.Accelerator)
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMismatch, strings.Join(parts, "; "))
}

// ProbeCode returns static Python source that prints one marker-delimited JSON
// object. It contains no interpolated user text, so it cannot execute injected
// strings; every value is produced by the VM itself.
func ProbeCode() string {
	return `import sys, json, subprocess
def _golab_env_probe():
    py = sys.version.split()[0]
    try:
        reqs = subprocess.check_output(
            [sys.executable, '-m', 'pip', 'freeze'], text=True).splitlines()
    except Exception:
        reqs = []
    cuda, gpu = '', ''
    try:
        import torch
        cuda = str(torch.version.cuda or '')
        if torch.cuda.is_available():
            gpu = str(torch.cuda.get_device_name(0))
    except Exception:
        pass
    if not gpu:
        try:
            out = subprocess.check_output(
                ['nvidia-smi', '--query-gpu=name', '--format=csv,noheader'],
                text=True, stderr=subprocess.DEVNULL)
            gpu = out.splitlines()[0].strip() if out.strip() else ''
        except Exception:
            pass
    obj = {'python_version': py,
           'pip_freeze': sorted(set(reqs)),
           'cuda': cuda, 'gpu': gpu, 'accelerator': ''}
    print('` + Marker + `' + json.dumps(obj, sort_keys=True, separators=(',', ':')))
_golab_env_probe()
`
}
