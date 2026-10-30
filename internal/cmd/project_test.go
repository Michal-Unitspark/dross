package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/boardsync"
	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/project"
)

// readDotted/writeDotted are the dotted-path field accessors the
// /dross-init slash command relies on for `dross project set X Y`.
// If a path is missing here, the prompt can't write that field.
//
// These tests pin the supported set so adding/removing a path is
// a deliberate change with test surface, not a silent drift.

func TestReadDottedSupportedPaths(t *testing.T) {
	p := &project.Project{
		Project: project.ProjectMeta{
			Name:        "feast",
			Version:     "1.2.3.0",
			Description: "meal plans",
		},
		Stack: project.Stack{
			PackageManager: "pnpm",
			Languages:      []string{"typescript", "go"},
			Frameworks:     []string{"sveltekit", "drizzle"},
		},
		Runtime: project.Runtime{
			Mode:             "docker",
			DevCommand:       "docker compose up app",
			TestCommand:      "docker compose exec app pnpm test",
			TypecheckCommand: "docker compose exec app pnpm typecheck",
			LintCommand:      "docker compose exec app pnpm lint",
			BuildCommand:     "docker compose exec app pnpm build",
			MigrateCommand:   "docker compose exec app pnpm db:migrate",
		},
		Repo: project.Repo{
			GitMainBranch: "main",
			Layout:        "single",
		},
		Goals: project.Goals{
			CoreValue: "respects household constraints",
		},
	}

	cases := map[string]string{
		"project.name":              "feast",
		"project.description":       "meal plans",
		"project.version":           "1.2.3.0",
		"stack.package_manager":     "pnpm",
		"stack.languages":           "typescript,go",
		"stack.frameworks":          "sveltekit,drizzle",
		"runtime.mode":              "docker",
		"runtime.dev_command":       "docker compose up app",
		"runtime.test_command":      "docker compose exec app pnpm test",
		"runtime.typecheck_command": "docker compose exec app pnpm typecheck",
		"runtime.lint_command":      "docker compose exec app pnpm lint",
		"runtime.build_command":     "docker compose exec app pnpm build",
		"runtime.migrate_command":   "docker compose exec app pnpm db:migrate",
		"repo.git_main_branch":      "main",
		"repo.layout":               "single",
		"goals.core_value":          "respects household constraints",
	}
	for path, want := range cases {
		got, ok := readDotted(p, path)
		if !ok {
			t.Errorf("readDotted(%q): not found", path)
			continue
		}
		if got != want {
			t.Errorf("readDotted(%q): got %q want %q", path, got, want)
		}
	}
}

func TestReadDottedUnknownPath(t *testing.T) {
	p := &project.Project{}
	if _, ok := readDotted(p, "nonsense.field"); ok {
		t.Error("expected ok=false for unknown path")
	}
	if _, ok := readDotted(p, "project.nonsense"); ok {
		t.Error("expected ok=false for unknown subfield")
	}
}

func TestWriteDottedRoundTripsThroughReadDotted(t *testing.T) {
	p := &project.Project{}
	cases := map[string]string{
		"project.name":              "x-app",
		"project.description":       "tagline",
		"project.version":           "0.2.0.0",
		"stack.package_manager":     "pnpm",
		"runtime.mode":              "docker",
		"runtime.dev_command":       "docker compose up",
		"runtime.test_command":      "docker compose exec app pnpm test",
		"runtime.typecheck_command": "tsc --noEmit",
		"runtime.lint_command":      "eslint .",
		"runtime.build_command":     "vite build",
		"runtime.migrate_command":   "drizzle-kit push",
		"repo.git_main_branch":      "main",
		"repo.layout":               "monorepo",
		"goals.core_value":          "ship fast",
	}
	for path, value := range cases {
		if err := writeDotted(p, path, value); err != nil {
			t.Fatalf("writeDotted(%q, %q): %v", path, value, err)
		}
	}
	for path, want := range cases {
		got, ok := readDotted(p, path)
		if !ok {
			t.Errorf("read after write: %q not found", path)
			continue
		}
		if got != want {
			t.Errorf("round-trip %q: got %q want %q", path, got, want)
		}
	}
}

