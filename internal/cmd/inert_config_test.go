package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file is c-4: no settable project.toml key is inert.
//
// A key that `dross project set` accepts, `dross project show` prints and
// options.md offers, but that nothing ever reads, is a lie with a UI. The user
// types a value, the tool says OK, and nothing happens — which is worse than
// the key not existing, because the absence would have been visible.
//
// The guard walks the SETTER'S OWN case list rather than a hand-typed roster,
// so a key added tomorrow is covered without anyone remembering to add it here.

// inertKeyReasons is the exemption list, and it is deliberately small and
// individually argued.
//
// Per the locked inert_key_disposition decision this is NOT a dumping ground: an
// inert key is normally resolved by giving it a reader or taking it off the
// settable surface, and a key earns a line here only with a stated reason. A
// blanket "known inert" list would turn this whole file into a formality — the
// next inert key gets appended and the test keeps passing, which is exactly the
// failure that produced this phase.
//
// Shrinking this map is the point. Every entry is a debt with a name.
var inertKeyReasons = map[string]string{
	// The eight runtime commands. Recorded as one argument because they share
	// one fate: dross has no verb that runs them. Giving them a reader means
	// building `dross run <name>`, which is a feature question (what should
	// dross DO with a dev_command?) and not a truth question — so it is carried
	// as an explicit [[deferred]] item in this phase's spec rather than
	// invented here. They stay settable because the value is still true
	// documentation of the repo that a human and an agent both read.

	// The stack descriptors. dross detects the stack from the filesystem
	// (internal/stack, internal/profile) and never consults these, so they
	// describe the repo for a human and for an agent reading project.toml
	// rather than steering any dross code path.
	"stack.languages":    "dross detects languages from the filesystem (internal/profile markers); this field describes the repo for a reader and steers nothing",
	"stack.frameworks":   "dross detects the stack from the filesystem; this field describes the repo for a reader and steers nothing",
	"stack.type_checker": "the command dross runs is runtime.typecheck_command; this names the tool for a reader and steers nothing",
	"stack.linter":       "the command dross runs is runtime.lint_command; this names the tool for a reader and steers nothing",
	"stack.formatter":    "the command dross runs is runtime.format_command; this names the tool for a reader and steers nothing",
	"stack.test_runner":  "the command dross runs is runtime.test_command; this names the tool for a reader and steers nothing",
	"stack.e2e_runner":   "the command dross runs is runtime.e2e_command; this names the tool for a reader and steers nothing",

	// Paths dross has no code path for. paths.source and paths.tests ARE
	// consumed (plan.md and verify.md branch on them); these three are the
	// remainder, kept for symmetry of the section and read by nothing.
	"paths.migrations": "no dross code path or prompt reads it — kept as repo description; remove it if the paths section is ever trimmed",
	"paths.schemas":    "no dross code path or prompt reads it — kept as repo description; remove it if the paths section is ever trimmed",
	"paths.public":     "no dross code path or prompt reads it — kept as repo description; remove it if the paths section is ever trimmed",

	// Repo shape fields with no consumer.
	"repo.layout":       "monorepo handling is driven by repo.workspaces and paths.*, not by this label; it records the shape for a reader",
	"repo.workspaces":   "no dross code path reads it — monorepo support is not built; recorded so the shape is captured when it is",
	"repo.root_run_dir": "no dross code path reads it — commands run from the repo root today; recorded for the monorepo work that will need it",

	// Remainder.
	"remote.public":        "no dross code path branches on it — visibility is the forge's fact, and dross reads it from the API when it needs it",
	"goals.audience":       "the prompts that read [goals] read core_value and non_goals; audience is captured for a human reading project.toml",
	"env.secrets_location": "options.md branches on env.files and env.gitignored but not on this; it is prose for a reader",

	// Path descriptors with no consumer. These three, plus stack.e2e_runner
	// above, were inert for their whole lives and never listed — the key-parsing
	// regex was digit-blind ([a-z_]+), so no key with a number in its name ever
	// entered the set this guard checks. runtime-command-runner fixed the regex,
	// which is what surfaced them.
	"paths.e2e":  "no dross code path reads it — mutation scoping works from the phase's changed files, not from a declared directory; it records the layout for a reader",
	"paths.i18n": "no dross code path reads it — nothing in dross is locale-aware; it records the layout for a reader",
}

