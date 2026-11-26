package bundle

import (
	"encoding/json"
	"strings"
	"testing"
)

// A run-produced manifest must say who recorded it and must keep the honesty
// note: the environment is recorded, never presented as a verified replay.
// The embedded probe lets `bundle verify --env` recheck it in a FRESH VM.
func TestNewRunRecordsEnvironmentAndHonesty(t *testing.T) {
	env := &Environment{
		PythonVersion: "3.11.9",
		PipFreeze:     []string{"numpy==1.26.0", "torch==2.1.0"},
		CUDA:          "12.1",
		GPU:           "Tesla T4",
		Accelerator:   "T4",
	}
	m, err := NewRun(Input{
		Files:           []File{{Path: "train.py", Content: []byte("print(1)\n")}},
		Env:             map[string]string{"python_version": "3.11.9"},
		Accelerator:     "T4",
		Variant:         "DEFAULT",
		Shape:           "STANDARD",
		UpstreamVersion: "0.8.x+HEAD",
		GolabVersion:    "0.2.0",
		Environment:     env,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Provenance.RecordedBy != "golab run --bundle" {
		t.Fatalf("recorded_by = %q", m.Provenance.RecordedBy)
	}
	if !strings.Contains(m.Provenance.Note, "recorded provenance") ||
		!strings.Contains(m.Provenance.Note, "not a verified replay") {
		t.Fatalf("dishonest provenance note: %q", m.Provenance.Note)
	}
	if m.Environment == nil || m.Environment.PythonVersion != "3.11.9" ||
		len(m.Environment.PipFreeze) != 2 {
		t.Fatalf("environment probe not recorded: %+v", m.Environment)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Verify(b)
	if err != nil {
		t.Fatalf("manifest does not verify: %v", err)
	}
	if back.ID != m.ID || back.Environment == nil || back.Environment.GPU != "Tesla T4" {
		t.Fatalf("round trip = %+v", back)
	}
	// Tampering with the embedded probe must invalidate the ID.
	var obj map[string]any
	_ = json.Unmarshal(b, &obj)
	obj["environment"].(map[string]any)["gpu"] = "A100"
	tampered, _ := json.Marshal(obj)
	if _, err := Verify(tampered); err == nil {
		t.Fatal("tampered environment probe still verified")
	}
}
