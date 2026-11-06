package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/boardsync"
	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/phase"
)

// Friction-window regression pins (c-7).
//
// One subtest per friction case recorded in the 2026-07-15..2026-07-28
// telemetry window — the classes the v1.1 milestone exists to stop seeing.
// Each asserts the message a user gets *now*, so the fix cannot rot back into
// the shape that produced the events.
//
// Five of the six were already fixed by earlier phases (root-robustness,
// cli-surface-sweep, validator-truth); this file is their regression surface,
// not their implementation. Only the unmapped board state is new work, landed
// by t-1 in this phase. Worth knowing when reading the verify report: c-7 is
// satisfied by pinning, and a green run here says the fixes still hold, not
// that they were made here.

// TestFrictionWindow_IncompleteRoot: `dross task list` in a directory whose
// .dross/ exists but is missing project.toml used to fail with a bare
// stat/parse error, which reads as a broken tool rather than an unfinished
// setup.
//
// project.toml is the whole of the required set now. A missing state.json is
// the fresh-clone shape, not an unfinished setup — it is machine-local and
// gitignored (locked state_tracking), so the run must materialize it and carry
// on rather than turning the user away.
func TestFrictionWindow_IncompleteRoot(t *testing.T) {
	t.Run("missing project.toml is an incomplete root", func(t *testing.T) {
		dir := t.TempDir()
		chdir(t, dir)
		if err := runCmd(t, Init()); err != nil {
			t.Fatalf("init: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, ".dross", "project.toml")); err != nil {
			t.Fatal(err)
		}

		err := runCmd(t, Task(), "list")
		if err == nil {
			t.Fatal("task list on an incomplete root must fail")
		}
		msg := err.Error()
		if !strings.Contains(msg, "project.toml") {
			t.Errorf("error %q does not name the missing file", msg)
		}
		if !strings.Contains(msg, RepairHint) {
			t.Errorf("error %q carries no repair hint (want %q)", msg, RepairHint)
		}
		if !strings.Contains(msg, "dross onboard") {
			t.Errorf("error %q does not name the command that repairs it", msg)
		}
	})

	t.Run("missing state.json is materialized, not refused", func(t *testing.T) {
		dir := t.TempDir()
		chdir(t, dir)
		if err := runCmd(t, Init()); err != nil {
			t.Fatalf("init: %v", err)
		}
		stPath := filepath.Join(dir, ".dross", "state.json")
		if err := os.Remove(stPath); err != nil {
			t.Fatal(err)
		}

		// It still fails — there is no phase to list tasks for — but on its own
		// terms, never as a not-a-dross-repo. The RepairHint is the whole
		// discriminator: a root that still demands state.json carries it here.
		err := runCmd(t, Task(), "list")
		if err != nil && strings.Contains(err.Error(), RepairHint) {
			t.Errorf("a state-less root must not read as an unfinished setup, got: %v", err)
		}
		if _, err := os.Stat(stPath); err != nil {
			t.Errorf("state.json should have been materialized: %v", err)
		}
	})
}

// TestFrictionWindow_TaskListExists: `dross task list` was reached for
// repeatedly and did not exist, so every call landed in the
// unknown_subcommand bucket. It must resolve — with and without an explicit
// phase id.
func TestFrictionWindow_TaskListExists(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "01-auth", "plan.toml"), `
[phase]
id = "01-auth"

[[task]]
id = "t-1"
wave = 1
title = "schema"
files = ["a.go"]
status = "done"

[[task]]
id = "t-2"
wave = 1
title = "handler"
files = ["b.go"]
`)

	out := captureStdout(t, func() {
		if err := runCmd(t, Task(), "list", "01-auth"); err != nil {
			t.Fatalf("task list 01-auth: %v", err)
		}
	})
	for _, id := range []string{"t-1", "t-2"} {
		if !strings.Contains(out, id) {
			t.Errorf("task list output missing %q:\n%s", id, out)
		}
	}
}

// TestFrictionWindow_TaskDoneNamesTheRealCommand: `dross task done` does not
// exist and never will — the verb is `task status <id> done`. The mis-reach
// must be answered with the working invocation rather than a bare
// unknown-subcommand error.
func TestFrictionWindow_TaskDoneHint(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Through the assembled root: the curated mis-reach table is installed by
	// EnforceSubcommandKnown on the root, which is where a real invocation
	// enters, so calling Task() directly would only ever see cobra's own
	// message.
	err := runCmd(t, hintedTree(), "task", "done", "01-auth", "t-1")
	if err == nil {
		t.Fatal("`task done` must fail — it is not a command")
	}
	if !strings.Contains(err.Error(), "dross task status <phase-id> <task-id> done") {
		t.Errorf("error %q does not name the working invocation", err)
	}
}