func TestWriteDottedSplitsCSV(t *testing.T) {
	p := &project.Project{}

	if err := writeDotted(p, "stack.languages", "typescript, go,  csharp,gdscript "); err != nil {
		t.Fatal(err)
	}
	got := p.Stack.Languages
	want := []string{"typescript", "go", "csharp", "gdscript"}
	if len(got) != len(want) {
		t.Fatalf("languages len: got %d want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("languages[%d]: got %q want %q", i, got[i], w)
		}
	}

	// frameworks share the same splitter
	if err := writeDotted(p, "stack.frameworks", "sveltekit,drizzle,paraglide"); err != nil {
		t.Fatal(err)
	}
	if len(p.Stack.Frameworks) != 3 {
		t.Errorf("frameworks: %v", p.Stack.Frameworks)
	}
}

func TestWriteDottedDropsEmptyCSVEntries(t *testing.T) {
	p := &project.Project{}
	if err := writeDotted(p, "stack.languages", "typescript,,go,"); err != nil {
		t.Fatal(err)
	}
	if len(p.Stack.Languages) != 2 {
		t.Errorf("expected empty entries dropped: got %v", p.Stack.Languages)
	}
}

func TestWriteDottedRejectsUnknownPath(t *testing.T) {
	p := &project.Project{}
	err := writeDotted(p, "nonsense.field", "x")
	if err == nil {
		t.Fatal("expected error for unknown path")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error should mention 'unknown': %v", err)
	}
}

// TestExpandedDottedPathsRoundTrip exercises every leaf added in the
// /dross-options expansion. Bools and CSVs round-trip through their
// stringified forms.
func TestExpandedDottedPathsRoundTrip(t *testing.T) {
	p := &project.Project{}
	cases := map[string]string{
		// stack
		"stack.type_checker": "tsc",
		"stack.linter":       "eslint",
		"stack.formatter":    "prettier",
		"stack.test_runner":  "vitest",
		"stack.e2e_runner":   "playwright",
		// runtime
		"runtime.stop_command":   "docker compose down",
		"runtime.test_watch":     "vitest --watch",
		"runtime.e2e_command":    "playwright test",
		"runtime.format_command": "prettier --write .",
		"runtime.seed_command":   "pnpm db:seed",
		"runtime.shell_command":  "docker compose exec app sh",
		"runtime.logs_command":   "docker compose logs -f app",
		// repo
		"repo.root_run_dir":      "apps/web",
		"repo.workspaces":        "apps/web,apps/api,packages/shared",
		"repo.branch_pattern":    "feat/*",
		"repo.commit_convention": "conventional",
		"repo.squash_merge":      "true",
		// remote (re-covered to ensure bool path stays consistent)
		"remote.public":      "true",
		"remote.log_api":     "false",
		"remote.auth_scheme": "bearer",
		"remote.project_id":  "42",
		"remote.api_version": "6.0",
		// paths
		"paths.source":     "src",
		"paths.tests":      "src",
		"paths.e2e":        "e2e",
		"paths.migrations": "src/db/migrations",
		"paths.schemas":    "src/db/schema",
		"paths.i18n":       "src/lib/i18n",
		"paths.public":     "static",
		// env
		"env.files":            ".env,.env.local",
		"env.secrets_location": "1password",
		"env.gitignored":       "true",
		// goals
		"goals.audience":        "self-hosters",
		"goals.non_goals":       "realtime collab,mobile native",
		"goals.differentiators": "lean prompts,mutation testing",
	}
	for path, value := range cases {
		if err := writeDotted(p, path, value); err != nil {
			t.Errorf("writeDotted(%q, %q): %v", path, value, err)
			continue
		}
		got, ok := readDotted(p, path)
		if !ok {
			t.Errorf("readDotted(%q): missing after write", path)
			continue
		}
		if got != value {
			t.Errorf("round-trip %q: got %q want %q", path, got, value)
		}
	}
}