// TestNoSettableKeyIsInert is the guard.
//
// Two directions, both required. A settable key with no reader and no recorded
// reason fails — that is the new-inert-key case. A recorded reason for a key
// that now HAS a reader also fails — that is what makes the exemption list
// shrink instead of ossify.
func TestNoSettableKeyIsInert(t *testing.T) {
	root := repoRootForHybridTest(t)
	keys := settableKeys(t, root)
	if len(keys) < 40 {
		t.Fatalf("only parsed %d settable keys from writeDotted — the parser has drifted and this guard is vacuous", len(keys))
	}

	consumed := map[string]bool{}
	for _, k := range keys {
		consumed[k] = keyIsConsumed(t, root, k)
	}

	var inert []string
	for _, k := range keys {
		if consumed[k] {
			continue
		}
		if _, ok := inertKeyReasons[k]; !ok {
			inert = append(inert, k)
		}
	}
	sort.Strings(inert)
	for _, k := range inert {
		t.Errorf("%s is settable but nothing reads it — give it a reader, take it off the setter, or add a stated reason to inertKeyReasons", k)
	}

	// The other direction: a reason that has outlived its key.
	for k := range inertKeyReasons {
		if consumed[k] {
			t.Errorf("%s now has a reader — drop its inertKeyReasons entry; the list only ever shrinks", k)
		}
		if !containsString(keys, k) {
			t.Errorf("inertKeyReasons names %q, which is not settable — drop the entry", k)
		}
	}
}

// TestInertKeyReasonsAreStated: a key is exempt only with a real why.
//
// An entry with an empty or throwaway reason is the dumping-ground failure mode
// wearing the shape of compliance, so the prose is checked rather than merely
// required to exist.
func TestInertKeyReasonsAreStated(t *testing.T) {
	placeholders := []string{"tbd", "todo", "n/a", "none", "known", "wip", "?"}
	for k, why := range inertKeyReasons {
		trimmed := strings.TrimSpace(why)
		if len(trimmed) < 40 {
			t.Errorf("%s: reason is too short to be a reason (%q)", k, trimmed)
			continue
		}
		low := strings.ToLower(trimmed)
		for _, p := range placeholders {
			if low == p || strings.HasPrefix(low, p+" ") {
				t.Errorf("%s: %q is a placeholder, not a stated reason", k, trimmed)
			}
		}
	}
}

// TestListingsDoNotCountAsConsumption is the locked
// prompt_listing_is_not_consumption decision, tested directly.
//
// init.md carries a `dross project set <key>` line and options.md carries a
// `Fields:` enumeration for EVERY settable key by construction. If either
// counted as a reader, this guard would pass for exactly the keys it exists to
// catch. The planted lines below are the real shapes those two files use.
func TestListingsDoNotCountAsConsumption(t *testing.T) {
	listings := []string{
		`dross project set runtime.dev_command       "<exact command>"`,
		"Fields: `runtime.mode`, `runtime.dev_command`, `runtime.stop_command`.",
		"Fields: `paths.source`, `paths.tests`, `paths.e2e`.",
		"Recorded only: `runtime.dev_command`, `stack.languages`, `paths.schemas`.",
	}
	for _, line := range listings {
		if !isListingLine(line) {
			t.Errorf("a listing was counted as consumption: %q", line)
		}
	}

	// The counterexamples: prompts that actually branch on or read a value.
	// These must NOT be filtered out, or the guard becomes a blanket failure.
	branches := []string{
		"For each file in `env.files`, check it exists and is in `.gitignore` if `env.gitignored = true`.",
		"- Existing relevant files in `paths.source` for patterns",
		"- `.dross/project.toml` — `goals.core_value`, `goals.non_goals`, `stack.locked`, `runtime.mode`",
	}
	for _, line := range branches {
		if isListingLine(line) {
			t.Errorf("a prompt that reads the value was dismissed as a listing: %q", line)
		}
	}
}

// --- helpers -------------------------------------------------------------

