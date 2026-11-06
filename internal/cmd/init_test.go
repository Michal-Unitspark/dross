package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/architecture"
	"github.com/Rivil/dross/internal/defaults"
	"github.com/Rivil/dross/internal/project"
)

func loadInitedProject(t *testing.T, dir string) *project.Project {
	t.Helper()
	p, err := project.Load(filepath.Join(dir, ".dross", "project.toml"))
	if err != nil {
		t.Fatalf("load project.toml: %v", err)
	}
	return p
}

func TestInitSeedsRuntimeFromProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	p := loadInitedProject(t, dir)

	if p.Stack.Profile != "go" {
		t.Errorf("[stack].profile = %q, want \"go\"", p.Stack.Profile)
	}
	want := map[string]string{
		"test":      "go test -count=1 ./...",
		"typecheck": "go vet ./...",
		"format":    "gofmt -l .",
		"build":     "make build",
	}
	got := map[string]string{
		"test":      p.Runtime.TestCommand,
		"typecheck": p.Runtime.TypecheckCommand,
		"format":    p.Runtime.FormatCommand,
		"build":     p.Runtime.BuildCommand,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("[runtime].%s = %q, want %q (must come from the Go profile, not a hardcoded guess)", k, got[k], w)
		}
	}
}

func TestInitUnsupportedLeavesRuntimeUnseeded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# project"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)

	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	p := loadInitedProject(t, dir)

	if p.Stack.Profile != "" {
		t.Errorf("[stack].profile = %q, want empty for an unsupported stack", p.Stack.Profile)
	}
	for name, cmd := range map[string]string{
		"test": p.Runtime.TestCommand, "typecheck": p.Runtime.TypecheckCommand,
		"format": p.Runtime.FormatCommand, "build": p.Runtime.BuildCommand,
	} {
		if cmd != "" {
			t.Errorf("[runtime].%s = %q, want empty — no fabricated commands on an unsupported stack", name, cmd)
		}
	}
}

// TestInitSeedsArchitectureSkeleton guards c-3: dross init must seed an
// ARCHITECTURE.md skeleton at repo root. If seeding is removed, this fails
// (test_contract 1).
func TestInitSeedsArchitectureSkeleton(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(dir, architecture.File)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ARCHITECTURE.md not seeded at repo root: %v", err)
	}
	content := mustRead(t, path)
	if !strings.Contains(content, "# Architecture") {
		t.Errorf("seeded ARCHITECTURE.md missing header:\n%s", content)
	}
	if !strings.Contains(content, "organized by feature") {
		t.Errorf("seeded doc should declare feature-organization:\n%s", content)
	}
}

// TestInitDoesNotClobberExistingArchitecture guards first-creation-only: init
// must not overwrite an ARCHITECTURE.md the repo already has (idempotent
// refresh is deferred).
func TestInitDoesNotClobberExistingArchitecture(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	path := filepath.Join(dir, architecture.File)
	const existing = "# Mine\n\nhand-written.\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}

	if got := mustRead(t, path); got != existing {
		t.Errorf("init clobbered existing ARCHITECTURE.md:\ngot:  %q\nwant: %q", got, existing)
	}
}

// TestInitEnsuresHooksIdempotent pins the hook_scope contract: init wires the
// user-level PreCompact + SessionStart hooks, and a second init (fresh repo,
// same CLAUDE_CONFIG_DIR) leaves settings.json byte-identical with exactly one
// dross entry per event.
func TestInitEnsuresHooksIdempotent(t *testing.T) {
	cfg := t.TempDir()
	settings := filepath.Join(cfg, "settings.json")

	chdir(t, t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", cfg) // after chdir, which pins its own temp dir
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("first init: %v", err)
	}
	first := mustRead(t, settings)

	chdir(t, t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("second init: %v", err)
	}
	second := mustRead(t, settings)

	if first != second {
		t.Errorf("second init's ensure not byte-identical:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if n := countIn(readHookCommands(t, settings, "PreCompact"), preCompactHookCommand); n != 1 {
		t.Errorf("want exactly 1 dross PreCompact entry after two inits, got %d", n)
	}
	if n := countIn(readHookCommands(t, settings, "SessionStart"), sessionStartHookCommand); n != 1 {
		t.Errorf("want exactly 1 dross SessionStart entry after two inits, got %d", n)
	}
	// The gate pair rides the same ensure. onboard's byte-parity test cannot
	// catch it going missing — both share ensureUserHooks — so this one must.
	if n := countIn(readHookCommands(t, settings, "PreToolUse"), GateCheckHook); n != 1 {
		t.Errorf("want exactly 1 dross PreToolUse %q entry after two inits, got %d", GateCheckHook, n)
	}
	if n := countIn(readHookCommands(t, settings, "PostToolUse"), GateRecordHook); n != 1 {
		t.Errorf("want exactly 1 dross PostToolUse %q entry after two inits, got %d", GateRecordHook, n)
	}
}

// TestInitDetectsAzureRemote: an ssh v3 Azure DevOps origin initialises to the
// canonical https url, the org api_base and no public flag — and a saved
// GitHub defaults.toml contributes neither its token variable nor its
// reviewers.
func TestInitDetectsAzureRemote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	saved := &defaults.Defaults{Remote: defaults.RemoteDefaults{
		Provider: "github", APIBase: "https://api.github.com", AuthEnv: "GITHUB_TOKEN", Reviewers: []string{"alice"},
	}}
	if err := saved.SaveFile(filepath.Join(home, ".claude", "dross", defaults.File)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gitInit(t, dir, "git@ssh.dev.azure.com:v3/o/p/r")
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}

	p := loadInitedProject(t, dir)
	if p.Remote.URL != "https://dev.azure.com/o/p/_git/r" || p.Remote.Provider != "azure-devops" || p.Remote.APIBase != "https://dev.azure.com/o" {
		t.Errorf("[remote] = %+v, want the canonical url, azure-devops and the org api_base", p.Remote)
	}
	body := mustRead(t, filepath.Join(dir, ".dross", "project.toml"))
	_, remote, _ := strings.Cut(body, "[remote]")
	if end := strings.Index(remote, "\n["); end >= 0 {
		remote = remote[:end]
	}
	if remote == "" || strings.Contains(remote, "public") {
		t.Errorf("an azure-devops [remote] should leave public absent (false):\n%s", body)
	}
	if strings.Contains(body, "GITHUB_TOKEN") || strings.Contains(body, "alice") {
		t.Errorf("the saved GitHub defaults seeded the azure-devops remote:\n%s", body)
	}
}

// TestInitNeverPersistsOriginPAT: an https origin carrying a PAT in its
// userinfo initialises to a project.toml with no trace of it.
func TestInitNeverPersistsOriginPAT(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	gitInit(t, dir, "https://o:PATVALUE@dev.azure.com/o/p/_git/r")
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	body := mustRead(t, filepath.Join(dir, ".dross", "project.toml"))
	if strings.Contains(body, "PATVALUE") {
		t.Errorf("the origin's PAT reached project.toml:\n%s", body)
	}
	if !strings.Contains(body, `url = "https://dev.azure.com/o/p/_git/r"`) {
		t.Errorf("want the canonical url without userinfo:\n%s", body)
	}
}