// TestProjectSetAPIVersionIsLossless: setting remote.api_version through the
// CLI writes exactly one line, `api_version = "6.0"`, and leaves every other
// line of the file byte-identical; a project that never set it carries no
// api_version line at all.
func TestProjectSetAPIVersionIsLossless(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	path := filepath.Join(dir, ".dross", "project.toml")
	if err := runCmd(t, Project(), "set", "remote.url", "https://tfs.corp/tfs/Coll/P/_git/R"); err != nil {
		t.Fatalf("set url: %v", err)
	}
	before := mustRead(t, path)
	if strings.Contains(before, "api_version") {
		t.Fatalf("an unset api_version already has a line:\n%s", before)
	}

	if err := runCmd(t, Project(), "set", "remote.api_version", "6.0"); err != nil {
		t.Fatalf("set api_version: %v", err)
	}
	after := mustRead(t, path)
	beforeLines, afterLines := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(afterLines) != len(beforeLines)+1 {
		t.Fatalf("want exactly one added line, got %d -> %d:\n%s", len(beforeLines), len(afterLines), after)
	}
	added := ""
	for i, j := 0, 0; j < len(afterLines); j++ {
		if i < len(beforeLines) && afterLines[j] == beforeLines[i] {
			i++
			continue
		}
		if added != "" {
			t.Fatalf("more than one line differs:\n%s", after)
		}
		added = afterLines[j]
	}
	if strings.TrimSpace(added) != `api_version = "6.0"` {
		t.Errorf("added line = %q, want api_version = \"6.0\"", added)
	}

	p, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := readDotted(p, "remote.api_version"); !ok || got != "6.0" {
		t.Errorf("get remote.api_version = %q (%v), want 6.0", got, ok)
	}
}

// TestBoardDottedArmsRoundTrip drives every board.* scalar through the full
// CLI path the slash commands use: writeDotted → Save → Load → readDotted. A
// missing read or write arm breaks the round-trip on that field.
func TestBoardDottedArmsRoundTrip(t *testing.T) {
	p := &project.Project{}
	cases := map[string]string{
		"board.provider":       "youtrack",
		"board.base_url":       "https://yt.example.com",
		"board.auth_env":       "YOUTRACK_TOKEN",
		"board.auth_user":      "me@example.com",
		"board.project":        "PROJ",
		"board.enabled":        "true",
		"board.milestone_mode": "version",
	}
	for path, value := range cases {
		if err := writeDotted(p, path, value); err != nil {
			t.Errorf("writeDotted(%q, %q): %v", path, value, err)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := p.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := project.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for dotted, want := range cases {
		got, ok := readDotted(loaded, dotted)
		if !ok {
			t.Errorf("readDotted(%q): missing after Save→Load", dotted)
			continue
		}
		if got != want {
			t.Errorf("round-trip %q: got %q want %q", dotted, got, want)
		}
	}
}

// remote.auth_user is the [remote] half of Basic auth: the Bitbucket ship
// backend sends base64(auth_user:$auth_env) and 401s without it. It has to
// survive a Save→Load cycle or the credential silently vanishes between
// `dross project set` and the ship that needs it.
func TestRemoteAuthUserRoundTrip(t *testing.T) {
	p := &project.Project{}
	cases := map[string]string{
		"remote.auth_user":   "bb-workspace-user",
		"remote.auth_scheme": "basic",
	}
	for path, value := range cases {
		if err := writeDotted(p, path, value); err != nil {
			t.Errorf("writeDotted(%q, %q): %v", path, value, err)
		}
	}
	if p.Remote.AuthUser != "bb-workspace-user" {
		t.Errorf("Remote.AuthUser = %q, want %q", p.Remote.AuthUser, "bb-workspace-user")
	}

	path := filepath.Join(t.TempDir(), "project.toml")
	if err := p.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := project.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for dotted, want := range cases {
		got, ok := readDotted(loaded, dotted)
		if !ok {
			t.Errorf("readDotted(%q): missing after Save→Load", dotted)
			continue
		}
		if got != want {
			t.Errorf("round-trip %q: got %q want %q", dotted, got, want)
		}
	}
}

// The new arm must be an exact case, not a remote.* catch-all — a typo has to
// keep erroring rather than being written to a field nothing reads.
func TestUnknownRemoteDottedKeyStillRejected(t *testing.T) {
	for _, bad := range []string{"remote.auth_users", "remote.auth_user.name", "remote.user"} {
		p := &project.Project{}
		if err := writeDotted(p, bad, "x"); err == nil {
			t.Errorf("writeDotted(%q) succeeded; unknown keys must error", bad)
		}
		if _, ok := readDotted(p, bad); ok {
			t.Errorf("readDotted(%q) = ok; unknown keys must report missing", bad)
		}
	}
}

func TestWriteDottedRejectsBadBool(t *testing.T) {
	p := &project.Project{}
	if err := writeDotted(p, "remote.public", "maybe"); err == nil {
		t.Error("expected error for invalid bool")
	}
}

// TestProjectCover_ShowPropagatesLoadError drives projectShow's
// `if err != nil` (project.go:30) down its error branch: with no .dross
// root, loadProject fails and `show` must return that error. The negated
// mutant would skip the return and proceed with a nil project instead of
// surfacing the failure.
func TestProjectCover_ShowPropagatesLoadError(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Project(), "show"); err == nil {
		t.Fatal("expected project show to error when no .dross root exists")
	}
}

// TestProjectCover_GetPropagatesLoadError drives projectGet's
// `if err != nil` (project.go:47) down its error branch: with no .dross
// root, loadProject fails and `get` must return that error before it ever
// reaches readDotted. The negated mutant would skip the return and deref a
// nil project.
func TestProjectCover_GetPropagatesLoadError(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Project(), "get", "project.name"); err == nil {
		t.Fatal("expected project get to error when no .dross root exists")
	}
}

