package project

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	original := &Project{
		Project: ProjectMeta{
			Name:        "feastahead",
			Version:     "1.2.3.4",
			Description: "meal planning",
			Created:     "2026-01-15",
		},
		Stack: Stack{
			Languages:      []string{"typescript", "go"},
			Frameworks:     []string{"sveltekit", "drizzle"},
			PackageManager: "pnpm",
			TestRunner:     "vitest",
			Locked: []LockedChoice{
				{Choice: "sveltekit", Why: "ts ssr", LockedAt: "2026-01-15"},
			},
		},
		Runtime: Runtime{
			Mode:           "docker",
			DevCommand:     "docker compose up app",
			TestCommand:    "docker compose exec app pnpm test",
			MigrateCommand: "docker compose exec app pnpm db:migrate",
			Services: map[string]Service{
				"app": {URL: "http://localhost:5173", Health: "/api/health"},
				"db":  {URL: "postgres://localhost:5432/x", Admin: "psql"},
			},
		},
		Repo: Repo{
			Layout:           "single",
			GitMainBranch:    "main",
			BranchPattern:    "feature/*",
			CommitConvention: "conventional",
			SquashMerge:      true,
		},
		Paths: Paths{Source: "src", Tests: "src", Migrations: "src/db/migrations"},
		Env:   Env{Files: []string{".env", ".env.local"}, SecretsLocation: "1password", Gitignored: true},
		Goals: Goals{
			CoreValue: "meal planning that respects household constraints",
			NonGoals:  []string{"realtime collab"},
		},
		Constraints: map[string]string{"hosting": "self-hosted"},
		Competition: []Competitor{{Name: "mealime", URL: "https://mealime.com", WhatTheyDo: "X"}},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")

	if err := original.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(original, loaded) {
		t.Fatalf("round-trip mismatch:\noriginal: %+v\nloaded:   %+v", original, loaded)
	}
}

func TestLoadMissingFileReturnsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestLoadRefusesARemoteHostInTrackedConfig is c-2's tracked-config half.
//
// project.toml is committed, so a remote host set there would be the repo
// authorizing dross to copy the working tree onto a machine of its choosing and
// execute the test suite. The refusal names the key, the value and the verb
// that grants it properly — a bare "invalid config" leaves the user with no
// move.
func TestLoadRefusesARemoteHostInTrackedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		key  string
		val  string
	}{
		{"host", "[mutation]\n  remote_host = \"helicon\"\n", "mutation.remote_host", "helicon"},
		// Half a config is not a bypass.
		{"workdir alone", "[mutation]\n  remote_workdir = \"/srv/dross\"\n", "mutation.remote_workdir", "/srv/dross"},
		{"both", "[mutation]\n  remote_host = \"helicon\"\n  remote_workdir = \"/srv/dross\"\n", "mutation.remote_host", "helicon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "project.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(path)
			if err == nil {
				t.Fatalf("a committed remote host was accepted: %+v", p)
			}
			// nil, not a partly-usable Project: no caller can proceed on a
			// config dross has just refused to honour, and a non-nil return
			// invites exactly that.
			if p != nil {
				t.Errorf("Load returned %+v alongside its refusal", p)
			}
			for _, want := range []string{tc.key, tc.val, "dross mutation remote grant"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q: %v", want, err)
				}
			}
		})
	}
}

// TestLoadStillDecodesTheRealMutationKeys pins the other side of the trap: the
// refusal is keyed to the trap FIELDS, not to unknown keys generally.
//
// It matters because toml.DecodeFile ignores undecoded keys silently — which is
// both why the trap fields have to be declared at all, and why a refusal
// implemented as "reject anything unrecognised under [mutation]" would be a
// different, much broader change that breaks every forward-compatible config.
func TestLoadStillDecodesTheRealMutationKeys(t *testing.T) {
	body := `[mutation]
  adapters = ["gremlins", "stryker"]

  [mutation.gremlins]
    timeout_coefficient = 30

  [mutation.stryker]
    workdir = "web"
`
	path := filepath.Join(t.TempDir(), "project.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("a real mutation config was refused: %v", err)
	}
	if !reflect.DeepEqual(p.Mutation.Adapters, []string{"gremlins", "stryker"}) {
		t.Errorf("adapters = %v", p.Mutation.Adapters)
	}
	if p.Mutation.Gremlins.TimeoutCoefficient != 30 {
		t.Errorf("timeout_coefficient = %d, want 30", p.Mutation.Gremlins.TimeoutCoefficient)
	}
	if p.Mutation.Stryker.Workdir != "web" {
		t.Errorf("stryker workdir = %q, want web", p.Mutation.Stryker.Workdir)
	}
}

