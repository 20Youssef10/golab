package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/logx"
	"github.com/20Youssef10/golab/internal/nb"
	"github.com/20Youssef10/golab/internal/platform"
)

const syncManifestSchema = "golab/sync/1"

type syncFile struct {
	Path string // slash-separated relative path
	Hash string
	Mod  time.Time
}

type syncManifest struct {
	Schema  string            `json:"schema"`
	Session string            `json:"session"`
	Remote  string            `json:"remote"`
	Files   map[string]string `json:"files"`
}

type syncAction struct {
	Kind   string // upload | download | delete-remote | delete-local | skip
	Path   string
	Reason string
}

// NewSyncCommand builds the one-shot file synchroniser. There is deliberately
// no watch mode: the command authenticates, works and exits.
func NewSyncCommand() *cobra.Command {
	var direction, session string
	var dryRun, apply, allowDelete, diff, includeOutputs bool
	cmd := &cobra.Command{
		Use:   "sync LOCAL REMOTE",
		Short: "One-shot bidirectional file sync with conflict detection",
		Long: "Sync a local directory with a VM directory. Remote paths use Linux " +
			"path semantics; local paths use host semantics. Dry-run is the default; " +
			"pass --apply to write. Conflicts fail instead of overwriting. " +
			"--diff compares two notebooks with outputs stripped by default.",
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch direction {
			case "up", "down", "both":
			default:
				return usagef("[colab] sync: --direction must be up, down or both")
			}
			if err := validateSessionName(session); err != nil {
				return err
			}
			remote, err := cleanRemotePath(args[1])
			if err != nil {
				return usagef("[colab] sync: remote path: %v", err)
			}
			local := filepath.Clean(args[0])
			if local == "" || local == "." {
				return usagef("[colab] sync: LOCAL must be a directory")
			}
			d := depsOf(cmd)
			s, err := d.ResolveSession(cmd.Context(), session)
			if err != nil {
				return err
			}
			client, err := d.ContentsFor(s)
			if err != nil {
				return err
			}
			if diff {
				return syncNotebookDiff(cmd.Context(), d, client, local, remote, includeOutputs)
			}
			return runSync(cmd.Context(), d, client, s.Name, local, remote,
				syncOptions{direction: direction, dryRun: dryRun && !apply,
					allowDelete: allowDelete})
		},
	}
	cmd.Flags().StringVarP(&session, "session", "s", "", "Session name")
	cmd.Flags().StringVar(&direction, "direction", "both", "Sync direction: up, down or both")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Print the plan without writing (default)")
	cmd.Flags().BoolVar(&apply, "apply", false, "Perform the planned changes")
	cmd.Flags().BoolVar(&allowDelete, "delete", false, "Allow deletions of files missing on the other side")
	cmd.Flags().BoolVar(&diff, "diff", false, "Compare two notebooks (LOCAL and REMOTE are files)")
	cmd.Flags().BoolVar(&includeOutputs, "include-outputs", false, "Keep notebook outputs and execution counts in --diff")
	return cmd
}

// RegisterSyncCommands adds `sync` to the root command tree.
func RegisterSyncCommands(root *cobra.Command) { root.AddCommand(NewSyncCommand()) }

type syncOptions struct {
	direction   string
	dryRun      bool
	allowDelete bool
}

func cleanRemotePath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	clean := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if clean == "." || clean == "/" || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("path %q escapes the VM workspace", p)
	}
	return clean, nil
}

func collectLocal(ctx context.Context, root string) (map[string]syncFile, error) {
	out := map[string]syncFile{}
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		// #nosec G122 -- p comes from filepath.WalkDir on a validated local
		// root; symlinks are skipped above and rel is containment-checked below.
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		out[rel] = syncFile{Path: rel, Hash: sha256Hex(b)}
		return nil
	})
	return out, err
}

