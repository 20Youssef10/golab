package bundle

import (
	"encoding/json"
	"sort"
)

// Environment is the VM probe recorded alongside a run bundle. It is stored
// in the manifest (and covered by its ID) so `bundle verify --env` can recheck
// it in a fresh VM. It is never presented as proof that a past run succeeded.
type Environment struct {
	PythonVersion string   `json:"python_version"`
	PipFreeze     []string `json:"pip_freeze"`
	CUDA          string   `json:"cuda"`
	GPU           string   `json:"gpu"`
	Accelerator   string   `json:"accelerator"`
}

// NewRun builds the provenance manifest written by `golab run --bundle`. It
// differs from `bundle record` only in provenance attribution: the environment
// was observed during a real run, not supplied by hand. Both remain recorded
// provenance, never a verified replay or a cache key.
func NewRun(in Input) (*Manifest, error) {
	if in.Environment != nil {
		env := *in.Environment
		env.PipFreeze = append([]string(nil), in.Environment.PipFreeze...)
		sort.Strings(env.PipFreeze)
		in.Environment = &env
	}
	m, err := New(in)
	if err != nil {
		return nil, err
	}
	m.Provenance.RecordedBy = "golab run --bundle"
	b, err := json.Marshal(m.canonical())
	if err != nil {
		return nil, err
	}
	m.ID = Hash(b)
	return m, nil
}