// TestBoardFieldsAllAddressable is the completeness guard behind c-6: every
// [board] field the code reads must be reachable through both readDotted and
// writeDotted. Driven by reflection over the toml tags rather than a hand-kept
// list, so a 10th Board field fails here the day it is added.
func TestBoardFieldsAllAddressable(t *testing.T) {
	bt := reflect.TypeOf(project.Board{})
	for i := 0; i < bt.NumField(); i++ {
		f := bt.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if tag == "" || tag == "-" {
			t.Fatalf("Board.%s has no toml tag — the dotted path is undefined", f.Name)
		}
		path, probe := "board."+tag, "x"
		switch f.Type.Kind() {
		case reflect.Map:
			// Map fields are addressed one entry at a time
			// (locked state_map_write), so probe a concrete key.
			path += ".planned"
		case reflect.Struct:
			// Nested tables are addressed one key at a time too. Probe the
			// first declared key so a new sub-field of an existing table is
			// still covered by the sub-table's own round-trip test.
			path += "." + firstTOMLKey(t, f.Type)
		case reflect.Bool:
			probe = "true"
		}
		// An enum-valued key is gated (config-value-truth), so the probe has to
		// be a real member. The claim under test is that the field is
		// ADDRESSABLE, not that it accepts arbitrary text — using "x" here
		// would now assert the opposite of what the gate exists to do.
		if set, gated := enumKeys[path]; gated {
			probe = set.Values()[0]
		}
		p := &project.Project{}
		if err := writeDotted(p, path, probe); err != nil {
			t.Errorf("Board.%s: writeDotted(%q) = %v — field is not settable", f.Name, path, err)
			continue
		}
		if _, ok := readDotted(p, path); !ok {
			t.Errorf("Board.%s: readDotted(%q) not ok — field is not readable", f.Name, path)
		}
	}
}

// firstTOMLKey returns the toml key of a nested table's first field — the
// probe TestBoardFieldsAllAddressable uses to reach into it.
func firstTOMLKey(t *testing.T, typ reflect.Type) string {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		tag, _, _ := strings.Cut(typ.Field(i).Tag.Get("toml"), ",")
		if tag != "" && tag != "-" {
			return tag
		}
	}
	t.Fatalf("%s has no toml-tagged field — its dotted paths are undefined", typ)
	return ""
}

func TestStateMapPerKeyWrite(t *testing.T) {
	// No [board.state_map] table at all: the first write must create the map,
	// not panic on a nil map.
	p := &project.Project{}
	if err := writeDotted(p, "board.state_map.complete", "Closed"); err != nil {
		t.Fatalf("first state_map write: %v", err)
	}
	if got, _ := readDotted(p, "board.state_map.complete"); got != "Closed" {
		t.Errorf("board.state_map.complete = %q, want Closed", got)
	}

	// A second key must not clobber the first — per-key write, never a
	// whole-map replace.
	if err := writeDotted(p, "board.state_map.in-progress", "In Review"); err != nil {
		t.Fatal(err)
	}
	if got, _ := readDotted(p, "board.state_map.complete"); got != "Closed" {
		t.Errorf("after writing a second key, board.state_map.complete = %q, want Closed", got)
	}
	if got, _ := readDotted(p, "board.state_map.in-progress"); got != "In Review" {
		t.Errorf("board.state_map.in-progress = %q, want In Review", got)
	}

	// Bare `board.state_map` is not an addressable leaf.
	if err := writeDotted(p, "board.state_map", "x"); err == nil {
		t.Error("bare board.state_map was accepted as a leaf; want unknown-field error")
	}
}

