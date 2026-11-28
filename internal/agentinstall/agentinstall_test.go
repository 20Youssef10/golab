package agentinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// These tests are new behaviour for the MCP-install feature. They are written
// first (RED) against the API below, then the package is implemented to GREEN.

func boolPtr(v bool) *bool { return &v }

func newTestInstaller(t *testing.T) (*Installer, string, string, *int) {
	t.Helper()
	home := t.TempDir()
	project := t.TempDir()
	i := New(home, project, SkillsFS())
	syncCalls := 0
	i.DirSync = func(string) error { syncCalls++; return nil }
	return i, home, project, &syncCalls
}

func readMap(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v (%s)", p, err, b)
	}
	return m
}

func nested(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("key %q missing/not object in %v", k, cur)
		}
		cur = next
	}
	return cur
}

func mcpPath(home, project, agent string) string {
	switch agent {
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	case "claude-code":
		return filepath.Join(home, ".claude", "settings.json")
	case "cursor":
		return filepath.Join(home, ".cursor", "mcp.json")
	case "gemini":
		return filepath.Join(home, ".gemini", "settings.json")
	case "codex":
		return filepath.Join(home, ".codex", "config.toml")
	case "vscode":
		return filepath.Join(project, ".vscode", "mcp.json")
	}
	return ""
}

