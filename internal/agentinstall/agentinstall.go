// Package agentinstall writes MCP server configuration and portable Agent
// Skills into AI-agent config directories. It is a pure library: every root,
// filesystem dependency and durability seam is injected by the caller, so no
// runtime-mutable state lives at package scope and tests use t.TempDir().
package agentinstall

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Agent is an allowlisted MCP-config target. Unknown names are rejected; no
// caller-supplied string is ever joined onto a home directory without passing
// this allowlist first (traversal defense).
type Agent string

const (
	AgentOpenCode   Agent = "opencode"
	AgentClaudeCode Agent = "claude-code"
	AgentCursor     Agent = "cursor"
	AgentGemini     Agent = "gemini"
	AgentCodex      Agent = "codex"
	AgentVSCode     Agent = "vscode"
)

// AllAgents is the deterministic expansion of ["all"].
var AllAgents = []Agent{
	AgentOpenCode, AgentClaudeCode, AgentCursor, AgentGemini, AgentCodex, AgentVSCode,
}

// SkillNames are the only two skill trees this installer may copy. Sorted so
// the copy order and the reported targets are deterministic.
var SkillNames = []string{"colab-mcp", "golab-operations"}

// Skill targets, beyond the five MCP agent names.
const (
	TargetUnified = "unified"
	TargetProject = "project"
)

// allSkillTargets is the deterministic expansion of ["all"].
var allSkillTargets = []string{
	TargetUnified, string(AgentClaudeCode), string(AgentOpenCode), string(AgentGemini),
	string(AgentCursor), TargetProject,
}

// MCPRequest is the validated input to InstallMCP. A nil DryRun means dry-run
// (preview) mode, matching the MCP tool default.
type MCPRequest struct {
	Agents    []string // empty or ["all"] expands to every agent
	Transport string   // "" -> stdio; "http"
	Addr      string   // HTTP listen address when Transport is http
	GolabBin  string   // path to golab-mcp; made absolute before writing
	Mode      string   // "" -> auto; "auto" writes, "manual" only returns snippets
	DryRun    *bool
}

// SkillsRequest is the validated input to InstallSkills. Empty Targets means
// the unified directory; ["all"] means every target.
type SkillsRequest struct {
	Targets []string
	DryRun  *bool
}