// TestProjectSetGatesStateMapKeys proves c-4's write half: a [board].state_map
// key outside the lifecycle set is refused, because the sync-time lookup is
// keyed by what dross emits and an override on anything else can never apply.
func TestProjectSetGatesStateMapKeys(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	path := filepath.Join(dir, ".dross", "project.toml")

	t.Run("an out-of-set key is refused before anything is written", func(t *testing.T) {
		before := mustRead(t, path)
		err := runCmd(t, Project(), "set", "board.state_map.planning", "To Do")
		if err == nil {
			t.Fatal(`board.state_map.planning was accepted — it is the pre-rename key and remaps a state dross never emits`)
		}
		if !strings.Contains(err.Error(), configenum.LifecycleStatuses.List()) {
			t.Errorf("error %q does not name the five valid keys", err)
		}
		if after := mustRead(t, path); after != before {
			t.Errorf("project.toml changed on a refused write — the key must be rejected before the map is touched:\n%s", after)
		}
	})

	t.Run("a valid key still writes", func(t *testing.T) {
		if err := runCmd(t, Project(), "set", "board.state_map.uat", "In Review"); err != nil {
			t.Fatalf("board.state_map.uat: %v", err)
		}
		if body := mustRead(t, path); !strings.Contains(body, `In Review`) {
			t.Errorf("entry not written:\n%s", body)
		}
	})

	t.Run("a near-miss of case is normalized, not rejected", func(t *testing.T) {
		if err := runCmd(t, Project(), "set", "board.state_map.Planned", "Backlog"); err != nil {
			t.Fatalf("board.state_map.Planned should normalize to planned: %v", err)
		}
		// Stored under the normalized key, which is the only spelling the
		// sync-time lookup ever asks for.
		p, _, err := loadProject()
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Board.StateMap["planned"]; got != "Backlog" {
			t.Errorf(`StateMap["planned"] = %q, want Backlog — stored under the raw key, where nothing will find it`, got)
		}
	})

	t.Run("read and unset address the normalized entry too", func(t *testing.T) {
		// The trap a write-only normalization sets: the CLI writes an entry it
		// can no longer address, so `get` reads back empty and `--unset`
		// deletes nothing.
		out := captureStdout(t, func() {
			if err := runCmd(t, Project(), "get", "board.state_map.Planned"); err != nil {
				t.Fatalf("get board.state_map.Planned: %v", err)
			}
		})
		if !strings.Contains(out, "Backlog") {
			t.Errorf("get board.state_map.Planned = %q, want the value written under planned", out)
		}
		if err := runCmd(t, Project(), "set", "--unset", "board.state_map.Planned"); err != nil {
			t.Fatalf("unset board.state_map.Planned: %v", err)
		}
		p, _, err := loadProject()
		if err != nil {
			t.Fatal(err)
		}
		if _, still := p.Board.StateMap["planned"]; still {
			t.Error("--unset board.state_map.Planned deleted nothing — write, read and unset must agree on the key")
		}
	})

	t.Run("an existing bad key stays readable and removable", func(t *testing.T) {
		// doctor reports on-disk bad keys, so the CLI has to be able to repair
		// one — otherwise it names a fault with no fix.
		//
		// Its own repo: the subtests above already wrote a [board.state_map]
		// table, and a second header for the same table is a TOML decode error
		// rather than a merge.
		dir := t.TempDir()
		chdir(t, dir)
		if err := runCmd(t, Init()); err != nil {
			t.Fatalf("init: %v", err)
		}
		path := filepath.Join(dir, ".dross", "project.toml")
		mustWrite(t, path, mustRead(t, path)+"\n[board.state_map]\nplanning = \"To Do\"\n")
		out := captureStdout(t, func() {
			if err := runCmd(t, Project(), "get", "board.state_map.planning"); err != nil {
				t.Fatalf("get on an existing bad key: %v", err)
			}
		})
		if !strings.Contains(out, "To Do") {
			t.Errorf("a bad key on disk is unreadable: %q", out)
		}
		if err := runCmd(t, Project(), "set", "--unset", "board.state_map.planning"); err != nil {
			t.Fatalf("unset an existing bad key: %v", err)
		}
		p, _, err := loadProject()
		if err != nil {
			t.Fatal(err)
		}
		if _, still := p.Board.StateMap["planning"]; still {
			t.Error("a bad key survived --unset — doctor would report a fault the CLI cannot repair")
		}
	})
}

