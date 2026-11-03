package boardsync

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/board"
	"github.com/Rivil/dross/internal/configenum"
)

// Board lifecycle / state-map divergence guard (c-2).
//
// The lifecycle vocabulary has two independent sides. dross *emits* a status —
// from a `--status <literal>` in a workflow prompt, or from DerivePhaseStatus
// when the flag is absent — and each forge *keys* a state map on one. Nothing
// in the type system connects them, and they drifted: the producer emitted
// "planning" while both maps keyed "planned", so the commonest phase shape
// there is warned and skipped its board transition.
//
// These tests gate both directions. A status dross emits with no state-map
// entry fails; a state map keyed on a status nothing emits fails.
//
// **"Emits" means call sites only** — the `--status <literal>` occurrences in
// assets/prompts/*.md, the values DerivePhaseStatus can return, and the status
// every close call FinalizePhase reaches carries. Declared Go
// constants are deliberately NOT counted: ctx.go's constants are defined *as*
// members of configenum.LifecycleStatuses (cmd's issue_test.go asserts exactly that),
// so folding them in would make the emit-set equal the Set by construction and
// turn this file into a tautology. The point is to catch a status that has a
// definition but no longer has a caller, which is precisely how "shipped" and
// "complete" sat in both maps as keys nothing ever resolved.

// statusLiteralRE matches a `--status <value>` occurrence in a prompt. The
// value is the lifecycle vocabulary's own shape — lowercase words and hyphens —
// so a placeholder like `--status <value>` is not matched and does not need an
// exemption.
var statusLiteralRE = regexp.MustCompile(`--status\s+([a-z][a-z-]*)`)

// emittedStatuses collects every lifecycle status dross can put on a board.
func emittedStatuses(t *testing.T) map[string]string {
	t.Helper()
	root := repoRootFromTest(t)
	out := map[string]string{} // status -> where it is emitted

	// 1. Prompt call sites.
	promptDir := filepath.Join(root, "assets", "prompts")
	entries, err := os.ReadDir(promptDir)
	if err != nil {
		t.Fatalf("read %s: %v", promptDir, err)
	}
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(promptDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		scanned++
		for _, m := range statusLiteralRE.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = "assets/prompts/" + e.Name()
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no prompt files — the emit-set would be empty and every assertion below vacuous")
	}

	// 2. DerivePhaseStatus's return values, read as source. Executing the
	// function instead would only reach the branches a fixture happens to
	// trigger; parsing reaches every return statement, so a new branch is
	// caught the day it is written.
	for _, s := range derivePhaseStatusReturns(t, filepath.Join(root, "internal", "boardsync", "phase.go")) {
		out[s] = "internal/boardsync/phase.go:DerivePhaseStatus"
	}

	// 3. The finalizer's close calls. The Phases and Tasks lanes' terminal
	// statuses moved out of ship.md into FinalizePhase, which `dross phase
	// complete` runs, so the binary is now their only call site.
	for _, s := range finalizerCloses(t) {
		out[s] = finalizerFile + ":FinalizePhase"
	}
	return out
}

// packageStringConsts collects the string constants declared in a package's
// non-test files, by name.
func packageStringConsts(t *testing.T, dir string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	consts := map[string]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if i >= len(vs.Values) {
							continue
						}
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if v, err := strconv.Unquote(lit.Value); err == nil {
								consts[name.Name] = v
							}
						}
					}
				}
			}
		}
	}
	return consts
}

// finalizerFile holds the Phases and Tasks lanes' terminal emissions:
// boardsync.FinalizePhase.
var finalizerFile = filepath.Join("internal", "boardsync", "finalize.go")

// closerFuncs are the finalizer's sinks: each drives a card to its status
// argument and closes it. Calls to them are what is read; their own bodies
// only pass that parameter on.
var closerFuncs = []string{"closeNew", "finalizeCard"}