func collectRemote(ctx context.Context, client contents.Client, remote string) (map[string]syncFile, error) {
	model, err := client.List(ctx, remote)
	if err != nil {
		return nil, err
	}
	out := map[string]syncFile{}
	for _, entry := range model.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			continue
		}
		child, err := cleanRemotePath(path.Join(remote, name))
		if err != nil {
			continue
		}
		if entry.Type == "directory" {
			continue
		}
		read, err := client.Read(ctx, child)
		if err != nil {
			return nil, err
		}
		rel := strings.TrimPrefix(child, remote+"/")
		out[rel] = syncFile{Path: rel, Hash: sha256Hex(read.Content)}
	}
	return out, nil
}

func syncManifestPath(session, remote string) (string, error) {
	dir, err := platform.CacheDir()
	if err != nil {
		return "", err
	}
	key := sha256Hex([]byte(session + "\x00" + remote))
	return filepath.Join(dir, "sync-"+key+".json"), nil
}

func loadSyncManifest(ctx context.Context, path string) (syncManifest, error) {
	empty := syncManifest{Schema: syncManifestSchema, Files: map[string]string{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	var m syncManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return empty, fmt.Errorf("%w: %s", stateCorrupt{}, logx.Redact(err.Error()))
	}
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m, nil
}

// stateCorrupt is a local typed error; it never carries file contents.
type stateCorrupt struct{}

func (stateCorrupt) Error() string { return "corrupt sync manifest" }

func saveSyncManifest(ctx context.Context, path string, m syncManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func planSync(local, remote map[string]syncFile, base map[string]string, opts syncOptions) ([]syncAction, bool) {
	keys := map[string]bool{}
	for k := range local {
		keys[k] = true
	}
	for k := range remote {
		keys[k] = true
	}
	for k := range base {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	actions := []syncAction{}
	conflict := false
	for _, k := range ordered {
		l, lok := local[k]
		r, rok := remote[k]
		old := base[k]
		switch {
		case lok && rok:
			switch {
			case old != "" && l.Hash != old && r.Hash != old:
				conflict = true
				actions = append(actions, syncAction{Kind: "conflict", Path: k,
					Reason: "both sides changed since the last sync (local " + l.Hash[:8] + ", remote " + r.Hash[:8] + ")"})
			case l.Hash == r.Hash:
				actions = append(actions, syncAction{Kind: "skip", Path: k, Reason: "already in sync"})
			case opts.direction == "up" || (opts.direction == "both" && old != "" && r.Hash == old):
				actions = append(actions, syncAction{Kind: "upload", Path: k})
			case opts.direction == "down" || (opts.direction == "both" && old != "" && l.Hash == old):
				actions = append(actions, syncAction{Kind: "download", Path: k})
			default:
				conflict = true
				actions = append(actions, syncAction{Kind: "conflict", Path: k, Reason: "both sides differ with no baseline"})
			}
		case lok && !rok:
			if old != "" && l.Hash == old {
				// No local change since the baseline; a missing remote entry
				// is treated as a listing artifact, never re-uploaded.
				actions = append(actions, syncAction{Kind: "skip", Path: k, Reason: "unchanged since last sync"})
			} else if opts.direction == "down" {
				if opts.allowDelete {
					actions = append(actions, syncAction{Kind: "delete-local", Path: k, Reason: "not present remotely"})
				} else {
					actions = append(actions, syncAction{Kind: "skip", Path: k, Reason: "not present remotely; pass --delete to remove"})
				}
			} else {
				actions = append(actions, syncAction{Kind: "upload", Path: k})
			}
		case !lok && rok:
			if old != "" && r.Hash == old {
				actions = append(actions, syncAction{Kind: "skip", Path: k, Reason: "unchanged since last sync"})
			} else if opts.direction == "up" {
				if opts.allowDelete {
					actions = append(actions, syncAction{Kind: "delete-remote", Path: k, Reason: "not present locally"})
				} else {
					actions = append(actions, syncAction{Kind: "skip", Path: k, Reason: "not present locally; pass --delete to remove"})
				}
			} else {
				actions = append(actions, syncAction{Kind: "download", Path: k})
			}
		}
	}
	return actions, conflict
}

func runSync(ctx context.Context, d *app.Deps, client contents.Client,
	session, localRoot, remote string, opts syncOptions) error {
	manifestPath, err := syncManifestPath(session, remote)
	if err != nil {
		return err
	}
	manifest, err := loadSyncManifest(ctx, manifestPath)
	if err != nil {
		return exitf(1, "[colab] sync: %s", logx.Redact(err.Error()))
	}
	localFiles, err := collectLocal(ctx, localRoot)
	if err != nil {
		return err
	}
	remoteFiles, err := collectRemote(ctx, client, remote)
	if err != nil {
		return err
	}
	actions, conflict := planSync(localFiles, remoteFiles, manifest.Files, opts)
	for _, a := range actions {
		if opts.dryRun {
			fmt.Fprintf(d.Stdout, "[sync] would %s %s (%s)\n", a.Kind, a.Path, a.Reason)
		} else {
			fmt.Fprintf(d.Stdout, "[sync] %s %s\n", a.Kind, a.Path)
		}
	}
	if conflict {
		return exitf(1, "[colab] sync: conflicts detected; nothing was changed")
	}
	if opts.dryRun {
		return nil
	}
	newHashes := map[string]string{}
	for k, v := range manifest.Files {
		newHashes[k] = v
	}
	if err := applySync(ctx, client, localRoot, remote, actions, localFiles, remoteFiles, newHashes); err != nil {
		return exitCausef(1, err, "[colab] sync: %s", logx.Redact(err.Error()))
	}
	manifest.Schema, manifest.Session, manifest.Remote, manifest.Files =
		syncManifestSchema, session, remote, newHashes
	return saveSyncManifest(ctx, manifestPath, manifest)
}

// syncPathEscape is the typed rejection for an action whose local target
// resolves outside the sync root. It never carries file contents.
type syncPathEscape struct {
	root string
	path string
}

func (e *syncPathEscape) Error() string {
	return fmt.Sprintf("path %q escapes local root %q", e.path, e.root)
}

// syncLocalTarget resolves a slash-separated action path against localRoot and
// asserts the result stays inside it. Checking the Rel result catches both a
// '..' escape and an absolute target; an absolute input is rejected up front
// because filepath.Join would otherwise nest it under the root silently.
func syncLocalTarget(localRoot, slashPath string) (string, error) {
	relPath := filepath.FromSlash(slashPath)
	if filepath.IsAbs(relPath) {
		return "", &syncPathEscape{root: localRoot, path: slashPath}
	}
	target := filepath.Join(localRoot, relPath)
	rel, err := filepath.Rel(localRoot, target)
	if err != nil {
		return "", &syncPathEscape{root: localRoot, path: slashPath}
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", &syncPathEscape{root: localRoot, path: slashPath}
	}
	return target, nil
}

// applySync performs the planned local and remote mutations. It is separate
// from planning so the local containment invariant (§4.18) is directly
// testable with crafted actions: a tampered manifest or state entry must not
// be able to read, write or delete outside localRoot.
func applySync(ctx context.Context, client contents.Client, localRoot, remote string,
	actions []syncAction, localFiles, remoteFiles map[string]syncFile,
	newHashes map[string]string) error {
	for _, a := range actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		remotePath := path.Join(remote, a.Path)
		switch a.Kind {
		case "upload":
			localPath, err := syncLocalTarget(localRoot, a.Path)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			if err := client.Write(ctx, remotePath, b, 0); err != nil {
				return err
			}
			newHashes[a.Path] = sha256Hex(b)
		case "download":
			localPath, err := syncLocalTarget(localRoot, a.Path)
			if err != nil {
				return err
			}
			model, err := client.Read(ctx, remotePath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(localPath, model.Content, 0o600); err != nil {
				return err
			}
			newHashes[a.Path] = sha256Hex(model.Content)
		case "delete-remote":
			if err := client.Delete(ctx, remotePath); err != nil {
				return err
			}
			delete(newHashes, a.Path)
		case "delete-local":
			localPath, err := syncLocalTarget(localRoot, a.Path)
			if err != nil {
				return err
			}
			if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(newHashes, a.Path)
		case "skip":
			// Only record a baseline when both sides agree. A one-sided skip
			// must not become a baseline that hides a future deletion.
			if l, ok := localFiles[a.Path]; ok {
				if r, ok := remoteFiles[a.Path]; ok && l.Hash == r.Hash {
					newHashes[a.Path] = l.Hash
				}
			}
		}
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// syncNotebookDiff canonicalises notebooks through internal/nb and prints a
// compact unified diff. Outputs and execution counts are stripped by default
// so a notebook diff is reviewable in git.
func syncNotebookDiff(ctx context.Context, d *app.Deps, client contents.Client,
	local, remote string, includeOutputs bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	localBytes, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	model, err := client.Read(ctx, remote)
	if err != nil {
		return err
	}
	a, err := canonicalNotebook(localBytes, includeOutputs)
	if err != nil {
		return err
	}
	b, err := canonicalNotebook(model.Content, includeOutputs)
	if err != nil {
		return err
	}
	diff := unifiedDiff(a, b, filepath.Base(local), path.Base(remote))
	if diff != "" {
		fmt.Fprint(d.Stdout, diff)
	}
	return nil
}

var volatileNotebookMetadata = map[string]bool{
	"execution": true, "executionInfo": true, "collapsed": true,
	"scrolled": true, "colab": true, "widgets": true,
}

func canonicalNotebook(raw []byte, includeOutputs bool) (string, error) {
	n, err := nb.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("sync: invalid notebook: %w", err)
	}
	for i := range n.Cells {
		c := &n.Cells[i]
		for k := range volatileNotebookMetadata {
			delete(c.Metadata, k)
		}
		if !includeOutputs {
			c.Outputs = nil
			c.ExecutionCount = nil
		}
	}
	for k := range volatileNotebookMetadata {
		delete(n.Metadata, k)
	}
	b, err := nb.Marshal(n)
	if err != nil {
		return "", err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		return "", err
	}
	return pretty.String(), nil
}

// unifiedDiff emits one hunk with three lines of context. It is intentionally
// simple and deterministic; notebooks are small enough that a full LCS is not
// needed for the "did the code change?" question.
func unifiedDiff(a, b, labelA, labelB string) string {
	if a == b {
		return ""
	}
	al := strings.Split(strings.TrimSuffix(a, "\n"), "\n")
	bl := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	prefix := 0
	for prefix < len(al) && prefix < len(bl) && al[prefix] == bl[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(al)-prefix && suffix < len(bl)-prefix &&
		al[len(al)-1-suffix] == bl[len(bl)-1-suffix] {
		suffix++
	}
	removed, added := al[prefix:len(al)-suffix], bl[prefix:len(bl)-suffix]
	start := prefix - 3
	if start < 0 {
		start = 0
	}
	before, after := al[start:prefix], al[len(al)-suffix:]
	if len(after) > 3 {
		after = after[:3]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n",
		labelA, labelB, start+1, len(before)+len(removed)+len(after),
		start+1, len(before)+len(added)+len(after))
	for _, line := range before {
		out.WriteString(" " + line + "\n")
	}
	for _, line := range removed {
		out.WriteString("-" + line + "\n")
	}
	for _, line := range added {
		out.WriteString("+" + line + "\n")
	}
	for _, line := range after {
		out.WriteString(" " + line + "\n")
	}
	return out.String()
}