func TestProjectUnsetStateMapEntry(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)
	mustRunSet(t, "board.state_map.complete", "Closed")
	mustRunSet(t, "board.state_map.in-progress", "In Review")
	mustRunSet(t, "board.provider", "jira")

	if err := runCmd(t, Project(), "set", "--unset", "board.state_map.complete"); err != nil {
		t.Fatalf("--unset state_map entry: %v", err)
	}
	p, _, err := loadProjectAt(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := p.Board.StateMap["done"]; present {
		t.Error("board.state_map.complete survived --unset")
	}
	if p.Board.StateMap["in-progress"] != "In Review" {
		t.Errorf("sibling entry lost: %v", p.Board.StateMap)
	}
	if p.Board.Provider != "jira" {
		t.Errorf("board.provider lost: %q", p.Board.Provider)
	}

	// Unsetting the last entry drops the table entirely — no empty
	// [board.state_map] left behind.
	if err := runCmd(t, Project(), "set", "--unset", "board.state_map.in-progress"); err != nil {
		t.Fatal(err)
	}
	body := mustRead(t, filepath.Join(".dross", "project.toml"))
	if strings.Contains(body, "state_map") {
		t.Errorf("empty [board.state_map] table left in project.toml:\n%s", body)
	}
}

func TestProjectUnsetScalarAndList(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)
	mustRunSet(t, "repo.squash_merge", "true")
	mustRunSet(t, "remote.reviewers", "a,b")
	mustRunSet(t, "remote.url", "https://example.com/x")

	for _, path := range []string{"repo.squash_merge", "remote.reviewers", "remote.url"} {
		if err := runCmd(t, Project(), "set", "--unset", path); err != nil {
			t.Fatalf("--unset %s: %v", path, err)
		}
	}
	p, _, err := loadProjectAt(t)
	if err != nil {
		t.Fatal(err)
	}
	if p.Repo.SquashMerge {
		t.Error("--unset repo.squash_merge did not yield false")
	}
	if len(p.Remote.Reviewers) != 0 {
		t.Errorf("--unset remote.reviewers = %v, want absent", p.Remote.Reviewers)
	}
	if p.Remote.URL != "" {
		t.Errorf("--unset remote.url = %q", p.Remote.URL)
	}
	body := mustRead(t, filepath.Join(".dross", "project.toml"))
	if strings.Contains(body, "<nil>") {
		t.Errorf("unset wrote the literal string <nil>:\n%s", body)
	}
	if strings.Contains(body, "reviewers") {
		t.Errorf("unset list is still present in project.toml:\n%s", body)
	}
}

func TestProjectUnsetUnknownPathLeavesFileUnchanged(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)
	path := filepath.Join(".dross", "project.toml")
	before := mustRead(t, path)

	err := runCmd(t, Project(), "set", "--unset", "board.nope")
	if err == nil {
		t.Fatal("--unset on an unknown path succeeded")
	}
	// Same message `set` gives for the same path.
	setErr := runCmd(t, Project(), "set", "board.nope", "x")
	if setErr == nil || err.Error() != setErr.Error() {
		t.Errorf("--unset error = %v, want the same as set's %v", err, setErr)
	}
	if after := mustRead(t, path); after != before {
		t.Errorf("project.toml was rewritten by a failed --unset:\n%s", after)
	}
}

func TestProjectGitHubProjectRoundTrip(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)
	mustRunSet(t, "board.github_project", "PVT_x")

	out := captureStdout(t, func() {
		if err := runCmd(t, Project(), "get", "board.github_project"); err != nil {
			t.Errorf("get: %v", err)
		}
	})
	if strings.TrimSpace(out) != "PVT_x" {
		t.Errorf("get board.github_project = %q, want PVT_x", strings.TrimSpace(out))
	}
	p, _, err := loadProjectAt(t)
	if err != nil {
		t.Fatal(err)
	}
	if p.Board.GitHubProject != "PVT_x" {
		t.Errorf("reloaded Board.GitHubProject = %q — stray top-level key?", p.Board.GitHubProject)
	}
}

// scaffoldProject initialises a .dross root with the minimum project.toml the
// set/get tests need.
func scaffoldProject(t *testing.T) {
	t.Helper()
	if err := runCmd(t, Init()); err != nil {
		t.Fatal(err)
	}
	mustRunSet(t, "project.name", "x")
	mustRunSet(t, "runtime.mode", "native")
}