// finalizerCloses resolves the status argument of every close call reachable
// from FinalizePhase within finalize.go. Parsed rather than run, for the reason
// derivePhaseStatusReturns gives: a fixture reaches the branches it triggers, a
// parse reaches all of them.
func finalizerCloses(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRootFromTest(t), finalizerFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	consts := packageStringConsts(t, filepath.Dir(path))

	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
			funcs[fd.Name.Name] = fd
		}
	}
	statusArg := map[string]int{}
	for _, name := range closerFuncs {
		fd, ok := funcs[name]
		if !ok {
			t.Fatalf("%s has no %s — the finalizer's closes cannot be read", finalizerFile, name)
		}
		i, at := 0, -1
		for _, field := range fd.Type.Params.List {
			for _, n := range field.Names {
				if n.Name == "status" {
					at = i
				}
				i++
			}
		}
		if at < 0 {
			t.Fatalf("%s's %s has no status parameter — the guard cannot tell which argument it closes with", finalizerFile, name)
		}
		statusArg[name] = at
	}
	if _, ok := funcs["FinalizePhase"]; !ok {
		t.Fatalf("no FinalizePhase in %s — the emit-set is missing its finalizer half", finalizerFile)
	}

	var got []string
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		fd, ok := funcs[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			at, closer := statusArg[fn.Name]
			if !closer {
				walk(fn.Name)
				return true
			}
			var s string
			resolved := false
			if at < len(call.Args) {
				switch arg := call.Args[at].(type) {
				case *ast.Ident:
					s, resolved = consts[arg.Name]
				case *ast.BasicLit:
					if arg.Kind == token.STRING {
						v, err := strconv.Unquote(arg.Value)
						s, resolved = v, err == nil
					}
				}
			}
			if !resolved {
				t.Errorf("%s:%d calls %s with a status that is neither a string literal nor a package string constant — the emit-set cannot see what it closes with",
					finalizerFile, fset.Position(call.Pos()).Line, fn.Name)
				return true
			}
			got = append(got, s)
			return true
		})
	}
	walk("FinalizePhase")
	if len(got) == 0 {
		t.Fatal("FinalizePhase reaches no close call — the Phases and Tasks lanes would have no terminal emission")
	}
	return got
}

// derivePhaseStatusReturns resolves every value DerivePhaseStatus can return.
// Returns are written as constant identifiers, so the PACKAGE's string
// constants are collected first (they live in ctx.go, the function in
// phase.go) and the identifiers resolved through them.
func derivePhaseStatusReturns(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	consts := packageStringConsts(t, filepath.Dir(path))

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "DerivePhaseStatus" && fd.Body != nil {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatalf("no DerivePhaseStatus in %s — the emit-set is missing its Go half", path)
	}

	var got []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		switch v := ret.Results[0].(type) {
		case *ast.Ident:
			if s, ok := consts[v.Name]; ok {
				got = append(got, s)
			} else {
				t.Errorf("derivePhaseStatus returns identifier %q, which is not a string constant in %s — the emit-set cannot see what it resolves to", v.Name, path)
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, err := strconv.Unquote(v.Value); err == nil {
					got = append(got, s)
				}
			}
		}
		return true
	})
	if len(got) == 0 {
		t.Fatal("derivePhaseStatus has no resolvable return values")
	}
	return got
}

// stateMapKeys extracts the keys of a package-level map literal from source,
// rather than exporting the map for the test's benefit. Same technique
// enum_divergence_test.go uses on the dispatch switches.
func stateMapKeys(t *testing.T, path, varName string) []string {
	pairs := stateMapPairs(t, path, varName)
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stateMapPairs reads the same declaration as stateMapKeys but keeps the
// VALUES too — which board state each status resolves to. The lane guards below
// need them to ask whether two statuses land in the same column, and the maps
// are unexported in package forge, so source is the only way to read them from
// here.
func stateMapPairs(t *testing.T, path, varName string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	pairs := map[string]string{}
	found := false
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != varName || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s in %s is not a composite literal", varName, path)
				}
				found = true
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					k, ok := kv.Key.(*ast.BasicLit)
					if !ok || k.Kind != token.STRING {
						t.Errorf("%s has a non-literal key %v — this guard can only read string literals", varName, kv.Key)
						continue
					}
					s, err := strconv.Unquote(k.Value)
					if err != nil {
						t.Errorf("%s: unquote %s: %v", varName, k.Value, err)
						continue
					}
					v, ok := kv.Value.(*ast.BasicLit)
					if !ok || v.Kind != token.STRING {
						t.Errorf("%s[%q] has a non-literal value %v — this guard can only read string literals", varName, s, kv.Value)
						continue
					}
					state, err := strconv.Unquote(v.Value)
					if err != nil {
						t.Errorf("%s: unquote %s: %v", varName, v.Value, err)
						continue
					}
					pairs[s] = state
				}
			}
		}
	}
	if !found {
		t.Fatalf("no %s in %s", varName, path)
	}
	return pairs
}

