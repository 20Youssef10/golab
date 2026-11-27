// Package compat compares the golab command/flag surface against a captured
// snapshot. It performs no network I/O: the snapshot is a file baked into the
// binary, explicitly labelled "self-snapshot" so it is never mistaken for a
// live upstream capture (which only a human can make).
package compat

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Snapshot is a command -> sorted flag-names map plus provenance. self-snapshot
// files only carry Source/Captured; upstream-fetched files additionally carry
// SourceURL/Ref/FetchedAt/ContentHash/Method so a report can name its origin
// without ever claiming a live probe.
type Snapshot struct {
	Kind        string              `json:"kind"`
	Scope       string              `json:"scope,omitempty"`
	Source      string              `json:"source"`
	Captured    string              `json:"captured"`
	SourceURL   string              `json:"source_url,omitempty"`
	Ref         string              `json:"ref,omitempty"`
	FetchedAt   string              `json:"fetched_at,omitempty"`
	ContentHash string              `json:"content_hash,omitempty"`
	Method      string              `json:"method,omitempty"`
	Commands    map[string][]string `json:"commands"`
}

//go:embed upstream.json
var embedded []byte

// Embedded decodes the baked-in snapshot.
func Embedded() (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(embedded, &s); err != nil {
		return Snapshot{}, fmt.Errorf("compat: decode embedded snapshot: %w", err)
	}
	return s, nil
}

// Report is the stable, machine-readable comparison result.
type Report struct {
	Schema           string              `json:"schema"`
	SnapshotKind     string              `json:"snapshot_kind"`
	SnapshotScope    string              `json:"snapshot_scope,omitempty"`
	SnapshotSource   string              `json:"snapshot_source"`
	SourceURL        string              `json:"source_url,omitempty"`
	Ref              string              `json:"ref,omitempty"`
	FetchedAt        string              `json:"fetched_at,omitempty"`
	ContentHash      string              `json:"content_hash,omitempty"`
	Method           string              `json:"method,omitempty"`
	Captured         string              `json:"captured"`
	Live             bool                `json:"live"`
	SnapshotCommands int                 `json:"snapshot_commands"`
	CurrentCommands  int                 `json:"current_commands"`
	AddedCommands    []string            `json:"added_commands"`
	MissingCommands  []string            `json:"missing_commands"`
	AddedFlags       map[string][]string `json:"added_flags"`
	MissingFlags     map[string][]string `json:"missing_flags"`
}

const schema = "golab/compat-report/1"

// Compare reports the deltas between the current command tree and a snapshot.
// Hidden commands/flags and cobra's generated `help` flag are ignored.
func Compare(root *cobra.Command, snap Snapshot) Report {
	rep := Report{
		Schema: schema, SnapshotKind: snap.Kind, SnapshotScope: snap.Scope,
		SnapshotSource: snap.Source, SourceURL: snap.SourceURL, Ref: snap.Ref,
		FetchedAt: snap.FetchedAt, ContentHash: snap.ContentHash, Method: snap.Method,
		Captured: snap.Captured, Live: false,
		AddedCommands: []string{}, MissingCommands: []string{},
		AddedFlags: map[string][]string{}, MissingFlags: map[string][]string{},
	}
	current := surface(root)
	rep.CurrentCommands = len(current)
	rep.SnapshotCommands = len(snap.Commands)

	for name, flags := range current {
		old, ok := snap.Commands[name]
		if !ok {
			rep.AddedCommands = append(rep.AddedCommands, name)
			continue
		}
		if d := difference(flags, old); len(d) > 0 {
			rep.AddedFlags[name] = d
		}
		if d := difference(old, flags); len(d) > 0 {
			rep.MissingFlags[name] = d
		}
	}
	for name := range snap.Commands {
		if _, ok := current[name]; !ok {
			rep.MissingCommands = append(rep.MissingCommands, name)
		}
	}
	sort.Strings(rep.AddedCommands)
	sort.Strings(rep.MissingCommands)
	return rep
}

func surface(root *cobra.Command) map[string][]string {
	out := map[string][]string{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden && cmd != root {
			return
		}
		name := cmd.CommandPath()
		seen := map[string]bool{}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden && f.Name != "help" {
				seen[f.Name] = true
			}
		})
		flags := make([]string, 0, len(seen))
		for f := range seen {
			flags = append(flags, f)
		}
		sort.Strings(flags)
		out[name] = flags
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	return out
}

func difference(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if !set[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