// TargetResult is the per-target outcome reported to the caller.
type TargetResult struct {
	Target  string `json:"target"`
	Path    string `json:"path"`
	Outcome string `json:"outcome"` // snippet | would-write | written | error
	Snippet string `json:"snippet,omitempty"`
	Backup  string `json:"backup,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Result is the structured output shared by both installers.
type Result struct {
	Kind    string         `json:"kind"` // mcp | skills
	Mode    string         `json:"mode,omitempty"`
	DryRun  bool           `json:"dry_run"`
	Targets []TargetResult `json:"targets"`
}

// Installer carries every injected dependency. Home and Project are the only
// roots used to build paths; Skills is the embedded (or fake) skill tree FS;
// DirSync is an optional durability seam for tests.
type Installer struct {
	Home    string
	Project string
	Skills  fs.FS

	DirSync func(string) error
}

// New returns an installer rooted at home (for ~/.config and friends) and
// project (for .vscode/mcp.json and the project-local .agents/skills).
func New(home, project string, skills fs.FS) *Installer {
	return &Installer{Home: home, Project: project, Skills: skills}
}

//go:embed skills
var embeddedSkills embed.FS

// SkillsFS returns an immutable FS rooted directly at the two skill trees, so
// callers only ever see "colab-mcp/..." and "golab-operations/...".
func SkillsFS() fs.FS {
	sub, err := fs.Sub(embeddedSkills, "skills")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	return sub
}

func readEmbedded(name string) ([]byte, error) { return fs.ReadFile(SkillsFS(), name) }

// ---------- MCP config installation ----------

// InstallMCP writes (or previews) the golab MCP entry for the requested
// agents. No network access and no secrets: every entry uses an empty env.
func (i *Installer) InstallMCP(req MCPRequest) (Result, error) {
	agents, err := resolveAgents(req.Agents)
	if err != nil {
		return Result{}, err
	}
	bin, err := absoluteBin(req.GolabBin)
	if err != nil {
		return Result{}, err
	}
	transport := req.Transport
	if transport == "" {
		transport = "stdio"
	}
	if transport != "stdio" && transport != "http" {
		return Result{}, fmt.Errorf("transport must be stdio or http, got %q", transport)
	}
	mode := strings.ToLower(req.Mode)
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "manual" {
		return Result{}, fmt.Errorf("mode must be auto or manual, got %q", req.Mode)
	}
	dry := req.DryRun == nil || *req.DryRun
	res := Result{Kind: "mcp", Mode: mode, DryRun: dry, Targets: make([]TargetResult, 0, len(agents))}

	for _, agent := range agents {
		p := i.mcpPath(agent)
		entry := buildEntry(agent, bin, transport, req.Addr)
		tr := TargetResult{Target: string(agent), Path: p}
		switch {
		case mode == "manual":
			tr.Outcome = "snippet"
			tr.Snippet = snippetFor(agent, entry)
		case dry:
			tr.Outcome = "would-write"
		default:
			data, err := i.renderConfig(agent, p, entry)
			if err != nil {
				tr.Outcome, tr.Error = "error", redactError(err)
				res.Targets = append(res.Targets, tr)
				continue
			}
			backup, err := atomicWrite(p, data, i.DirSync)
			if err != nil {
				tr.Outcome, tr.Error = "error", redactError(err)
				res.Targets = append(res.Targets, tr)
				continue
			}
			tr.Outcome, tr.Backup = "written", backup
		}
		res.Targets = append(res.Targets, tr)
	}
	return res, nil
}

func (i *Installer) mcpPath(agent Agent) string {
	switch agent {
	case AgentOpenCode:
		return filepath.Join(i.Home, ".config", "opencode", "opencode.json")
	case AgentClaudeCode:
		return filepath.Join(i.Home, ".claude", "settings.json")
	case AgentCursor:
		return filepath.Join(i.Home, ".cursor", "mcp.json")
	case AgentGemini:
		return filepath.Join(i.Home, ".gemini", "settings.json")
	case AgentCodex:
		return filepath.Join(i.Home, ".codex", "config.toml")
	case AgentVSCode:
		return filepath.Join(i.Project, ".vscode", "mcp.json")
	default:
		return ""
	}
}

func resolveAgents(agents []string) ([]Agent, error) {
	if len(agents) == 0 {
		return append([]Agent(nil), AllAgents...), nil
	}
	var out []Agent
	seen := map[Agent]bool{}
	for _, raw := range agents {
		if raw == "all" {
			for _, a := range AllAgents {
				if !seen[a] {
					seen[a] = true
					out = append(out, a)
				}
			}
			continue
		}
		if strings.ContainsAny(raw, "/\\") || strings.Contains(raw, "..") || filepath.IsAbs(raw) {
			return nil, fmt.Errorf("invalid agent %q: use one of the supported agent names", raw)
		}
		a := Agent(strings.ToLower(raw))
		known := false
		for _, cand := range AllAgents {
			if cand == a {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown agent %q", raw)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no agents selected")
	}
	return out, nil
}

func absoluteBin(bin string) (string, error) {
	if strings.TrimSpace(bin) == "" {
		return "", errors.New("golab_bin is required: supply the absolute path to golab-mcp")
	}
	if filepath.IsAbs(bin) {
		return bin, nil
	}
	return filepath.Abs(bin)
}

func transportArgs(transport, addr string) []string {
	if transport == "http" {
		args := []string{"--transport", "http"}
		if addr != "" {
			args = append(args, "--addr", addr)
		}
		return args
	}
	return []string{"--transport", "stdio"}
}

// buildEntry returns the exact config fragment written for one agent. All env
// maps are literal empty maps; no credential is ever accepted or stored here.
func buildEntry(agent Agent, bin, transport, addr string) any {
	args := transportArgs(transport, addr)
	if agent == AgentOpenCode {
		// stdio is the default, so only a non-default transport needs flags.
		command := []string{bin}
		if transport == "http" {
			command = append(command, args...)
		}
		return struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Environment map[string]string `json:"environment"`
		}{Type: "local", Command: command, Environment: map[string]string{}}
	}
	return struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}{Command: bin, Args: args, Env: map[string]string{}}
}

func entryPath(agent Agent) []string {
	switch agent {
	case AgentOpenCode:
		return []string{"mcp", "servers"}
	case AgentVSCode:
		return []string{"servers"}
	default:
		return []string{"mcpServers"}
	}
}

func (i *Installer) renderConfig(agent Agent, p string, entry any) ([]byte, error) {
	existing, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if agent == AgentCodex {
		return mergeTOMLSection(existing, "mcp_servers.golab", tomlEntry(entry)), nil
	}
	return setNestedJSON(existing, entryPath(agent), "golab", entry)
}

func snippetFor(agent Agent, entry any) string {
	if agent == AgentCodex {
		var b strings.Builder
		b.WriteString("[mcp_servers.golab]\n")
		b.WriteString(strings.Join(tomlEntry(entry), "\n"))
		b.WriteByte('\n')
		return b.String()
	}
	wrapped := map[string]any{}
	cur := wrapped
	path := entryPath(agent)
	for _, key := range path {
		next := map[string]any{}
		cur[key] = next
		cur = next
	}
	cur["golab"] = entry
	b, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// tomlEntry renders the stdio/HTTP command as TOML key/value lines.
func tomlEntry(entry any) []string {
	type standard struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	b, _ := json.Marshal(entry)
	var st standard
	_ = json.Unmarshal(b, &st)
	args := make([]string, 0, len(st.Args))
	for _, a := range st.Args {
		args = append(args, tomlQuote(a))
	}
	return []string{
		"command = " + tomlQuote(st.Command),
		"args = [" + strings.Join(args, ", ") + "]",
		"env = {}",
	}
}

func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setNestedJSON sets one key inside a nested JSON object while preserving all
// unrelated keys (as raw JSON) byte-for-byte. Invalid JSON is an error: the
// installer never truncates or resets a config it cannot parse.
func setNestedJSON(raw []byte, path []string, key string, value any) ([]byte, error) {
	vb, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("invalid JSON config: %w", err)
		}
	}
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	var build func(map[string]json.RawMessage, int) (json.RawMessage, error)
	build = func(m map[string]json.RawMessage, depth int) (json.RawMessage, error) {
		if depth == len(path) {
			m[key] = vb
			return json.Marshal(m)
		}
		p := path[depth]
		var child map[string]json.RawMessage
		if existing, ok := m[p]; ok && len(bytes.TrimSpace(existing)) > 0 {
			if err := json.Unmarshal(existing, &child); err != nil {
				return nil, fmt.Errorf("config key %q is not an object: %w", p, err)
			}
		}
		if child == nil {
			child = map[string]json.RawMessage{}
		}
		encoded, err := build(child, depth+1)
		if err != nil {
			return nil, err
		}
		m[p] = encoded
		return json.Marshal(m)
	}
	out, err := build(root, 0)
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		return nil, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

// mergeTOMLSection replaces an existing [section] (and its body up to the next
// section header) or appends a new one. It never parses or rewrites unrelated
// TOML, so a careful line-based merge cannot corrupt existing settings.
func mergeTOMLSection(raw []byte, section string, kv []string) []byte {
	header := "[" + section + "]"
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+len(kv)+3)
	skipping, replaced := false, false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			skipping = false
			if t == header {
				if replaced {
					skipping = true // drop a duplicate target section
					continue
				}
				replaced = true
				skipping = true
				out = append(out, header)
				out = append(out, kv...)
				out = append(out, "")
				continue
			}
		}
		if skipping {
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "")
		out = append(out, header)
		out = append(out, kv...)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// atomicWrite writes to a temp file in the same directory, fsyncs it, keeps a
// .bak of the previous good file, preserves its mode and renames into place.
func atomicWrite(p string, data []byte, dirSync func(string) error) (backup string, err error) {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(p); statErr == nil {
		mode = info.Mode().Perm()
		backup = p + ".bak"
		if previous, readErr := os.ReadFile(p); readErr == nil {
			_ = os.WriteFile(backup, previous, mode)
		}
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	f, err := os.CreateTemp(dir, ".agentinstall-tmp-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	if dirSync != nil {
		_ = dirSync(dir) // best effort: the data file is already durable
	}
	return backup, nil
}

// ---------- Skill installation ----------

// InstallSkills copies exactly the two known skill trees into each requested
// target directory. Nothing outside SkillNames is ever read or written.
func (i *Installer) InstallSkills(req SkillsRequest) (Result, error) {
	targets, err := resolveSkillTargets(req.Targets)
	if err != nil {
		return Result{}, err
	}
	if i.Skills == nil {
		return Result{}, errors.New("skill filesystem is not configured")
	}
	dry := req.DryRun == nil || *req.DryRun
	res := Result{Kind: "skills", DryRun: dry, Targets: make([]TargetResult, 0, len(targets))}
	for _, target := range targets {
		base, err := i.skillBase(target)
		if err != nil {
			return Result{}, err
		}
		tr := TargetResult{Target: target, Path: base}
		if dry {
			tr.Outcome = "would-write"
			res.Targets = append(res.Targets, tr)
			continue
		}
		if err := i.copySkills(base); err != nil {
			tr.Outcome, tr.Error = "error", redactError(err)
			res.Targets = append(res.Targets, tr)
			continue
		}
		tr.Outcome = "written"
		res.Targets = append(res.Targets, tr)
	}
	return res, nil
}

func resolveSkillTargets(targets []string) ([]string, error) {
	if len(targets) == 0 {
		return []string{TargetUnified}, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range targets {
		if raw == "all" {
			for _, t := range allSkillTargets {
				if !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
			continue
		}
		if !allowedSkillTarget(raw) {
			return nil, fmt.Errorf("unknown or unsafe skill target %q", raw)
		}
		if !seen[raw] {
			seen[raw] = true
			out = append(out, raw)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no skill targets selected")
	}
	return out, nil
}

func allowedSkillTarget(t string) bool {
	if strings.ContainsAny(t, "/\\") || strings.Contains(t, "..") || filepath.IsAbs(t) {
		return false
	}
	for _, cand := range allSkillTargets {
		if cand == t {
			return true
		}
	}
	return false
}

func (i *Installer) skillBase(target string) (string, error) {
	switch target {
	case TargetUnified:
		return filepath.Join(i.Home, ".agents", "skills"), nil
	case string(AgentClaudeCode):
		return filepath.Join(i.Home, ".claude", "skills"), nil
	case string(AgentOpenCode):
		return filepath.Join(i.Home, ".config", "opencode", "skills"), nil
	case string(AgentGemini):
		return filepath.Join(i.Home, ".gemini", "skills"), nil
	case string(AgentCursor):
		return filepath.Join(i.Home, ".cursor", "skills"), nil
	case TargetProject:
		return filepath.Join(i.Project, ".agents", "skills"), nil
	default:
		return "", fmt.Errorf("unknown skill target %q", target)
	}
}

func (i *Installer) copySkills(base string) error {
	for _, skill := range SkillNames {
		// Defense in depth: never walk an arbitrary path, only the two names.
		allowed := false
		for _, known := range SkillNames {
			if skill == known {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("skill %q is not allowlisted", skill)
		}
		err := fs.WalkDir(i.Skills, skill, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !fs.ValidPath(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
				return fmt.Errorf("unsafe skill path %q", p)
			}
			dest := filepath.Join(base, filepath.FromSlash(p))
			if d.IsDir() {
				return os.MkdirAll(dest, 0o750)
			}
			data, err := fs.ReadFile(i.Skills, p)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if info, infoErr := d.Info(); infoErr == nil && info.Mode().Perm() != 0 {
				mode = info.Mode().Perm()
			}
			return os.WriteFile(dest, data, mode)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// redactError keeps config-error strings helpful without leaking arbitrary
// content: only the error class is surfaced.
func redactError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.SplitN(msg, "\n", 2)[0]
	if len(msg) > 256 {
		msg = msg[:256] + "…"
	}
	return msg
}