// TestInstallMCPAllAgentsAuto verifies that one call writes the documented,
// transport-aware config shape for every supported agent, that unrelated keys
// survive, that env is always empty, and that a .bak plus a mode-preserving
// atomic rename is used.
func TestInstallMCPAllAgentsAuto(t *testing.T) {
	i, home, project, syncCalls := newTestInstaller(t)
	const bin = "/opt/golab-mcp"

	res, err := i.InstallMCP(MCPRequest{
		Agents:    []string{"all"},
		GolabBin:  bin,
		Transport: "stdio",
		Mode:      "auto",
		DryRun:    boolPtr(false),
	})
	if err != nil {
		t.Fatalf("InstallMCP: %v", err)
	}
	if len(res.Targets) != len(AllAgents) {
		t.Fatalf("targets = %d, want %d", len(res.Targets), len(AllAgents))
	}
	for _, agent := range AllAgents {
		p := mcpPath(home, project, string(agent))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s config not written: %v", agent, err)
		}
	}
	if *syncCalls == 0 {
		t.Error("atomic rename did not sync the parent directory")
	}

	// opencode uses mcp.servers with a command array and an empty environment.
	oc := nested(t, readMap(t, mcpPath(home, project, "opencode")), "mcp", "servers", "golab")
	if oc["type"] != "local" {
		t.Errorf("opencode type = %v", oc["type"])
	}
	cmd, ok := oc["command"].([]any)
	if !ok || len(cmd) != 1 || cmd[0] != bin {
		t.Errorf("opencode command = %v", oc["command"])
	}
	if env, ok := oc["environment"].(map[string]any); !ok || len(env) != 0 {
		t.Errorf("opencode environment must be empty, got %v", oc["environment"])
	}

	// The other clients use command + args + env.
	for _, agent := range []Agent{AgentClaudeCode, AgentCursor, AgentGemini, AgentVSCode} {
		p := mcpPath(home, project, string(agent))
		m := readMap(t, p)
		var entry map[string]any
		switch agent {
		case AgentVSCode:
			entry = nested(t, m, "servers", "golab")
		default:
			entry = nested(t, m, "mcpServers", "golab")
		}
		if entry["command"] != bin {
			t.Errorf("%s command = %v", agent, entry["command"])
		}
		args, _ := entry["args"].([]any)
		if len(args) != 2 || args[0] != "--transport" || args[1] != "stdio" {
			t.Errorf("%s args = %v", agent, entry["args"])
		}
		if env, ok := entry["env"].(map[string]any); !ok || len(env) != 0 {
			t.Errorf("%s env must be empty, got %v", agent, entry["env"])
		}
	}

	// codex uses a TOML section.
	toml, err := os.ReadFile(mcpPath(home, project, "codex"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(toml)
	for _, want := range []string{"[mcp_servers.golab]", `command = "/opt/golab-mcp"`, `"--transport", "stdio"`, "env = {}"} {
		if !strings.Contains(s, want) {
			t.Errorf("codex config missing %q:\n%s", want, s)
		}
	}
}

// TestInstallMCPTransportAwareArgs verifies HTTP transport is reflected in
// both the JSON args arrays and the opencode command array.
func TestInstallMCPTransportAwareArgs(t *testing.T) {
	i, home, project, _ := newTestInstaller(t)
	_, err := i.InstallMCP(MCPRequest{
		Agents:    []string{"opencode", "claude-code"},
		GolabBin:  "/bin/golab-mcp",
		Transport: "http",
		Addr:      "127.0.0.1:9090",
		Mode:      "auto",
		DryRun:    boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	oc := nested(t, readMap(t, mcpPath(home, project, "opencode")), "mcp", "servers", "golab")
	cmd, _ := oc["command"].([]any)
	want := []any{"/bin/golab-mcp", "--transport", "http", "--addr", "127.0.0.1:9090"}
	if len(cmd) != len(want) {
		t.Fatalf("opencode command = %v, want %v", cmd, want)
	}
	for j := range want {
		if cmd[j] != want[j] {
			t.Fatalf("opencode command = %v, want %v", cmd, want)
		}
	}
	cc := nested(t, readMap(t, mcpPath(home, project, "claude-code")), "mcpServers", "golab")
	args, _ := cc["args"].([]any)
	if len(args) != 4 || args[2] != "--addr" || args[3] != "127.0.0.1:9090" {
		t.Fatalf("claude args = %v", args)
	}
}

// TestInstallMCPSpecificAgentsOnly writes only the requested agents.
func TestInstallMCPSpecificAgentsOnly(t *testing.T) {
	i, home, project, _ := newTestInstaller(t)
	res, err := i.InstallMCP(MCPRequest{
		Agents:   []string{"cursor", "gemini"},
		GolabBin: "/bin/golab-mcp",
		Mode:     "auto",
		DryRun:   boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(res.Targets))
	}
	for _, agent := range []string{"cursor", "gemini"} {
		if _, err := os.Stat(mcpPath(home, project, agent)); err != nil {
			t.Errorf("%s missing: %v", agent, err)
		}
	}
	for _, agent := range []string{"opencode", "claude-code", "codex", "vscode"} {
		if _, err := os.Stat(mcpPath(home, project, agent)); !os.IsNotExist(err) {
			t.Errorf("%s should not exist: %v", agent, err)
		}
	}
}

// TestInstallMCPNoClobberAndIdempotent proves unrelated settings survive and
// repeating the call yields exactly one golab entry (no duplicate keys).
func TestInstallMCPNoClobberAndIdempotent(t *testing.T) {
	i, home, project, _ := newTestInstaller(t)
	ccPath := mcpPath(home, project, "claude-code")
	if err := os.MkdirAll(filepath.Dir(ccPath), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := []byte(`{"mcpServers":{"other":{"command":"/keep"}},"theme":"dark"}`)
	if err := os.WriteFile(ccPath, initial, 0o640); err != nil {
		t.Fatal(err)
	}
	codexPath := mcpPath(home, project, "codex")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte("[workspace]\nroot = \"/keep\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := MCPRequest{Agents: []string{"claude-code", "codex"}, GolabBin: "/bin/golab-mcp", Mode: "auto", DryRun: boolPtr(false)}
	for n := 0; n < 2; n++ {
		if _, err := i.InstallMCP(req); err != nil {
			t.Fatalf("run %d: %v", n, err)
		}
	}
	m := readMap(t, ccPath)
	if m["theme"] != "dark" {
		t.Errorf("theme clobbered: %v", m["theme"])
	}
	servers := nested(t, m, "mcpServers")
	if _, ok := servers["other"]; !ok {
		t.Error("unrelated mcpServer clobbered")
	}
	if len(servers) != 2 {
		t.Errorf("duplicate/incorrect server count: %d (%v)", len(servers), servers)
	}

	toml, _ := os.ReadFile(codexPath)
	s := string(toml)
	if strings.Count(s, "[mcp_servers.golab]") != 1 {
		t.Errorf("duplicate codex section:\n%s", s)
	}
	if !strings.Contains(s, `root = "/keep"`) {
		t.Errorf("unrelated TOML clobbered:\n%s", s)
	}

	// A .bak of the first write exists and the original mode was preserved.
	if _, err := os.Stat(ccPath + ".bak"); err != nil {
		t.Errorf("no .bak: %v", err)
	}
	info, err := os.Stat(ccPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %o, want 640", got)
	}
	entries, err := os.ReadDir(filepath.Dir(ccPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".agentinstall-tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestInstallMCPManualWritesNothingAndReturnsSnippets verifies manual mode is
// side-effect free and returns pasteable snippets.
func TestInstallMCPManualWritesNothingAndReturnsSnippets(t *testing.T) {
	i, home, project, syncCalls := newTestInstaller(t)
	res, err := i.InstallMCP(MCPRequest{
		Agents:   []string{"opencode", "claude-code", "codex"},
		GolabBin: "/bin/golab-mcp",
		Mode:     "manual",
		DryRun:   boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Targets) != 3 {
		t.Fatalf("targets = %d", len(res.Targets))
	}
	for _, tr := range res.Targets {
		if tr.Outcome != "snippet" || tr.Snippet == "" {
			t.Errorf("%s outcome/snippet = %q/%q", tr.Target, tr.Outcome, tr.Snippet)
		}
		if !strings.Contains(tr.Snippet, "golab") {
			t.Errorf("%s snippet missing golab key: %q", tr.Target, tr.Snippet)
		}
	}
	if *syncCalls != 0 {
		t.Error("manual mode performed writes")
	}
	for _, agent := range AllAgents {
		if _, err := os.Stat(mcpPath(home, project, string(agent))); !os.IsNotExist(err) {
			t.Errorf("%s file created in manual mode: %v", agent, err)
		}
	}
}

// TestInstallMCPDryRunWritesNothing verifies dry-run (the default) previews
// without touching disk.
func TestInstallMCPDryRunWritesNothing(t *testing.T) {
	i, home, project, syncCalls := newTestInstaller(t)
	res, err := i.InstallMCP(MCPRequest{Agents: []string{"all"}, GolabBin: "/bin/golab-mcp", Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun {
		t.Error("dry_run not reported true")
	}
	for _, tr := range res.Targets {
		if tr.Outcome != "would-write" {
			t.Errorf("%s outcome = %q", tr.Target, tr.Outcome)
		}
	}
	if *syncCalls != 0 {
		t.Error("dry run synced a directory")
	}
	for _, agent := range AllAgents {
		if _, err := os.Stat(mcpPath(home, project, string(agent))); !os.IsNotExist(err) {
			t.Errorf("%s file created in dry run: %v", agent, err)
		}
	}
}

// TestInstallMCPRejectsTraversalAndUnknownAgents verifies the allowlist: no
// path-shaped or unknown target may be written.
func TestInstallMCPRejectsTraversalAndUnknownAgents(t *testing.T) {
	for _, agents := range [][]string{{"../../etc/passwd"}, {"/absolute/agent"}, {"opencode", "../evil"}} {
		i, home, project, _ := newTestInstaller(t)
		_, err := i.InstallMCP(MCPRequest{Agents: agents, GolabBin: "/bin/golab-mcp", DryRun: boolPtr(false)})
		if err == nil {
			t.Fatalf("agents %v accepted", agents)
		}
		for _, agent := range AllAgents {
			if _, err := os.Stat(mcpPath(home, project, string(agent))); !os.IsNotExist(err) {
				t.Errorf("agents %v wrote %s", agents, agent)
			}
		}
	}
}

// TestInstallSkillsUnifiedAndSpecific verifies both known skill trees are
// copied into the unified dir and into explicitly named agent dirs, and that
// unrelated files are untouched.
func TestInstallSkillsUnifiedAndSpecific(t *testing.T) {
	i, home, project, _ := newTestInstaller(t)
	unified := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(unified, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unified, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := i.InstallSkills(SkillsRequest{Targets: []string{"unified", "claude-code", "project"}, DryRun: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Targets) != 3 {
		t.Fatalf("targets = %d", len(res.Targets))
	}
	for _, base := range []string{unified, filepath.Join(home, ".claude", "skills"), filepath.Join(project, ".agents", "skills")} {
		for _, skill := range SkillNames {
			p := filepath.Join(base, skill, "SKILL.md")
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s missing: %v", p, err)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(unified, "keep.txt")); err != nil || string(b) != "keep" {
		t.Errorf("unrelated file clobbered: %q %v", b, err)
	}
}

// TestInstallSkillsAll verifies "all" covers unified plus every agent dir.
func TestInstallSkillsAll(t *testing.T) {
	i, home, project, _ := newTestInstaller(t)
	res, err := i.InstallSkills(SkillsRequest{Targets: []string{"all"}, DryRun: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	bases := []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".config", "opencode", "skills"),
		filepath.Join(home, ".gemini", "skills"),
		filepath.Join(home, ".cursor", "skills"),
		filepath.Join(project, ".agents", "skills"),
	}
	if len(res.Targets) != len(bases) {
		t.Fatalf("targets = %d, want %d", len(res.Targets), len(bases))
	}
	for _, base := range bases {
		if _, err := os.Stat(filepath.Join(base, "colab-mcp", "SKILL.md")); err != nil {
			t.Errorf("skills missing in %s: %v", base, err)
		}
	}
}

// TestInstallSkillsDryRunAndTraversal verifies dry-run writes nothing and an
// injected malicious/unknown skill tree is never copied.
func TestInstallSkillsDryRunAndTraversal(t *testing.T) {
	i, home, _, _ := newTestInstaller(t)
	res, err := i.InstallSkills(SkillsRequest{Targets: []string{"all"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun {
		t.Error("dry_run not reported")
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "colab-mcp")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote skills: %v", err)
	}

	for _, targets := range [][]string{{"../../evil"}, {"/abs/target"}, {"colab-mcp"}} {
		i2, home2, _, _ := newTestInstaller(t)
		_, err := i2.InstallSkills(SkillsRequest{Targets: targets, DryRun: boolPtr(false)})
		if err == nil {
			t.Fatalf("targets %v accepted", targets)
		}
		if _, err := os.Stat(filepath.Join(home2, ".agents", "skills")); !os.IsNotExist(err) {
			t.Errorf("targets %v wrote files: %v", targets, err)
		}
	}

	// A fake FS with an allowlisted tree plus an arbitrary one: only the known
	// tree is copied.
	i3, home3, _, _ := newTestInstaller(t)
	i3.Skills = fstest.MapFS{
		"colab-mcp/SKILL.md": &fstest.MapFile{Data: []byte("skill")},
		"evil-skill/x.txt":   &fstest.MapFile{Data: []byte("evil")},
	}
	if _, err := i3.InstallSkills(SkillsRequest{Targets: []string{"unified"}, DryRun: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(home3, ".agents", "skills")
	if _, err := os.Stat(filepath.Join(base, "colab-mcp", "SKILL.md")); err != nil {
		t.Errorf("allowed skill not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "evil-skill")); !os.IsNotExist(err) {
		t.Errorf("arbitrary skill copied: %v", err)
	}
}

// TestSkillNamesAreSorted keeps the reported/installed set deterministic.
func TestSkillNamesAreSorted(t *testing.T) {
	got := append([]string(nil), SkillNames...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(SkillNames, ",") {
		t.Fatalf("SkillNames not sorted: %v", SkillNames)
	}
	if len(SkillNames) != 2 {
		t.Fatalf("SkillNames = %v, want exactly two portable skills", SkillNames)
	}
}

// TestEmbeddedSkillsMatchRepo guards against drift between the embedded copy
// and the portable skill shipped in .agents/skills.
func TestEmbeddedSkillsMatchRepo(t *testing.T) {
	repo := filepath.Join("..", "..", ".agents", "skills")
	for _, rel := range []string{
		"colab-mcp/SKILL.md",
		"colab-mcp/references/tools.md",
		"colab-mcp/references/client-config.md",
		"golab-operations/SKILL.md",
		"golab-operations/references/command-map.md",
		"golab-operations/references/safety.md",
	} {
		want, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("repo skill %s: %v", rel, err)
		}
		got, err := readEmbedded(rel)
		if err != nil {
			t.Fatalf("embedded skill %s: %v", rel, err)
		}
		if string(want) != string(got) {
			t.Errorf("embedded skill %s drifted from repo", rel)
		}
	}
}

// TestInstallMCPCollapsesDuplicatePreexistingTOMLSections hardens the careful
// merge: a config that already contains two [mcp_servers.golab] sections must
// end up with exactly one after an idempotent write.
func TestInstallMCPCollapsesDuplicatePreexistingTOMLSections(t *testing.T) {
	i, home, _, _ := newTestInstaller(t)
	p := mcpPath(home, "", "codex")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := "[mcp_servers.golab]\ncommand = \"old\"\n\n[other]\nx = 1\n\n[mcp_servers.golab]\ncommand = \"older\"\n"
	if err := os.WriteFile(p, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := i.InstallMCP(MCPRequest{Agents: []string{"codex"}, GolabBin: "/bin/golab-mcp", DryRun: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "[mcp_servers.golab]"); got != 1 {
		t.Errorf("sections = %d, want 1:\n%s", got, b)
	}
	if !strings.Contains(string(b), `x = 1`) {
		t.Errorf("unrelated section lost:\n%s", b)
	}
}