// loadProjectAt re-reads project.toml from disk so assertions see what was
// actually persisted, not an in-memory struct.
func loadProjectAt(t *testing.T) (*project.Project, string, error) {
	t.Helper()
	return loadProject()
}

func TestProjectGetSinglePathUnchanged(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)
	mustRunSet(t, "project.name", "feast")

	out := captureStdout(t, func() {
		if err := runCmd(t, Project(), "get", "project.name"); err != nil {
			t.Errorf("get: %v", err)
		}
	})
	// Byte-identical against a literal — this is the guard that keeps every
	// existing prompt's orientation step working.
	if out != "feast\n" {
		t.Errorf("project get project.name = %q, want %q", out, "feast\n")
	}
}

func TestProjectGetMultiPath(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)
	mustRunSet(t, "project.name", "feast")
	mustRunSet(t, "board.state_map.complete", "Closed")

	out := captureStdout(t, func() {
		if err := runCmd(t, Project(), "get", "project.name", "runtime.mode", "board.state_map.complete"); err != nil {
			t.Errorf("multi get: %v", err)
		}
	})
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("multi-path project get is not one JSON object: %v\ngot %q", err, out)
	}
	want := map[string]string{"project.name": "feast", "runtime.mode": "native", "board.state_map.complete": "Closed"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("multi get = %v, want %v", got, want)
	}
}

func TestProjectGetUnknownPathAmongSeveral(t *testing.T) {
	chdir(t, t.TempDir())
	scaffoldProject(t)

	var err error
	out := captureStdout(t, func() {
		err = runCmd(t, Project(), "get", "project.name", "project.nope")
	})
	if err == nil {
		t.Fatal("want an error for an unknown path")
	}
	if !strings.Contains(err.Error(), "project.nope") {
		t.Errorf("error = %q, want it to name project.nope", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a partial object was emitted: %q", out)
	}
}

// TestProjectSetArityGuards covers the hand-rolled switch `set` uses instead of
// cobra's arity check. `--unset` forced Args from ExactArgs(2) to RangeArgs(1,2),
// so cobra no longer rejects a one-arg `set` and the guard is the only thing
// standing between `dross project set <path>` and an args[1] index-out-of-range.
func TestProjectSetArityGuards(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unset with a value", []string{"set", "--unset", "board.provider", "jira"}, "--unset takes a path and no value"},
		// The row cobra used to catch and now does not.
		{"set with no value", []string{"set", "board.provider"}, "accepts 2 arg(s), received 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chdir(t, t.TempDir())
			scaffoldProject(t)
			path := filepath.Join(".dross", "project.toml")
			before := mustRead(t, path)

			err := runCmd(t, Project(), c.args...)
			if err == nil {
				t.Fatalf("`project %s` succeeded; want an arity error", strings.Join(c.args, " "))
			}
			if err.Error() != c.want {
				t.Errorf("error = %q, want %q", err, c.want)
			}
			// The guard runs before loadProject, so nothing is written.
			if after := mustRead(t, path); after != before {
				t.Errorf("project.toml was rewritten by a rejected set:\n%s", after)
			}
		})
	}
}

// TestProjectSetVersionMirrorsToState (c-4): the other entry point mirrors the
// other way. Leaving `project set project.version` on the generic dotted-path
// writer strands state.json at the old value.
func TestProjectSetVersionMirrorsToState(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, Project(), "set", "project.version", "1.3.0.0"); err != nil {
		t.Fatal(err)
	}
	assertVersionParity(t, dir, "1.3.0.0")
}

// TestRepoProjectVersionIsLive (c-4): this repo's own [project].version was the
// dead 0.2.0.0 scaffold value while the real number lived only in state.json —
// which is exactly how the release tag came to be computed from an untracked
// file. release.yml reads this field now, so it has to be the live one.
func TestRepoProjectVersionIsLive(t *testing.T) {
	p, err := project.Load(filepath.Join(repoRootFromTest(t), ".dross", project.File))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVersion(p.Project.Version); err != nil {
		t.Fatalf("this repo's [project].version is unusable as a release tag: %v", err)
	}
	if p.Project.Version == "0.2.0.0" {
		t.Error("[project].version is still the dead scaffold value 0.2.0.0")
	}
}

