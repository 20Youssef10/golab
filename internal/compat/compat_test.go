package compat

// The snapshot is honest about its provenance: it is captured from this repo's
// own command tree, NOT from a live upstream CLI. Treat it as a regression
// baseline for the Go surface, not as a claim of upstream parity.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func testRoot() *cobra.Command {
	root := &cobra.Command{Use: "golab"}
	doctor := &cobra.Command{Use: "doctor"}
	doctor.Flags().Bool("json", false, "")
	doctor.Flags().Bool("live", false, "")
	doctor.Flags().String("fixtures", "", "")
	root.AddCommand(doctor)
	root.AddCommand(&cobra.Command{Use: "newcmd"})
	root.PersistentFlags().String("config", "", "")
	return root
}

func TestCompareReportsAddedAndMissing(t *testing.T) {
	snap := Snapshot{
		Kind:   "self-snapshot",
		Source: "repo command tree",
		Commands: map[string][]string{
			"golab doctor": {"json"},
			"golab gone":   {},
		},
	}
	rep := Compare(testRoot(), snap)
	if len(rep.AddedCommands) != 2 || rep.AddedCommands[0] != "golab" ||
		rep.AddedCommands[1] != "golab newcmd" {
		t.Fatalf("added commands = %v", rep.AddedCommands)
	}
	if len(rep.MissingCommands) != 1 || rep.MissingCommands[0] != "golab gone" {
		t.Fatalf("missing commands = %v", rep.MissingCommands)
	}
	if len(rep.AddedFlags["golab doctor"]) != 2 || rep.AddedFlags["golab doctor"][0] != "fixtures" ||
		rep.AddedFlags["golab doctor"][1] != "live" {
		t.Fatalf("added flags = %v", rep.AddedFlags)
	}
	if len(rep.MissingFlags) != 0 {
		t.Fatalf("missing flags = %v", rep.MissingFlags)
	}
	if rep.SnapshotKind != "self-snapshot" || rep.Live != false {
		t.Fatalf("honesty labels lost: %+v", rep)
	}
}

func TestEmbeddedSnapshotIsHonestAndMatchesTestdata(t *testing.T) {
	snap, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Kind != "self-snapshot" || snap.Captured == "" || snap.Source == "" {
		t.Fatalf("snapshot lacks provenance: %+v", snap)
	}
	if len(snap.Commands) == 0 {
		t.Fatal("snapshot has no commands")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "compat", "upstream.json"))
	if err != nil {
		t.Fatal(err)
	}
	var testdataSnap Snapshot
	if err := json.Unmarshal(raw, &testdataSnap); err != nil {
		t.Fatal(err)
	}
	if testdataSnap.Kind != snap.Kind || testdataSnap.Captured != snap.Captured ||
		len(testdataSnap.Commands) != len(snap.Commands) {
		t.Fatal("embedded snapshot and testdata snapshot drifted")
	}
	rep := Compare(testRoot(), snap)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty report")
	}
}