// TestRepoOwnProjectTomlStillLoads: the trap is only worth having if it does not
// fire on the config dross itself ships. A repo that cannot load its own
// project.toml has traded one bug for a worse one.
func TestRepoOwnProjectTomlStillLoads(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	path := filepath.Join(dir, ".dross", "project.toml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no repo project.toml at %s", path)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("dross's own project.toml no longer loads: %v", err)
	}
}

func TestSaveDefaultsAreOmittedAndOptionalsRemainEmpty(t *testing.T) {
	// Empty project should serialise without explosion and load back equal.
	p := &Project{}
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := p.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Project.Name != "" {
		t.Errorf("expected empty name, got %q", loaded.Project.Name)
	}
	if loaded.Repo.SquashMerge {
		t.Error("expected SquashMerge to default false")
	}
}

// TestTestLaneDecodes pins the [[runtime.test_lane]] wire format against
// hand-written toml rather than against a struct this package Saved: the
// document is what a user edits and what `dross test lane add` has to produce,
// so a decoder that only agrees with its own encoder proves nothing about it.
//
// A dropped toml tag or a missing field decodes to a zero-length slice with no
// error at all — toml.Decode ignores keys it has no home for — so the
// length assertion is the one that fails when the schema regresses.
func TestTestLaneDecodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(path, []byte(`
[project]
name = "x"
version = "1.0.0.0"

[runtime]
mode = "native"
test_command = "go test ./..."

[[runtime.test_lane]]
name = "go"
match = ["internal/**/*.go", "main.go"]
command = "go test -count=1 ./..."
`), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.Runtime.TestLane) != 1 {
		t.Fatalf("want 1 lane, got %d — the toml tag or the Runtime field is missing", len(p.Runtime.TestLane))
	}
	lane := p.Runtime.TestLane[0]
	if lane.Name != "go" {
		t.Errorf("name = %q, want go", lane.Name)
	}
	if !reflect.DeepEqual(lane.Match, []string{"internal/**/*.go", "main.go"}) {
		t.Errorf("match = %v, want both globs in order", lane.Match)
	}
	if lane.Command != "go test -count=1 ./..." {
		t.Errorf("command = %q — this is the exact string consent fingerprints", lane.Command)
	}
	// Lanes are additive: declaring one must not disturb the scalar the
	// lane-less path still runs.
	if p.Runtime.TestCommand != "go test ./..." {
		t.Errorf("test_command = %q, want it untouched by the lane block", p.Runtime.TestCommand)
	}
}