// TestEmittedStatusesAreTheLifecycleSet gates the producer side: what dross
// actually puts on a board is exactly configenum.LifecycleStatuses — no more
// (a status the maps cannot resolve) and no less (a Set member with no caller,
// which is a map key nothing will ever reach).
func TestEmittedStatusesAreTheLifecycleSet(t *testing.T) {
	emitted := emittedStatuses(t)

	for status, where := range emitted {
		if !configenum.LifecycleStatuses.Has(status) {
			t.Errorf("%s emits --status %q, which is not a lifecycle status (expected %s) — no state map will resolve it",
				where, status, configenum.LifecycleStatuses.List())
		}
	}
	for _, want := range configenum.LifecycleStatuses.Values() {
		if _, ok := emitted[want]; !ok {
			t.Errorf("lifecycle status %q is in the Set but nothing emits it — a state-map key nothing will ever reach (emitted: %s)",
				want, strings.Join(sortedSet(emitted), ", "))
		}
	}
}

// TestStateMapsKeyExactlyTheEmittedStatuses gates the map side, per provider.
//
// The two maps are checked independently on purpose: a key present in one and
// missing from the other is a real bug for whoever uses that tracker, and
// checking their union or intersection would average it away.
func TestStateMapsKeyExactlyTheEmittedStatuses(t *testing.T) {
	root := repoRootFromTest(t)
	emitted := emittedStatuses(t)

	for _, m := range []struct {
		provider string
		path     string
		varName  string
	}{
		{"jira", filepath.Join(root, "internal", "forge", "jira.go"), "defaultJiraStateMap"},
		{"youtrack", filepath.Join(root, "internal", "forge", "youtrack.go"), "defaultYouTrackStateMap"},
		{"azure-devops", filepath.Join(root, "internal", "forge", "azureboards_state.go"), "defaultADOStateCategory"},
	} {
		t.Run(m.provider, func(t *testing.T) {
			keys := stateMapKeys(t, m.path, m.varName)
			have := map[string]bool{}
			for _, k := range keys {
				have[k] = true
			}

			for status := range emitted {
				if !have[status] {
					t.Errorf("a status dross emits has no state-map entry: %q is missing from %s (%s emits it)", status, m.varName, emitted[status])
				}
			}
			for _, k := range keys {
				if _, ok := emitted[k]; !ok {
					t.Errorf("a state map keys on a status nothing emits: %s has %q, which no prompt, no derivePhaseStatus branch and no finalizer close produces", m.varName, k)
				}
			}
		})
	}
}

// taskStatusWriteRE matches the plan-side status write at an execute edge:
// `dross task status <phase> <task-id> <plan-status>`.
var taskStatusWriteRE = regexp.MustCompile(`dross task status\s+\S+\s+\S+\s+([a-z_]+)`)

// taskSyncEdgeRE matches the board-side write at the same edge:
// `dross issue task sync <phase> <task-id> --status <lifecycle-status>`.
//
// The task-id argument is required by the pattern, and required not to be a
// flag: a `task sync <phase> --status …` with no task id syncs the whole phase
// at once, which is not an edge and must not satisfy either half below.
var taskSyncEdgeRE = regexp.MustCompile(`dross issue task sync\s+\S+\s+([^-\s]\S*)\s+--status\s+([a-z][a-z-]*)`)

// taskEdges is the pairing c-2 actually claims: picking a task moves its card
// to in-progress, committing it moves the card to a review state. The key is
// the plan status execute writes at that edge; the value is the lifecycle
// status the board write beside it must carry.
var taskEdges = map[string]string{
	"in_progress": "task-in-progress",
	"done":        "task-in-review",
}

