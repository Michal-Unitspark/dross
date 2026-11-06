package cmd

import (
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/project"
)

// enumFixture returns a Project with every enum key holding a known-good value,
// so a test can prove a refused write left the prior value in place.
func enumFixture() *project.Project {
	p := &project.Project{}
	p.Runtime.Mode = "native"
	p.Repo.Layout = "single"
	p.Repo.CommitConvention = "conventional"
	p.Remote.Provider = "github"
	p.Board.MilestoneMode = "version"
	p.Board.AuthScheme = "bearer"
	return p
}

func enumKeyValue(p *project.Project, path string) string {
	switch path {
	case "runtime.mode":
		return p.Runtime.Mode
	case "repo.layout":
		return p.Repo.Layout
	case "repo.commit_convention":
		return p.Repo.CommitConvention
	case "remote.provider":
		return p.Remote.Provider
	case "board.milestone_mode":
		return p.Board.MilestoneMode
	case "board.auth_scheme":
		return p.Board.AuthScheme
	}
	return ""
}

// TestProjectSetRejectsOutOfSetValues is the criterion, stated over all five
// keys at once.
//
// Every one of them accepted `banana` and persisted it: set wrote it, get read
// it back, and validate called the project clean. A config value that can hold
// anything is not configuration, it is a comment.
func TestProjectSetRejectsOutOfSetValues(t *testing.T) {
	for _, path := range []string{
		"runtime.mode",
		"repo.layout",
		"repo.commit_convention",
		"remote.provider",
		"board.milestone_mode",
		"board.auth_scheme",
	} {
		t.Run(path, func(t *testing.T) {
			p := enumFixture()
			before := enumKeyValue(p, path)

			err := writeDotted(p, path, "banana")
			if err == nil {
				t.Fatalf("%s accepted banana", path)
			}
			// The message has to be actionable. "invalid value" sends the user
			// to the source; naming the key and listing the members does not.
			if !strings.Contains(err.Error(), path) {
				t.Errorf("refusal does not name the key: %v", err)
			}
			if !strings.Contains(err.Error(), "banana") {
				t.Errorf("refusal does not quote the offending value: %v", err)
			}
			for _, want := range enumKeys[path].Values() {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not list allowed value %q: %v", want, err)
				}
			}
			// TestProjectSetRejectionWritesNothing's claim, per key: the check
			// runs before the write, so a refusal cannot leave a partial edit.
			if got := enumKeyValue(p, path); got != before {
				t.Errorf("a refused write still changed the field: %q -> %q", before, got)
			}
		})
	}
}

// TestProjectSetRejectionWritesNothing proves the ordering directly rather than
// as a side assertion: a rejected value that still landed in project.toml would
// be the same lie in a new place.
func TestProjectSetRejectionWritesNothing(t *testing.T) {
	p := enumFixture()
	if err := writeDotted(p, "runtime.mode", "hybrid"); err == nil {
		t.Fatal("hybrid was accepted")
	}
	if p.Runtime.Mode != "native" {
		t.Errorf("runtime.mode = %q after a refused write, want the prior native", p.Runtime.Mode)
	}
}

// TestProjectSetStillAcceptsEveryEnumMember is what stops the gate from being a
// blanket refusal. Every declared member of every set must still go through —
// a check that rejects everything passes the test above and breaks the tool.
func TestProjectSetStillAcceptsEveryEnumMember(t *testing.T) {
	for path, set := range enumKeys {
		for _, v := range set.Values() {
			p := enumFixture()
			if err := writeDotted(p, path, v); err != nil {
				t.Errorf("%s rejected its own member %q: %v", path, v, err)
			}
		}
		// Empty means "unset this" and must survive: every project.toml between
		// init and the first full options pass is partially filled.
		p := enumFixture()
		if err := writeDotted(p, path, ""); err != nil {
			t.Errorf("%s rejected an empty (unset) value: %v", path, err)
		}
	}
}

// TestFreeTextKeysStayFreeText: only the enum keys are gated. A command line is
// whatever the repo says it is, and gating one would be dross deciding what a
// user's test runner may be called.
func TestFreeTextKeysStayFreeText(t *testing.T) {
	for _, path := range []string{
		"runtime.test_command",
		"runtime.dev_command",
		"project.description",
		"repo.branch_pattern",
	} {
		p := enumFixture()
		if err := writeDotted(p, path, "banana --with-flags"); err != nil {
			t.Errorf("free-text key %s was gated: %v", path, err)
		}
	}
}

// TestEnumKeysTableCoversEveryEnumTypedField is the drift guard on the table
// itself. A new enum-valued field added to project.toml without an entry here
// is unchecked by both gates, which is precisely the state this phase found.
func TestEnumKeysTableCoversEveryEnumTypedField(t *testing.T) {
	want := map[string]configenum.Set{
		"runtime.mode":           configenum.RuntimeModes,
		"repo.layout":            configenum.RepoLayouts,
		"repo.commit_convention": configenum.CommitConventions,
		"remote.provider":        configenum.ShipProviders,
		"board.milestone_mode":   configenum.MilestoneModes,
		"board.auth_scheme":      configenum.AuthSchemes,
	}
	for path := range want {
		if _, ok := enumKeys[path]; !ok {
			t.Errorf("enumKeys lost %q — it is settable and unchecked again", path)
		}
	}
	for path := range enumKeys {
		if _, ok := want[path]; !ok {
			t.Errorf("enumKeys gained %q with no test acknowledging it", path)
		}
	}
}