// TestNoTestLaneIsAbsentFromTheDocument holds the opt-in half of the schema:
// a project with no lane must not grow a test_lane key on Save. Without
// omitempty the encoder writes an empty array into every project.toml dross
// touches, which turns "this repo has no lanes" into a line the user has to
// read and wonder about.
func TestNoTestLaneIsAbsentFromTheDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	p := &Project{Project: ProjectMeta{Name: "x", Version: "1.0.0.0"}, Runtime: Runtime{Mode: "native"}}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	if body := string(mustReadFile(t, path)); strings.Contains(body, "test_lane") {
		t.Errorf("a lane-less project.toml mentions test_lane:\n%s", body)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestTestLaneSelectorFieldsDecode: selector and empty_exit reach the struct
// from a hand-written document, which is what a user edits and what `dross test
// lane add` has to produce. A dropped toml tag decodes to the zero value with
// no error at all, so these assertions are the only thing that fails when the
// schema regresses.
func TestTestLaneSelectorFieldsDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(path, []byte(`
[project]
name = "x"
version = "1.0.0.0"

[runtime]
mode = "native"

[[runtime.test_lane]]
name = "go"
match = ["internal/**"]
command = "go test -count=1"
selector = "go-package"
empty_exit = [5]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.Runtime.TestLane) != 1 {
		t.Fatalf("want 1 lane, got %d", len(p.Runtime.TestLane))
	}
	lane := p.Runtime.TestLane[0]
	if lane.Selector != "go-package" {
		t.Errorf("selector = %q, want go-package — the toml tag or the field is missing", lane.Selector)
	}
	if !reflect.DeepEqual(lane.EmptyExit, []int{5}) {
		t.Errorf("empty_exit = %v, want [5]", lane.EmptyExit)
	}
}

// TestLaneWithoutSelectorFieldsRoundTripsUnchanged is the opt-in half of this
// phase's schema, and the sibling of TestNoTestLaneIsAbsentFromTheDocument: a
// lane that declares neither field must render exactly the bytes a project.toml
// written before selectors existed carries. Without omitempty on both fields
// every existing lane grows a `selector = ""` line on the next Save, which
// turns an opt-in feature into one every repo has to read about.
func TestLaneWithoutSelectorFieldsRoundTripsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	p := &Project{
		Project: ProjectMeta{Name: "x", Version: "1.0.0.0"},
		Runtime: Runtime{
			Mode:     "native",
			TestLane: []TestLane{{Name: "go", Match: []string{"internal/**"}, Command: "go test"}},
		},
	}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	body := string(mustReadFile(t, path))
	if strings.Contains(body, "selector") {
		t.Errorf("a lane declaring no selector rendered a selector key:\n%s", body)
	}
	if strings.Contains(body, "empty_exit") {
		t.Errorf("a lane declaring no empty exit codes rendered an empty_exit key:\n%s", body)
	}
}

// TestDeclaredSelectorFieldsRender: the other direction — a lane that DOES
// declare them must write them back, or `dross test lane add --selector` would
// accept a flag that never reaches disk.
func TestDeclaredSelectorFieldsRender(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	p := &Project{
		Project: ProjectMeta{Name: "x", Version: "1.0.0.0"},
		Runtime: Runtime{
			Mode: "native",
			TestLane: []TestLane{{
				Name:      "go",
				Match:     []string{"internal/**"},
				Command:   "go test",
				Selector:  "go-package",
				EmptyExit: []int{5},
			}},
		},
	}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	lane := reloaded.Runtime.TestLane[0]
	if lane.Selector != "go-package" || !reflect.DeepEqual(lane.EmptyExit, []int{5}) {
		t.Errorf("round-trip lost the selector fields: selector=%q empty_exit=%v", lane.Selector, lane.EmptyExit)
	}
}

// TestTestLanePrepareDecodes: prepare has to reach the struct from a
// hand-written document, which is both what a user edits and what `dross test
// lane add --prepare` produces. A dropped toml tag decodes to the zero value
// with no error at all, so the lane would silently run with no bootstrap on a
// cold host — a red suite blamed on the code.
func TestTestLanePrepareDecodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(path, []byte(`
[project]
name = "x"
version = "1.0.0.0"

[runtime]
mode = "native"

[[runtime.test_lane]]
name = "go"
match = ["internal/**"]
command = "go test -count=1"
prepare = "make build"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(p.Runtime.TestLane) != 1 {
		t.Fatalf("want 1 lane, got %d", len(p.Runtime.TestLane))
	}
	if got := p.Runtime.TestLane[0].Prepare; got != "make build" {
		t.Errorf("prepare = %q, want \"make build\" — the toml tag or the field is missing", got)
	}
}

// TestLaneWithoutPrepareRoundTripsUnchanged is the opt-in half of this phase's
// schema, and the sibling of TestNoTestLaneIsAbsentFromTheDocument: a lane
// declaring no prepare must render exactly the bytes a project.toml written
// before this phase carries. Without omitempty every existing lane grows a
// `prepare = ""` line on the next Save — which both reads as something the
// user has to go and set AND, worse, would change what the lane's consent
// fingerprint is taken over on a document nobody edited.
//
// The byte compare is a full Save → Load → Save cycle, so a field that
// rendered on the first pass and not the second (or the reverse) fails here
// rather than at the moment a grant went stale for no reason.
func TestLaneWithoutPrepareRoundTripsUnchanged(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "project.toml")
	p := &Project{
		Project: ProjectMeta{Name: "x", Version: "1.0.0.0"},
		Runtime: Runtime{
			Mode:     "native",
			TestLane: []TestLane{{Name: "go", Match: []string{"internal/**"}, Command: "go test"}},
		},
	}
	if err := p.Save(first); err != nil {
		t.Fatal(err)
	}
	before := string(mustReadFile(t, first))
	if strings.Contains(before, "prepare") {
		t.Errorf("a lane declaring no prepare rendered a prepare key:\n%s", before)
	}

	reloaded, err := Load(first)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	second := filepath.Join(dir, "again.toml")
	if err := reloaded.Save(second); err != nil {
		t.Fatal(err)
	}
	if after := string(mustReadFile(t, second)); after != before {
		t.Errorf("a lane-with-no-prepare document did not round-trip byte-identically:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// TestDeclaredPrepareRenders: the other direction — a lane that DOES declare a
// prepare must write it back, or `dross test lane add --prepare` would accept a
// flag that never reaches disk.
func TestDeclaredPrepareRenders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	p := &Project{
		Project: ProjectMeta{Name: "x", Version: "1.0.0.0"},
		Runtime: Runtime{
			Mode: "native",
			TestLane: []TestLane{{
				Name:    "go",
				Match:   []string{"internal/**"},
				Command: "go test",
				Prepare: "make build",
			}},
		},
	}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Runtime.TestLane[0].Prepare; got != "make build" {
		t.Errorf("round-trip lost the prepare: %q", got)
	}
}

// TestProjectTechdebtExcludeRoundTrip pins the [techdebt] exclude list through a
// Load → Save → Load cycle: dropping omitempty or mis-tagging the field breaks the
// second Load, and order is preserved because the entries are applied in order.
func TestProjectTechdebtExcludeRoundTrip(t *testing.T) {
	body := `[techdebt]
  exclude = ["internal/techdebt/", "*.golden"]
`
	dir := t.TempDir()
	src := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(src)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"internal/techdebt/", "*.golden"}
	if !reflect.DeepEqual(p.Techdebt.Exclude, want) {
		t.Fatalf("Exclude after first Load = %v, want %v", p.Techdebt.Exclude, want)
	}
	dst := filepath.Join(dir, "saved.toml")
	if err := p.Save(dst); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, err := Load(dst)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if !reflect.DeepEqual(again.Techdebt.Exclude, want) {
		t.Fatalf("Exclude after round trip = %v, want %v", again.Techdebt.Exclude, want)
	}
}

// TestProjectNoTechdebtSectionIsNil: an adopter without a [techdebt] table gets a
// nil Exclude (scan everything) and no error — the knob is opt-in.
func TestProjectNoTechdebtSectionIsNil(t *testing.T) {
	body := `[project]
  name = "x"
`
	path := filepath.Join(t.TempDir(), "project.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Techdebt.Exclude != nil {
		t.Fatalf("Exclude = %v, want nil with no [techdebt] table", p.Techdebt.Exclude)
	}
}

// TestProjectPackageHasOneEncoder pins the encoder hand-off: every non-test
// file in this package makes exactly one BurntSushi encoder call between
// them — toml.NewEncoder or toml.Marshal — and it lives in encodeFresh. A
// second call site is a re-encode path the patcher does not verify, which is
// the whole-file overwrite this phase exists to remove.
func TestProjectPackageHasOneEncoder(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkg, ok := sel.X.(*ast.Ident)
					if !ok || pkg.Name != "toml" {
						return true
					}
					if sel.Sel.Name == "NewEncoder" || sel.Sel.Name == "Marshal" {
						sites = append(sites, fmt.Sprintf("%s in %s (%s)", sel.Sel.Name, fn.Name.Name, filepath.Base(name)))
					}
					return true
				})
			}
		}
	}
	if len(sites) != 1 || !strings.HasPrefix(sites[0], "NewEncoder in encodeFresh") {
		t.Errorf("encoder call sites = %v, want exactly [NewEncoder in encodeFresh]", sites)
	}
	// The differ's tree conversion still routes through encodeFresh.
	src, err := os.ReadFile("patch_diff.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "return encodeFresh(p)") {
		t.Error("encodeCanonical no longer routes through encodeFresh")
	}
}

// TestRemoteAPIVersionBareFloat: a hand-edited `api_version = 6.0` is a TOML
// float, and it must load as the version "6.0" — not "6", and not a decode
// error that takes the whole project.toml down with it. A string stays
// verbatim; a value of the wrong kind is a load error naming the key.
func TestRemoteAPIVersionBareFloat(t *testing.T) {
	load := func(t *testing.T, line string) (*Project, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "project.toml")
		body := "[remote]\n  provider = \"azure-devops\"\n  " + line + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}
	for line, want := range map[string]APIVersion{
		"api_version = 5.0":             "5.0",
		"api_version = 6.0":             "6.0",
		"api_version = 7.1":             "7.1",
		"api_version = 7":               "7.0",
		`api_version = "6.0"`:           "6.0",
		`api_version = "7.1-preview.1"`: "7.1-preview.1",
	} {
		p, err := load(t, line)
		if err != nil {
			t.Errorf("%s: %v", line, err)
			continue
		}
		if p.Remote.APIVersion != want {
			t.Errorf("%s: APIVersion = %q, want %q", line, p.Remote.APIVersion, want)
		}
	}
	if _, err := load(t, "api_version = true"); err == nil || !strings.Contains(err.Error(), "remote.api_version") {
		t.Errorf("api_version = true: want a load error naming remote.api_version, got %v", err)
	}
}