// TestTaskEdgesPairTheBoardStatusWithThePlanStatus pins WHICH execute edge
// emits which task status.
//
// TestEmittedStatusesAreTheLifecycleSet above is a `--status <literal>` regex
// over every prompt, so it only ever proves that both task literals appear
// somewhere in the corpus. Transposing execute.md's pick and commit call sites
// would invert c-2 — a picked task's card would jump straight to review and a
// committed one would go back to in-progress — and that guard, and every other
// test, would still pass.
//
// The anchor is the `dross task status` write execute already makes at the same
// edge. That write is what actually defines the edge (in_progress means picked,
// done means committed), so tying the board status to the plan status beside it
// pins the pairing to something that cannot be relabelled without changing what
// the loop does.
func TestTaskEdgesPairTheBoardStatusWithThePlanStatus(t *testing.T) {
	root := repoRootFromTest(t)
	path := filepath.Join(root, "assets", "prompts", "execute.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// Walk the prompt in order, recording both kinds of write as they appear.
	// Order is the whole signal here: a board write belongs to the plan write
	// most recently above it, which is how the two lines of one code fence
	// relate.
	type event struct {
		line   int
		plan   string // set on a `dross task status` write
		board  string // set on a `dross issue task-sync … --status` write
		taskID string
	}
	var events []event
	for i, line := range strings.Split(string(b), "\n") {
		if m := taskStatusWriteRE.FindStringSubmatch(line); m != nil {
			events = append(events, event{line: i + 1, plan: m[1]})
		}
		if m := taskSyncEdgeRE.FindStringSubmatch(line); m != nil {
			events = append(events, event{line: i + 1, board: m[2], taskID: m[1]})
		}
	}

	// paired[planStatus] is the board status found at that edge, so a missing
	// edge and a duplicated one are both visible below.
	paired := map[string][]string{}
	lastPlan := ""
	lastPlanLine := 0
	for _, e := range events {
		if e.plan != "" {
			lastPlan = e.plan
			lastPlanLine = e.line
			continue
		}
		if lastPlan == "" {
			t.Errorf("%s:%d syncs %s to the board with --status %q, but no `dross task status` write precedes it — nothing says which edge this is",
				path, e.line, e.taskID, e.board)
			continue
		}
		want, known := taskEdges[lastPlan]
		if !known {
			t.Errorf("%s:%d writes plan status %q (line %d) and then board status %q, but %q is not an edge this guard knows — add it to taskEdges or the pairing is unchecked",
				path, e.line, lastPlan, lastPlanLine, e.board, lastPlan)
			continue
		}
		if e.board != want {
			t.Errorf("%s:%d emits --status %q at the edge that writes plan status %q (line %d), want %q — picking a task must move its card to %s and committing it to %s",
				path, e.line, e.board, lastPlan, lastPlanLine, want, taskEdges["in_progress"], taskEdges["done"])
		}
		paired[lastPlan] = append(paired[lastPlan], e.board)
	}

	for plan, want := range taskEdges {
		switch got := paired[plan]; len(got) {
		case 1: // the shape we want
		case 0:
			t.Errorf("no board write follows the `dross task status … %s` edge in %s — that edge stops moving the card, and %q would become a lifecycle status nothing emits",
				plan, path, want)
		default:
			t.Errorf("%d board writes follow the `dross task status … %s` edge in %s (%s) — one edge, one card move",
				len(got), plan, path, strings.Join(got, ", "))
		}
	}
}

// sortedSet renders a status->where map's keys for an error message.
func sortedSet(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- mirror-lane terminal-path guard (c-5) ---
//
// The divergence guards above ask whether an emitted status has a state-map
// entry. This asks the question that was never asked: does every KIND of card
// dross mirrors have a way to reach a terminal state at all?
//
// It did not. Task cards sat in task-in-review from their commit until forever,
// epics stayed open behind finished milestones, and backlog mirrors accumulated
// because no prompt called the verb that would have closed them. Each of those
// was invisible for months because the missing thing was a missing LINE — and
// nothing fails when a line is absent unless something goes looking for it.

// mirrorLane says how one board.json namespace's cards reach a terminal state,
// and which lifecycle statuses belong to that lane.
type mirrorLane struct {
	// closedBy names a Go finalizer as the lane's terminal call site instead of
	// a prompt line: the lane is satisfied while that finalizer has a close
	// carrying terminal, and prompt and emission stay empty.
	closedBy string
	// prompt and emission are the terminal call site: which prompt must carry
	// it, and the literal that must appear there. Matched against the RAW file
	// rather than promptContent's normalised form — quick.md's line carries a
	// shell variable, and the normaliser strips underscores, so a normalised
	// match would silently depend on $NEW_VERSION becoming $newversion.
	prompt   string
	emission string
	// terminal is the lifecycle status that emission carries; others are the
	// lane's own non-terminal statuses. Empty terminal means the lane closes
	// without a --status, which is not a gap: see backlog below.
	terminal string
	others   []string
	// reapTerminal is the status `dross issue reap` writes when it closes one
	// of this lane's stranded cards. It lives in the same row as the forward
	// emission on purpose: the locked reap_state decision is that a reaped card
	// lands in the SAME state the forward path writes, so the board stays one
	// coherent history and the state map stays the single source of truth. Kept
	// as its own field rather than reusing `terminal` because the two are not
	// always literally equal — the backlog lane closes without a --status,
	// which closeBoardIssue resolves to "complete" — and collapsing them would
	// hide exactly that case.
	reapTerminal string
}

// mirrorLanes is keyed by board.Board FIELD NAME, and the test below checks
// that key set against reflection over the struct rather than trusting it. A
// namespace added to Board with no entry here fails, which is the point: a new
// kind of mirror must say how its cards end.
var mirrorLanes = map[string]mirrorLane{
	// The Phases and Tasks closes moved from ship.md into the binary:
	// `dross phase complete` runs FinalizePhase, so no agent can stop before
	// them.
	"Phases": {
		closedBy:     finalizerFile,
		terminal:     "complete",
		reapTerminal: "complete",
		others:       []string{"planned", "in-progress", "shipped", "uat"},
	},
	"Tasks": {
		closedBy:     finalizerFile,
		terminal:     "task-complete",
		reapTerminal: "task-complete",
		others:       []string{"task-in-progress", "task-in-review"},
	},
	"Quicks": {
		prompt:   "quick.md",
		emission: "dross issue quick $NEW_VERSION --close",
		terminal: "complete", // closeBoardIssue's default for a lane with no --status
		// The sweep never auto-closes a quick — there is no completion record
		// to read — but the lane still has a terminal, because a quick closed
		// BY HAND off the plan's unattributable list must land in the same
		// column the forward path uses.
		reapTerminal: "complete",
	},
	"Milestones": {
		prompt:       "milestone.md",
		emission:     "dross issue milestone sync <version> --close",
		terminal:     "complete",
		reapTerminal: "complete",
	},
	"Backlog": {
		prompt: "ship.md",
		// The backlog lane has no --close flag by design: backlog sync owns the
		// live set and reconciles both directions, closing the mirrors whose
		// artefact resolved. Asserting a flag here would guard something that
		// was never built.
		emission: "dross issue backlog sync",
		// No forward --status, so no forward terminal — but the sweep does pass
		// one, and it is closeBoardIssue's own default for exactly this case.
		reapTerminal: "complete",
	},
}

// rawPrompt reads one prompt verbatim. promptContent normalises for prose
// assertions; a command line has to be matched as written.
func rawPrompt(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "assets", "prompts", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// laneDistinctnessExemption records a terminal/non-terminal collision that
// predates this guard, so it is visible rather than quietly excluded.
//
// The guard fails if an exemption stops being needed, so a fixed map cannot
// leave a stale exemption behind claiming to protect something.
type laneDistinctnessExemption struct {
	lane, provider, terminal, other, why string
}

var laneDistinctnessExemptions = []laneDistinctnessExemption{
	{
		lane: "Phases", provider: "jira", terminal: "complete", other: "shipped",
		why: "Jira's default scheme has one done-category status, so a merged-but-unfinalized phase and a finished one share \"Done\". Pre-existing flattening; a project that cares splits it via [board].state_map.",
	},
	{
		lane: "Phases", provider: "azure-devops", terminal: "complete", other: "shipped",
		why: "Azure Boards defaults map to state categories, and a merged-but-unfinalized phase is delivered work: shipped lands in Completed, as on Jira, so it shares the Completed category with complete. A project that wants them apart names a state for shipped via [board].state_map.",
	},
}

// boardNamespaceFields enumerates board.Board's map-typed fields — the same
// derivation internal/board's namespace guard uses, from the same struct, so
// the two cannot disagree about how many mirror lanes exist.
func boardNamespaceFields(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeOf(board.Board{})
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Map {
			out = append(out, typ.Field(i).Name)
		}
	}
	if len(out) == 0 {
		t.Fatal("reflection found no map-typed fields on board.Board — every assertion below would be vacuous")
	}
	sort.Strings(out)
	return out
}

// TestEveryMirrorLaneHasATerminalEmission is the guard. Every namespace dross
// mirrors into must have a call site that ends its cards — a prompt line that
// is actually in the corpus, or a finalizer close that carries the lane's
// terminal status.
func TestEveryMirrorLaneHasATerminalEmission(t *testing.T) {
	fields := boardNamespaceFields(t)
	finalized := map[string]bool{}
	for _, s := range finalizerCloses(t) {
		finalized[s] = true
	}

	for _, field := range fields {
		lane, ok := mirrorLanes[field]
		if !ok {
			t.Errorf("board.Board has a %s namespace with no terminal emission recorded — every %s card dross creates would stay open forever; add it to mirrorLanes and give it a closing line in assets/prompts or a finalizer close",
				field, field)
			continue
		}
		t.Run(field, func(t *testing.T) {
			if lane.closedBy != "" {
				if !finalized[lane.terminal] {
					t.Errorf("%s no longer closes a card at %q — %s cards would strand", lane.closedBy, lane.terminal, field)
				}
				return
			}
			content := rawPrompt(t, lane.prompt)
			if !strings.Contains(content, lane.emission) {
				t.Errorf("%s no longer emits %q — %s cards would strand", lane.prompt, lane.emission, field)
			}
		})
	}

	// The reverse: an entry here for a namespace that no longer exists is a
	// guard protecting nothing.
	known := map[string]bool{}
	for _, f := range fields {
		known[f] = true
	}
	for field := range mirrorLanes {
		if !known[field] {
			t.Errorf("mirrorLanes describes %q, which is not a board.Board namespace any more — drop it rather than leaving a guard over nothing", field)
		}
	}
}

// TestLaneTerminalStatesAreDistinctWithinTheirLane checks the property that
// actually matters on a board: a lane's terminal state must be a different
// column from that lane's own working states, or "finished" is invisible.
//
// Per-lane rather than global, deliberately. A global assertion would compare
// the phase lane's `complete` against the task lane's `task-in-review`, which
// share nothing and need not differ — and it would drown the real signal in
// pairs nobody cares about.
func TestLaneTerminalStatesAreDistinctWithinTheirLane(t *testing.T) {
	root := repoRootFromTest(t)
	maps := map[string]map[string]string{}
	for _, m := range []struct{ provider, path, varName string }{
		{"jira", filepath.Join(root, "internal", "forge", "jira.go"), "defaultJiraStateMap"},
		{"youtrack", filepath.Join(root, "internal", "forge", "youtrack.go"), "defaultYouTrackStateMap"},
		{"azure-devops", filepath.Join(root, "internal", "forge", "azureboards_state.go"), "defaultADOStateCategory"},
	} {
		maps[m.provider] = stateMapPairs(t, m.path, m.varName)
	}

	exempt := map[string]string{} // "lane/provider/terminal/other" -> why
	used := map[string]bool{}
	for _, e := range laneDistinctnessExemptions {
		exempt[e.lane+"/"+e.provider+"/"+e.terminal+"/"+e.other] = e.why
	}

	for _, field := range boardNamespaceFields(t) {
		lane, ok := mirrorLanes[field]
		if !ok || lane.terminal == "" || len(lane.others) == 0 {
			continue // TestEveryMirrorLaneHasATerminalEmission owns the missing case
		}
		for provider, states := range maps {
			terminalState, hasTerminal := states[lane.terminal]
			if !hasTerminal {
				t.Errorf("%s's %s map has no entry for the %s lane's terminal status %q — TestStateMapsKeyExactlyTheEmittedStatuses should have caught this",
					provider, "default", field, lane.terminal)
				continue
			}
			for _, other := range lane.others {
				otherState, hasOther := states[other]
				if !hasOther || otherState != terminalState {
					continue
				}
				key := field + "/" + provider + "/" + lane.terminal + "/" + other
				if why, ok := exempt[key]; ok {
					used[key] = true
					t.Logf("known: %s maps the %s lane's %q and %q both to %q — %s", provider, field, lane.terminal, other, terminalState, why)
					continue
				}
				t.Errorf("%s maps the %s lane's terminal %q and its working state %q both to %q — a finished card would be indistinguishable from one still in flight",
					provider, field, lane.terminal, other, terminalState)
			}
		}
	}

	for key, why := range exempt {
		if !used[key] {
			t.Errorf("the exemption for %s no longer describes a real collision (%s) — remove it rather than leaving a guard-hole open", key, why)
		}
	}
}

// closeEmissionRE matches a `--close` call site together with the `--status` it
// carries, in either order.
var closeEmissionRE = regexp.MustCompile(`--status\s+([a-z][a-z-]*)[^\n]*--close|--close[^\n]*--status\s+([a-z][a-z-]*)`)

// TestCloseEmissionsCarryAValidStatus catches a terminal emission typo at guard
// time rather than at ship time. `--status task-complet --close` would exit
// non-zero on a real board — the wrong place to find out, since a ship's
// finalize steps run one after another over a merged PR.
func TestCloseEmissionsCarryAValidStatus(t *testing.T) {
	root := repoRootFromTest(t)
	promptDir := filepath.Join(root, "assets", "prompts")
	entries, err := os.ReadDir(promptDir)
	if err != nil {
		t.Fatalf("read %s: %v", promptDir, err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(promptDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range closeEmissionRE.FindAllStringSubmatch(string(b), -1) {
			status := m[1]
			if status == "" {
				status = m[2]
			}
			checked++
			if !configenum.LifecycleStatuses.Has(status) {
				t.Errorf("assets/prompts/%s closes a card with --status %q, which is not a lifecycle status (expected %s)",
					e.Name(), status, configenum.LifecycleStatuses.List())
			}
		}
	}
	// The finalizer's closes are emissions too — since ship stopped carrying
	// the Phases and Tasks closes, most of them.
	for _, s := range finalizerCloses(t) {
		checked++
		if !configenum.LifecycleStatuses.Has(s) {
			t.Errorf("%s closes a card with status %q, which is not a lifecycle status (expected %s)",
				finalizerFile, s, configenum.LifecycleStatuses.List())
		}
	}
	if checked == 0 {
		t.Fatal("no close emission carrying a status was found in the prompt corpus or the finalizer — every assertion above was vacuous")
	}
}

// --- reap coverage ---
//
// TestTaskSyncEdgeRegexIsNotVacuous: taskSyncEdgeRE drives the emit-set that
// board_lifecycle_divergence_test's execute-edge guard compares against. A
// rename that leaves the regex on the old spelling silently empties that set
// and the lifecycle guard then passes on nothing — a guard measuring zero
// lines is indistinguishable from a guard that agrees.
func TestTaskSyncEdgeRegexIsNotVacuous(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(repoRootFromTest(t), "assets", "prompts", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("globbed no prompts — the guard would pass vacuously")
	}
	matched := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		matched += len(taskSyncEdgeRE.FindAllStringSubmatch(string(b), -1))
	}
	if matched == 0 {
		t.Fatal("taskSyncEdgeRE matches no prompt line — the execute-edge lifecycle guard is running on an empty set")
	}
}

// TestEveryMirrorLaneHasATerminalEmission above asks whether every mirror class
// has a way FORWARD to a terminal state. These ask the other half: whether
// every mirror class has a way BACK — a reap path for the cards the forward
// lifecycle already left behind.
//
// The two questions are separate because the answers were: the forward
// emissions were added when the lifecycle was fixed, and the ninety cards that
// predate them are not reachable by any of those lines. A namespace can have a
// perfect forward path and still have no way to close its history.

// TestEveryMirrorLaneHasATerminalEmission above asks whether every mirror class
// has a way FORWARD to a terminal state. These ask the other half: whether
// every mirror class has a way BACK — a reap path for the cards the forward
// lifecycle already left behind.
//
// The two questions are separate because the answers were: the forward
// emissions were added when the lifecycle was fixed, and the ninety cards that
// predate them are not reachable by any of those lines. A namespace can have a
// perfect forward path and still have no way to close its history.

// TestEveryBoardNamespaceHasAReapPath: a map field added to board.Board with no
// reap lane fails by field name in the same run that adds it.
func TestEveryBoardNamespaceHasAReapPath(t *testing.T) {
	fields := boardNamespaceFields(t)
	for _, field := range fields {
		lane, ok := ReapLaneFor(field)
		if !ok {
			t.Errorf("board.Board has a %s namespace with no reap lane — every stranded %s card would be unreachable by the sweep; add it to ReapLanes", field, field)
			continue
		}
		if lane.Terminal == "" {
			t.Errorf("the %s reap lane declares no terminal status — the sweep would have nothing to write", field)
		}
	}

	// The reverse: a lane naming a namespace that no longer exists is a
	// classifier arm nothing can ever reach.
	known := map[string]bool{}
	for _, f := range fields {
		known[f] = true
	}
	for _, lane := range ReapLanes {
		if !known[lane.Name] {
			t.Errorf("ReapLanes describes %q, which is not a board.Board namespace any more — drop it rather than leaving a classifier arm over nothing", lane.Name)
		}
	}
	if len(ReapLanes) == 0 {
		t.Fatal("the reap lane registry is empty — every assertion here would be vacuous")
	}
}

// TestReapTerminalMatchesTheForwardTerminal holds the locked reap_state
// decision: a reaped card lands in the same state the forward lifecycle writes
// for its class. A sweep-specific state would make the board two histories and
// double the mapping — and the mapping is the thing the previous phase made
// trustworthy.
func TestReapTerminalMatchesTheForwardTerminal(t *testing.T) {
	checked := 0
	for _, field := range boardNamespaceFields(t) {
		lane, ok := ReapLaneFor(field)
		if !ok {
			continue // TestEveryBoardNamespaceHasAReapPath owns the missing case
		}
		row, ok := mirrorLanes[field]
		if !ok {
			continue // TestEveryMirrorLaneHasATerminalEmission owns that one
		}
		checked++
		if row.reapTerminal != lane.Terminal {
			t.Errorf("the %s lane reaps to %q but the registry records %q — the two tables have drifted", field, lane.Terminal, row.reapTerminal)
		}
		// The backlog lane is the one place the forward path carries no
		// --status, so its forward terminal is empty and only the reap one is
		// set. Everywhere else the two must be the same column.
		if row.terminal != "" && row.terminal != row.reapTerminal {
			t.Errorf("the %s lane ends forward at %q but reaps to %q — reap_state says a reaped card lands where the forward path puts it", field, row.terminal, row.reapTerminal)
		}
	}
	if checked == 0 {
		t.Fatal("compared no lanes — the guard would pass vacuously")
	}
}

// TestEveryReapTerminalIsMapped: a terminal nothing can resolve is a close that
// fails on a live board, per provider. Both default maps are checked
// independently, and the failure names which provider is missing which status.
func TestEveryReapTerminalIsMapped(t *testing.T) {
	root := repoRootFromTest(t)
	maps := map[string]map[string]string{
		"jira":         stateMapPairs(t, filepath.Join(root, "internal", "forge", "jira.go"), "defaultJiraStateMap"),
		"youtrack":     stateMapPairs(t, filepath.Join(root, "internal", "forge", "youtrack.go"), "defaultYouTrackStateMap"),
		"azure-devops": stateMapPairs(t, filepath.Join(root, "internal", "forge", "azureboards_state.go"), "defaultADOStateCategory"),
	}
	for provider, states := range maps {
		if len(states) == 0 {
			t.Fatalf("parsed no state-map pairs for %s — every assertion below would be vacuous", provider)
		}
	}

	for _, lane := range ReapLanes {
		if !configenum.LifecycleStatuses.Has(lane.Terminal) {
			t.Errorf("the %s lane reaps to %q, which is not a lifecycle status (%s) — nothing downstream could validate it", lane.Name, lane.Terminal, configenum.LifecycleStatuses.List())
			continue
		}
		for provider, states := range maps {
			if _, ok := states[lane.Terminal]; !ok {
				t.Errorf("%s has no state-map entry for %q, the %s lane's reap terminal — every close in that lane would fail on a %s board", provider, lane.Terminal, lane.Name, provider)
			}
		}
	}
}

// repoRootFromTest walks up from the package directory to the module root.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find module root (no go.mod above %s)", dir)
		}
		dir = parent
	}
}
