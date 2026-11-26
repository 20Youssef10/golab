// Package cost persists observed compute-unit samples from GET /tun/m/ccu-info.
//
// It is strictly CU-only: no prices, no currency symbols and no dollar
// estimates are ever produced. Samples are advisory observations, not a
// billing source of truth.
package cost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/20Youssef10/golab/internal/platform"
)

// DefaultMaxBytes bounds the on-disk ledger so a long-lived cache cannot grow
// without limit. Oversized ledgers are trimmed from the oldest line.
const DefaultMaxBytes int64 = 1 << 20

// Sample is one ccu-info observation. Source is one of usage, budget or guard.
type Sample struct {
	At          time.Time `json:"at"`
	Source      string    `json:"source"`
	Balance     float64   `json:"balance"`
	RateHourly  float64   `json:"rate_hourly"`
	Assignments int       `json:"assignments"`
}

// Ledger is an append-only, size-bounded JSONL file with 0600 permissions.
type Ledger struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	now      func() time.Time
}

// New creates a ledger at path. The directory is created with 0700.
func New(path string) *Ledger {
	return &Ledger{path: path, maxBytes: DefaultMaxBytes, now: time.Now}
}

// SetClock overrides the clock (test seam; production uses time.Now).
func (l *Ledger) SetClock(fn func() time.Time) {
	if fn != nil {
		l.now = fn
	}
}

// SetMaxBytes overrides the size bound.
func (l *Ledger) SetMaxBytes(n int64) {
	if n > 0 {
		l.maxBytes = n
	}
}

// Append writes one JSON line, then trims the oldest lines if the file has
// grown past the bound. The rewrite is atomic (temp file + rename).
func (l *Ledger) Append(s Sample) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s.At.IsZero() {
		s.At = l.now().UTC()
	}
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	existing, err := os.ReadFile(l.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	data := append(append([]byte(nil), existing...), line...)
	data = trimToBound(data, l.maxBytes)

	f, err := os.CreateTemp(dir, ".ccu-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	return platform.MakePrivate(l.path)
}

// trimToBound drops whole lines from the front until the payload fits. A
// single line larger than the bound is kept: dropping every sample would make
// the report useless.
func trimToBound(data []byte, max int64) []byte {
	if max <= 0 || int64(len(data)) <= max {
		return data
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 1 {
		out := bytes.Join(lines, []byte{'\n'})
		if int64(len(out)+1) <= max {
			return append(out, '\n')
		}
		lines = lines[1:]
	}
	out := bytes.Join(lines, []byte{'\n'})
	return append(out, '\n')
}

// Read parses a ledger, skipping malformed lines (a cache is never fatal).
func Read(path string) ([]Sample, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Sample
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var s Sample
		if err := json.Unmarshal(line, &s); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

// DefaultPath returns $XDG_CACHE_HOME/colab-cli/ccu.jsonl.
func DefaultPath() (string, error) {
	dir, err := platform.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ccu.jsonl"), nil
}

// Window is a one-hour bucket of observed samples.
type Window struct {
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Samples    int       `json:"samples"`
	Balance    float64   `json:"balance"`
	RateHourly float64   `json:"rate_hourly"`
}

// Report is the CU-only summary returned by `golab cost`.
type Report struct {
	Since       time.Time `json:"since"`
	ObservedAt  time.Time `json:"observed_at"`
	SampleCount int       `json:"sample_count"`
	Balance     float64   `json:"balance"`
	RateHourly  float64   `json:"rate_hourly"`
	Unit        string    `json:"unit"`
	Windows     []Window  `json:"windows"`
}

// ReportOf filters samples to [since, now] and aggregates them per hour.
// Balance is the last observed value; rate is the mean observed rate.
func ReportOf(samples []Sample, since, now time.Time) Report {
	r := Report{Since: since.UTC(), ObservedAt: now.UTC(), Unit: "compute_units", Windows: []Window{}}
	in := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.At.Before(since) || s.At.After(now) {
			continue
		}
		in = append(in, s)
	}
	sort.Slice(in, func(i, j int) bool { return in[i].At.Before(in[j].At) })
	r.SampleCount = len(in)
	if len(in) == 0 {
		return r
	}
	r.Balance = in[len(in)-1].Balance
	var rateSum float64
	buckets := make(map[time.Time]*Window)
	for _, s := range in {
		rateSum += s.RateHourly
		start := s.At.UTC().Truncate(time.Hour)
		w, ok := buckets[start]
		if !ok {
			w = &Window{Start: start, End: start.Add(time.Hour)}
			buckets[start] = w
		}
		w.Samples++
		w.Balance = s.Balance
		w.RateHourly += s.RateHourly
	}
	r.RateHourly = rateSum / float64(len(in))
	for _, w := range buckets {
		w.RateHourly /= float64(w.Samples)
		r.Windows = append(r.Windows, *w)
	}
	sort.Slice(r.Windows, func(i, j int) bool { return r.Windows[i].Start.Before(r.Windows[j].Start) })
	return r
}