// TestFrictionWindow_MilestoneStatusIsSettable: setting [milestone].status
// through the CLI produced unknown_field errors, forcing hand-edits of the
// toml. It is now a validated dotted path.
func TestFrictionWindow_MilestoneStatusIsSettable(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	path := filepath.Join(dir, ".dross", "milestones", "v1.1.toml")
	mustWrite(t, path, `
[milestone]
version = "v1.1"
title = "Friction pass"
status = "planning"

[scope]
success_criteria = ["ships"]
`)

	if err := runCmd(t, Milestone(), "set", "v1.1", "milestone.status", "active"); err != nil {
		t.Fatalf("milestone set milestone.status active: %v", err)
	}
	if body := mustRead(t, path); !strings.Contains(body, `status = "active"`) {
		t.Errorf("status not written:\n%s", body)
	}

	err := runCmd(t, Milestone(), "set", "v1.1", "milestone.status", "bogus")
	if err == nil {
		t.Fatal("an out-of-set milestone status must be rejected")
	}
	if !strings.Contains(err.Error(), configenum.MilestoneStatuses.List()) {
		t.Errorf("error %q does not name the valid set %q", err, configenum.MilestoneStatuses.List())
	}
	if strings.Contains(err.Error(), "unknown field") {
		t.Errorf("the path is still unrecognised rather than the value rejected: %v", err)
	}
}

// TestFrictionWindow_DoctorAcceptsEveryDispatchableProvider: doctor rejected
// board providers the CLI dispatches happily (jira, github), so a working
// configuration failed its own health check.
func TestFrictionWindow_DoctorAcceptsWorkingProviders(t *testing.T) {
	const tokenEnv = "DROSS_TEST_FRICTION_TOKEN"

	for _, provider := range configenum.BoardProviders.Values() {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir, "https://gitlab.com/Rivil/dross.git")
			chdir(t, dir)
			if err := runCmd(t, Init()); err != nil {
				t.Fatalf("init: %v", err)
			}
			// azure-devops is checked for an org URL on an allowed host;
			// dev.azure.com is a built-in one. Its client is real, so the
			// case runs with no network rather than dial it.
			baseURL := "https://board.example.com"
			if provider == "azure-devops" {
				baseURL = "https://dev.azure.com/acme"
				noNetwork(t)
			}
			for _, kv := range [][2]string{
				{"remote.auth_env", tokenEnv},
				{"board.provider", provider},
				{"board.base_url", baseURL},
				{"board.auth_env", tokenEnv},
				{"board.project", "PROJ"},
				{"board.enabled", "true"},
			} {
				if err := runCmd(t, Project(), "set", kv[0], kv[1]); err != nil {
					t.Fatalf("project set %s: %v", kv[0], err)
				}
			}
			t.Setenv(tokenEnv, "secret")

			var out string
			err := runCmdCapturing(t, &out, Doctor())
			if strings.Contains(out, "[board].provider") {
				t.Errorf("doctor rejected %q, which forge.NewBoard dispatches:\n%s", provider, out)
			}
			// With a complete block, the verdict is a clean board — not merely
			// "no provider complaint".
			if !strings.Contains(out, "✓ [board] is well-formed") {
				t.Errorf("a complete [board] block for %q is not well-formed (err=%v):\n%s", provider, err, out)
			}
		})
	}
}

// TestFrictionWindow_BoardStateResolves is the one case this phase fixes
// rather than pins. A phase whose plan is locked but unstarted derived the
// status "planning" while both state maps keyed "planned", so the board
// transition was skipped with a warning on every such sync.
//
// The end-to-end assertion lives in
// TestIssuePhaseSyncJiraTransitionsALockedButUnstartedPlan; this row states the
// friction case in its own terms, at the seam the warning came from.
func TestFrictionWindow_BoardStateResolves(t *testing.T) {
	// The shape plan.toml has the moment /dross-plan finishes: tasks exist,
	// none has started.
	plan := &phase.Plan{Task: []phase.Task{
		{ID: "t-1", Wave: 1, Title: "one", Status: phase.StatusPending},
		{ID: "t-2", Wave: 1, Title: "two", Status: phase.StatusPending},
	}}
	status := boardsync.DerivePhaseStatus(plan)

	if !configenum.LifecycleStatuses.Has(status) {
		t.Fatalf("a locked-but-unstarted plan derives %q, which is not a lifecycle status (%s) — no state map can resolve it",
			status, configenum.LifecycleStatuses.List())
	}
	if status == "planning" {
		t.Fatal(`derivePhaseStatus regressed to "planning" — the value neither state map keys`)
	}
}