var (
	// [a-z0-9_] and not [a-z_]: the digit-blind version silently dropped every
	// key with a number in its name from the set this guard checks, so
	// runtime.e2e_command, stack.e2e_runner, paths.e2e and paths.i18n were
	// invisible to the very test that exists to catch unread keys —
	// runtime.e2e_command was inert for its whole life and never listed as such.
	setterCaseRE = regexp.MustCompile(`(?m)^\tcase "([a-z0-9_]+\.[a-z0-9_]+)":`)
	getterCaseRE = regexp.MustCompile(`case "([a-z0-9_]+\.[a-z0-9_]+)":\s*\n\s*return [^\n]*?p\.([A-Z][A-Za-z0-9]*\.[A-Z][A-Za-z0-9]*)`)
	// A listing is init.md's setter line, options.md's `Fields:` enumeration,
	// or options.md's `Recorded only:` note. The first two exist for every
	// settable key whether or not anything reads it; the third exists for
	// exactly the keys nothing reads, so counting any of them as consumption
	// would make this guard pass for precisely the keys it must catch.
	listingRE = regexp.MustCompile(`(^|\s)dross project set\s|^\s*Fields:\s|^\s*Recorded only:\s`)
)

// settableKeys parses writeDotted's own case labels, so the guard cannot fall
// behind the setter. A hand-typed roster would go stale the first time someone
// added a key, which is the failure mode this whole phase is about.
func settableKeys(t *testing.T, root string) []string {
	t.Helper()
	src := mustReadFile(t, filepath.Join(root, "internal", "cmd", "project.go"))
	body := funcBody(t, src, "func writeDotted")
	var keys []string
	for _, m := range setterCaseRE.FindAllStringSubmatch(body, -1) {
		keys = append(keys, m[1])
	}
	sort.Strings(keys)
	return keys
}

// fieldSelector maps a dotted key to its Go struct path (e.g. runtime.mode ->
// Runtime.Mode) by parsing readDotted, so the mapping is derived rather than
// restated.
func fieldSelector(t *testing.T, root, key string) string {
	t.Helper()
	src := mustReadFile(t, filepath.Join(root, "internal", "cmd", "project.go"))
	body := funcBody(t, src, "func readDotted")
	for _, m := range getterCaseRE.FindAllStringSubmatch(body, -1) {
		if m[1] == key {
			return m[2]
		}
	}
	return ""
}

// keyIsConsumed reports whether anything actually reads the key: Go product
// code touching the struct field, or a prompt that does something with the
// value beyond listing it.
func keyIsConsumed(t *testing.T, root, key string) bool {
	t.Helper()
	if sel := fieldSelector(t, root, key); sel != "" && goReaderExists(t, root, sel) {
		return true
	}
	return promptReaderExists(t, root, key)
}

// goReaderExists looks for `.Section.Field` anywhere in the module's product
// code except project.go itself — the getter and setter are the key's own
// plumbing, not a consumer of its value — and except tests, since a test
// reading a field does not make the value do anything.
func goReaderExists(t *testing.T, root, selector string) bool {
	t.Helper()
	needle := "." + selector
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found {
			return nil //nolint:nilerr // a walk error on one dir must not fail the guard
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == ".dross" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.ToSlash(path) == filepath.ToSlash(filepath.Join(root, "internal", "cmd", "project.go")) {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(b), needle) {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk for %s: %v", selector, err)
	}
	return found
}

// promptReaderExists reports whether any prompt does more than list the key.
func promptReaderExists(t *testing.T, root, key string) bool {
	t.Helper()
	dir := filepath.Join(root, "assets", "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read prompts: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body := mustReadFile(t, filepath.Join(dir, e.Name()))
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, key) {
				continue
			}
			if isListingLine(line) {
				continue
			}
			return true
		}
	}
	return false
}

func isListingLine(line string) bool { return listingRE.MatchString(line) }

// funcBody returns the source text of a top-level func, from its header to the
// first line that is exactly "}".
func funcBody(t *testing.T, src, header string) string {
	t.Helper()
	i := strings.Index(src, header)
	if i < 0 {
		t.Fatalf("could not find %q in project.go — the guard's parser has drifted", header)
	}
	rest := src[i:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %q", header)
	}
	return rest[:end]
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
