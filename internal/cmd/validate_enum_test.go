package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/project"
)

// forceEnumValue writes an enum-valued key straight into project.toml, past the
// `project set` gate.
//
// The bypass is the point, not a shortcut. Since t-2 closed the setter, an
// out-of-set value CANNOT be created through the CLI — which is exactly why
// validate still has to check the file. A repo arrives in this state by
// hand-editing project.toml, by cloning one, or by carrying a value that
// predates the set becoming closed. Building the fixture through the setter
// would be building a state that no longer exists, and the test would prove
// nothing about the file nobody typed.
func forceEnumValue(t *testing.T, dir, key, value string) {
	t.Helper()
	path := filepath.Join(dir, ".dross", project.File)
	p, err := project.Load(path)
	if err != nil {
		t.Fatalf("load project.toml: %v", err)
	}
	switch key {
	case "runtime.mode":
		p.Runtime.Mode = value
	case "repo.layout":
		p.Repo.Layout = value
	case "repo.commit_convention":
		p.Repo.CommitConvention = value
	case "remote.provider":
		p.Remote.Provider = value
	case "board.milestone_mode":
		p.Board.MilestoneMode = value
	case "board.auth_scheme":
		p.Board.AuthScheme = value
	default:
		t.Fatalf("no forced-write arm for %q", key)
	}
	if err := p.Save(path); err != nil {
		t.Fatalf("save project.toml: %v", err)
	}
}

// enumFixtureRepo returns a temp repo whose project.toml is otherwise clean, so
// any problem validate reports is the one the test planted.
func enumFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	mustRunSet(t, "project.name", "test-app")
	mustRunSet(t, "runtime.mode", "native")
	return dir
}

// TestValidateFailsOnOutOfSetEnum is c-2: a value that never passed through
// `project set` is caught anyway. One planted garbage value per key, each in its
// own fixture so the reported problem can only be that key's.
func TestValidateFailsOnOutOfSetEnum(t *testing.T) {
	for _, tc := range []struct {
		key  string
		bad  string
		want []string // values the message must offer instead
	}{
		{"runtime.mode", "banana", []string{"docker", "native"}},
		{"repo.layout", "sideways", []string{"single", "monorepo"}},
		{"repo.commit_convention", "nonsense", []string{"conventional", "freeform"}},
		{"remote.provider", "notaforge", []string{"github", "gitlab"}},
		{"board.milestone_mode", "bogus", []string{"version", "agile", "epic"}},
		{"board.auth_scheme", "token", []string{"private-token", "bearer", "basic"}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			dir := enumFixtureRepo(t)
			forceEnumValue(t, dir, tc.key, tc.bad)

			var out string
			err := runCmdCapturing(t, &out, Validate())
			if err == nil {
				t.Fatalf("validate passed a project.toml holding %s = %q", tc.key, tc.bad)
			}
			// Naming both halves is what makes the problem actionable: the key
			// says where to look, the value says what to look for.
			if !strings.Contains(out, tc.key) {
				t.Errorf("problem does not name the key %q:\n%s", tc.key, out)
			}
			if !strings.Contains(out, tc.bad) {
				t.Errorf("problem does not quote the offending value %q:\n%s", tc.bad, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("problem does not offer the allowed value %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestValidateAcceptsEmptyEnumValues: empty means unset, and every one of these
// keys is legitimately absent in a partially-filled project.toml — which is
// every project.toml between init and the first full options pass. Reddening on
// empty would fail all of them.
//
// runtime.mode is excluded deliberately: empty is a problem there, reported by
// its own check, and this test asserts the enum walk is not the thing reporting
// it.
func TestValidateAcceptsEmptyEnumValues(t *testing.T) {
	dir := enumFixtureRepo(t)
	for _, key := range []string{
		"repo.layout",
		"repo.commit_convention",
		"remote.provider",
		"board.milestone_mode",
	} {
		forceEnumValue(t, dir, key, "")
	}

	var out string
	if err := runCmdCapturing(t, &out, Validate()); err != nil {
		t.Fatalf("validate failed on an empty (unset) enum field: %v\n%s", err, out)
	}
}

// TestValidateAndSetShareTheEnumSets is c-5 stated as behaviour rather than as
// a code-shape claim: drive every member of every set, plus a non-member,
// through BOTH gates and require them to agree.
//
// If validate ever restates its own list, this is what fails — a member the
// setter accepts and validate rejects would make a project.toml that dross
// wrote fail dross's own check.
func TestValidateAndSetShareTheEnumSets(t *testing.T) {
	for key, set := range enumKeys {
		for _, v := range set.Values() {
			t.Run(key+"="+v, func(t *testing.T) {
				dir := enumFixtureRepo(t)
				if err := runCmd(t, Project(), "set", key, v); err != nil {
					t.Fatalf("set refused the member %q: %v", v, err)
				}
				var out string
				if err := runCmdCapturing(t, &out, Validate()); err != nil {
					t.Errorf("set accepted %s=%q but validate rejected it: %v\n%s", key, v, err, out)
				}
				_ = dir
			})
		}

		t.Run(key+"=nonmember", func(t *testing.T) {
			dir := enumFixtureRepo(t)
			if err := runCmd(t, Project(), "set", key, "definitely-not-a-member"); err == nil {
				t.Fatalf("set accepted a non-member for %s", key)
			}
			forceEnumValue(t, dir, key, "definitely-not-a-member")
			var out string
			if err := runCmdCapturing(t, &out, Validate()); err == nil {
				t.Errorf("set refused a non-member for %s but validate passed it:\n%s", key, out)
			}
		})
	}
}