// TestBoardFieldsDottedArmsRoundTrip drives the [board.fields] overrides
// through the same writeDotted → Save → Load → readDotted path as the rest of
// board.*. A missing arm on either side breaks the round-trip on that key.
func TestBoardFieldsDottedArmsRoundTrip(t *testing.T) {
	p := &project.Project{}
	cases := map[string]string{
		"board.fields.state":        "Статус",
		"board.fields.type":         "Kind",
		"board.fields.fix_versions": "Release",
	}
	for path, value := range cases {
		if err := writeDotted(p, path, value); err != nil {
			t.Errorf("writeDotted(%q, %q): %v", path, value, err)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := p.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := project.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for dotted, want := range cases {
		got, ok := readDotted(loaded, dotted)
		if !ok {
			t.Errorf("readDotted(%q): missing after Save→Load", dotted)
			continue
		}
		if got != want {
			t.Errorf("round-trip %q: got %q want %q", dotted, got, want)
		}
	}
}

// TestBoardFieldsUnsetIsScoped pins that clearing one override leaves its
// siblings alone — a whole-table reset would silently revert two field names
// the user still wants.
func TestBoardFieldsUnsetIsScoped(t *testing.T) {
	p := &project.Project{}
	if err := writeDotted(p, "board.fields.state", "Статус"); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := writeDotted(p, "board.fields.type", "Kind"); err != nil {
		t.Fatalf("write type: %v", err)
	}
	if err := unsetDotted(p, "board.fields.state"); err != nil {
		t.Fatalf("unset state: %v", err)
	}
	if got, _ := readDotted(p, "board.fields.state"); got != "" {
		t.Errorf("board.fields.state = %q after --unset, want empty", got)
	}
	if got, _ := readDotted(p, "board.fields.type"); got != "Kind" {
		t.Errorf("board.fields.type = %q, want it untouched by the sibling unset", got)
	}
}

// TestBoardFieldsRejectsUnknownKey pins that a typo'd override is refused by
// name rather than silently stored where nothing reads it — and that the
// refusal happens before anything is written.
func TestBoardFieldsRejectsUnknownKey(t *testing.T) {
	p := &project.Project{}
	if err := writeDotted(p, "board.fields.state", "Статус"); err != nil {
		t.Fatalf("write state: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := p.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	err = writeDotted(p, "board.fields.bogus", "x")
	if err == nil {
		t.Fatal("board.fields.bogus was accepted; a key nothing reads must be refused")
	}
	for _, want := range []string{"state", "type", "fix_versions"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the accepted key %q", err, want)
		}
	}

	if err := p.Save(path); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("project.toml changed after a refused write:\nbefore %s\nafter  %s", before, after)
	}
}

// TestBoardFieldsAbsentTableReadsEmpty pins the no-[board.fields] case: every
// key reads back "" with no panic, matching how an absent [board.state_map]
// behaves.
func TestBoardFieldsAbsentTableReadsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(path, []byte("[project]\n  name = \"x\"\n\n[board]\n  provider = \"youtrack\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := project.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, key := range []string{"board.fields.state", "board.fields.type", "board.fields.fix_versions"} {
		got, ok := readDotted(loaded, key)
		if !ok {
			t.Errorf("readDotted(%q): not addressable without a [board.fields] table", key)
			continue
		}
		if got != "" {
			t.Errorf("readDotted(%q) = %q, want empty", key, got)
		}
	}
}

// TestBoardConfigCarriesFields pins the copy into forge.Config. A forgotten
// field here makes every downstream field test unreachable — the config would
// round-trip perfectly and never reach the client that uses it.
func TestBoardConfigCarriesFields(t *testing.T) {
	b := project.Board{
		Provider: "youtrack",
		BaseURL:  "https://yt.example.com",
		Project:  "PROJ",
		Fields: project.BoardFields{
			State:       "Статус",
			Type:        "Kind",
			FixVersions: "Release",
		},
	}
	cfg := boardsync.Config(b, "https://yt.example.com", nil)
	if cfg.Fields.State != "Статус" {
		t.Errorf("Fields.State = %q, want Статус", cfg.Fields.State)
	}
	if cfg.Fields.Type != "Kind" {
		t.Errorf("Fields.Type = %q, want Kind", cfg.Fields.Type)
	}
	if cfg.Fields.FixVersions != "Release" {
		t.Errorf("Fields.FixVersions = %q, want Release", cfg.Fields.FixVersions)
	}

	if zero := boardsync.Config(project.Board{Provider: "youtrack"}, "", nil); zero.Fields != (forge.Fields{}) {
		t.Errorf("Fields = %+v for an unconfigured board, want the zero value", zero.Fields)
	}
}
