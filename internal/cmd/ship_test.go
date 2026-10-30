package cmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/Rivil/dross/internal/changes"
	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/phase"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/ship"
	"github.com/Rivil/dross/internal/state"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestBuildOpenOptsMapsGitLabFields pins c-2's wiring: the ship command must
// copy remote.auth_scheme and remote.project_id (plus the base remote fields)
// onto ship.OpenOpts. The inline struct literal this replaced was untestable, so
// a regression where GitLab silently used default auth / a derived id even when
// the user overrode them would have gone unnoticed.
func TestBuildOpenOptsMapsGitLabFields(t *testing.T) {
	p := &project.Project{Remote: project.Remote{
		Provider:   "gitlab",
		URL:        "https://gitlab.example/me/proj",
		APIBase:    "https://gitlab.example/api/v4",
		AuthEnv:    "GL_TOKEN",
		AuthScheme: "bearer",
		ProjectID:  "42",
		Reviewers:  []string{"alice"},
	}}
	got := buildOpenOpts(p, hostallow.Policy{})
	if got.Provider != "gitlab" || got.URL != p.Remote.URL || got.APIBase != p.Remote.APIBase || got.AuthEnv != "GL_TOKEN" {
		t.Errorf("base remote fields not copied: %+v", got)
	}
	if got.AuthScheme != "bearer" {
		t.Errorf("auth_scheme not copied onto OpenOpts: %q", got.AuthScheme)
	}
	if got.ProjectID != "42" {
		t.Errorf("project_id not copied onto OpenOpts: %q", got.ProjectID)
	}
	if len(got.Reviewers) != 1 || got.Reviewers[0] != "alice" {
		t.Errorf("reviewers not copied: %v", got.Reviewers)
	}
}

// TestBuildCommentOptsMapsGitLabFields pins c-3's wiring: the same provider /
// auth_scheme / project_id fields must reach ship.CommentOpts.
func TestBuildCommentOptsMapsGitLabFields(t *testing.T) {
	p := &project.Project{Remote: project.Remote{
		Provider:   "gitlab",
		URL:        "https://gitlab.example/me/proj",
		APIBase:    "https://gitlab.example/api/v4",
		AuthEnv:    "GL_TOKEN",
		AuthScheme: "bearer",
		ProjectID:  "42",
	}}
	got := buildCommentOpts(p, hostallow.Policy{})
	if got.Provider != "gitlab" || got.AuthEnv != "GL_TOKEN" || got.AuthScheme != "bearer" || got.ProjectID != "42" {
		t.Errorf("comment opts dropped a field: %+v", got)
	}
}

// TestBuildOpenOptsCarriesAPIVersion and its comment twin pin the join between
// [remote].api_version and the Azure DevOps transport: a dropped copy sends
// 7.1 to a Server that caps at 6.0, and every call fails.
func TestBuildOpenOptsCarriesAPIVersion(t *testing.T) {
	p := &project.Project{Remote: project.Remote{APIVersion: "6.0"}}
	if got := buildOpenOpts(p, hostallow.Policy{}); got.APIVersion != "6.0" {
		t.Errorf("api_version not copied onto OpenOpts: %q", got.APIVersion)
	}
}

func TestBuildCommentOptsCarriesAPIVersion(t *testing.T) {
	p := &project.Project{Remote: project.Remote{APIVersion: "6.0"}}
	if got := buildCommentOpts(p, hostallow.Policy{}); got.APIVersion != "6.0" {
		t.Errorf("api_version not copied onto CommentOpts: %q", got.APIVersion)
	}
}

// bitbucketRemote is the [remote] shape whose credential is HTTP Basic
// user:token — the only one where a dropped auth_user is fatal.
func bitbucketRemote() *project.Project {
	return &project.Project{Remote: project.Remote{
		Provider:   "bitbucket",
		URL:        "https://bitbucket.org/acme/widget",
		APIBase:    "https://api.bitbucket.org/2.0",
		AuthEnv:    "BB_TOKEN",
		AuthUser:   "wsuser",
		AuthScheme: "basic",
	}}
}

// TestBuildOpenOptsCarriesAuthUser pins the join point between the schema field
// (project.Remote.AuthUser) and the backend that needs it. This is the one gap
// no ship-package test can see: those construct OpenOpts directly, so every one
// of them passes while a real Bitbucket ship 401s on base64(:token).
func TestBuildOpenOptsCarriesAuthUser(t *testing.T) {
	got := buildOpenOpts(bitbucketRemote(), hostallow.Policy{})
	if got.AuthUser != "wsuser" {
		t.Errorf("auth_user not copied onto OpenOpts: %q", got.AuthUser)
	}
	// The mergeGate path builds OpenOpts too, so the same field is what lets
	// PRMerged answer for a Bitbucket PR rather than erroring on credentials.
	if got.Provider != "bitbucket" || got.AuthEnv != "BB_TOKEN" || got.AuthScheme != "basic" {
		t.Errorf("bitbucket remote fields not copied: %+v", got)
	}
}

func TestBuildCommentOptsCarriesAuthUser(t *testing.T) {
	got := buildCommentOpts(bitbucketRemote(), hostallow.Policy{})
	if got.AuthUser != "wsuser" {
		t.Errorf("auth_user not copied onto CommentOpts: %q", got.AuthUser)
	}
	if got.Provider != "bitbucket" || got.AuthEnv != "BB_TOKEN" || got.AuthScheme != "basic" {
		t.Errorf("bitbucket remote fields not copied: %+v", got)
	}
}

// shipFixture builds a fully-initialised dross repo with a phase that
// has spec, verify (pass), and changes recorded — ready to ship.
// Returns the repo dir.
func shipFixture(t *testing.T, originURL string) string {
	t.Helper()
	// The provider fallback (existing_pr_source) asks the forge for an open
	// PR by head on a first ship. The hand-rolled forgejo mocks answer only
	// POST /pulls, so stub the seam to "none" here; c-5 tests override it.
	stubOpenPRByHead(t, nil, nil)
	dir := t.TempDir()
	gitInit(t, dir, originURL)
	chdir(t, dir)

	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Configure project [remote] for forgejo before the baseline commit
	// so phase create finds a clean tree.
	for _, set := range [][]string{
		{"set", "remote.provider", "forgejo"},
		{"set", "remote.api_base", "https://forge.example/api/v1"},
		{"set", "remote.log_api", "true"},
		{"set", "remote.auth_env", "MOCK_FORGEJO_TOKEN"},
		{"set", "remote.reviewers", "alice"},
		{"set", "repo.git_main_branch", "main"},
	} {
		if err := runCmd(t, Project(), set...); err != nil {
			t.Fatalf("project %v: %v", set, err)
		}
	}
	mustWrite(t, filepath.Join(dir, "README.md"), "base\n")
	gitCommit(t, dir, "initial baseline")

	// Create the phase via the real CLI — this also checks us out onto
	// phase/x, matching the post-create state ship expects.
	if err := runCmd(t, Phase(), "create", "x"); err != nil {
		t.Fatalf("phase create: %v", err)
	}

	// Drop verify.toml at pass and write phase code on the phase branch.
	root := filepath.Join(dir, ".dross")
	phaseDir := filepath.Join(root, "phases", "x")
	mustWrite(t, filepath.Join(phaseDir, "verify.toml"), `[verify]
phase = "x"
generated_at = 2026-05-02T10:00:00Z
verdict = "pass"

[summary]
mutation_score = 0.85
mutants_killed = 17
mutants_survived = 3
criteria_total = 1
criteria_covered = 1
criteria_uncovered = 0

[[criterion]]
id = "C1"
status = "covered"
tests = ["tag.test.ts:42"]
`)
	// Phase create already wrote a "created" state.json entry that's
	// staged but uncommitted — fold it into the phase commit so the
	// working tree is clean when ship runs.
	mustWrite(t, filepath.Join(dir, "src/tag.ts"), "export const tag = 1\n")
	mustWrite(t, filepath.Join(phaseDir, "spec.toml"), `[phase]
id = "x"
title = "Tagging"

[[criteria]]
id = "C1"
text = "Tags can be added"
`)
	gitCommit(t, dir, "feat(tag): add tagging")
	commitSHA := gitOutT(t, dir, "rev-parse", "HEAD")
	// Rewritten wholesale, so it must carry the base `phase create` recorded
	// at fork time — otherwise the fixture silently models a phase that never
	// recorded one, and ship's overwrite would look like a first write.
	mustWrite(t, filepath.Join(phaseDir, "changes.json"), `{
  "phase": "x",
  "base": "main",
  "tasks": {
    "t1": {"files": ["src/tag.ts"], "commit": "`+commitSHA+`", "completed_at": "2026-05-02T10:00:00Z"}
  }
}`)
	gitCommit(t, dir, "chore(dross): record task t1")
	return dir
}

// gitCommit stages everything and commits. --allow-empty because the thing
// being committed is often a state.json write, which is gitignored — the point
// of the call is to leave a clean tree and advance the branch, and an empty
// commit does both.
func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

func gitOutT(t *testing.T, dir string, args ...string) string {
	return mustGit(t, dir, args...)
}

func TestShipNoPushSkipsPushAndPR(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	if err := runCmd(t, Ship(), "--no-push"); err != nil {
		t.Fatalf("ship --no-push: %v", err)
	}

	// No state.json shipped action recorded — --no-push is a dry run.
	st, _ := state.Load(filepath.Join(dir, ".dross", "state.json"))
	for _, a := range st.History {
		if strings.HasPrefix(a.Action, "shipped x") {
			t.Error("should not record shipped action under --no-push")
		}
	}

	// We should still be on the phase branch (ship doesn't switch).
	cur := mustGit(t, dir, "symbolic-ref", "--short", "HEAD")
	if cur != "phase/x" {
		t.Errorf("expected to stay on phase/x, got %q", cur)
	}
}

// TestShipWrongBranchRefusalNamesGuardedCheckout (c-1): the off-branch refusal
// has to point at `dross phase checkout`, not `git checkout`. This phase
// removed every raw switch from ship.md, but a refusal that hands the user the
// unguarded verb reopens the same hole by hand — and the prompt guard greps
// ship.md only, so nothing covered the CLI's own narration.
//
// The fixture branches off the phase tip rather than checking out main: the
// branch check sits behind the spec + verify gates, so a working tree without
// .dross/phases/x would fail at "load spec" and never reach the line under
// test.
func TestShipWrongBranchRefusalNamesGuardedCheckout(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	mustGit(t, dir, "checkout", "-q", "-b", "sidequest")

	err := runCmd(t, Ship(), "--no-push")
	if err == nil {
		t.Fatal("expected ship to refuse off the phase branch")
	}
	msg := err.Error()

	if !strings.Contains(msg, "dross phase checkout x") {
		t.Errorf("refusal should name the guarded verb with the phase id; got: %s", msg)
	}
	if strings.Contains(msg, "git checkout") {
		t.Errorf("refusal must not hand the user a raw checkout; got: %s", msg)
	}
	// Both branches still named, so the verb assertion cannot be satisfied by
	// a refusal that dropped the diagnostic detail.
	for _, needle := range []string{"phase/x", "sidequest"} {
		if !strings.Contains(msg, needle) {
			t.Errorf("refusal should name %q; got: %s", needle, msg)
		}
	}
}

func TestShipRefusesUnverified(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	// Override verify verdict to "fail".
	verifyPath := filepath.Join(dir, ".dross", "phases", "x", "verify.toml")
	body, _ := os.ReadFile(verifyPath)
	body = []byte(strings.Replace(string(body), `verdict = "pass"`, `verdict = "fail"`, 1))
	if err := os.WriteFile(verifyPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCmd(t, Ship(), "--no-push")
	if err == nil {
		t.Fatal("expected error when verdict != pass")
	}
	if !strings.Contains(err.Error(), "force-unverified") {
		t.Errorf("error should mention --force-unverified: %v", err)
	}
}

// A phase that was never verified must be REFUSED, not crashed on.
// verify.LoadVerify reports a missing file as (nil, nil) rather than an error,
// so the err-only check above it fell through to a nil dereference — every
// existing gate test writes a verify.toml first, so the one state a user hits
// by simply forgetting to verify was the one state nothing covered.
//
// The advice is already written in the load path ("run /dross-verify"); this
// pins that the user actually receives it.
func TestShipRefusesAPhaseThatWasNeverVerified(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	if err := os.Remove(filepath.Join(dir, ".dross", "phases", "x", "verify.toml")); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: drop verify.toml")

	err := runCmd(t, Ship(), "--no-push")
	if err == nil {
		t.Fatal("a phase with no verify.toml shipped")
	}
	if !strings.Contains(err.Error(), "dross-verify") {
		t.Errorf("refusal does not point at /dross-verify: %v", err)
	}
}

func TestShipForceUnverifiedSkipsGate(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	verifyPath := filepath.Join(dir, ".dross", "phases", "x", "verify.toml")
	body, _ := os.ReadFile(verifyPath)
	body = []byte(strings.Replace(string(body), `verdict = "pass"`, `verdict = "fail"`, 1))
	if err := os.WriteFile(verifyPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: flip verdict") // ship needs clean tree

	if err := runCmd(t, Ship(), "--no-push", "--force-unverified"); err != nil {
		t.Errorf("--force-unverified should bypass gate: %v", err)
	}
}

func TestShipFullFlowAgainstMockProvider(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	// Stand up a mock Forgejo + a bare-init "remote" git repo so push works.
	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)

	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")

	var openedTitle string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == "POST" {
			var doc map[string]any
			_ = json.Unmarshal(body, &doc)
			openedTitle, _ = doc["title"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":99,"html_url":"https://forge.example/me/p/pulls/99"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/requested_reviewers") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(server.Close)

	// Point project.toml at the mock server. project set runs from main
	// (we're on phase/x and it writes to .dross/project.toml), so
	// commit on the phase branch before shipping (clean tree required).
	// The API lives on a different host from [remote].url here, which is the
	// case the machine-local escape hatch exists for: authorize it by hand on
	// this machine, never through committed config.
	if err := runCmd(t, Local(), "set", "allow_hosts", server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, Project(), "set", "remote.api_base", server.URL); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: point api_base at mock")

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}

	if !strings.Contains(openedTitle, "phase x") {
		t.Errorf("PR title should reference phase id, got: %q", openedTitle)
	}

	// Ship marks the phase shipped and stops there: current_phase stays set
	// and history records `shipped x`. The completed-state transition belongs
	// to `dross phase complete`, behind the merge gate. The PR URL is printed,
	// not persisted.
	st, _ := state.Load(filepath.Join(dir, ".dross", "state.json"))
	if st.CurrentPhase != "x" {
		t.Errorf("ship should leave current_phase set to the shipped phase, got %q", st.CurrentPhase)
	}
	if st.CurrentPhaseStatus != "shipped" {
		t.Errorf("ship should set current_phase_status to shipped, got %q", st.CurrentPhaseStatus)
	}
	foundShipped := false
	for _, a := range st.History {
		if strings.Contains(a.Action, "completed x") {
			t.Errorf("ship must not record `completed x` — that is phase complete's write: %+v", st.History)
		}
		if strings.Contains(a.Action, "shipped x") {
			foundShipped = true
		}
	}
	if !foundShipped {
		t.Errorf("state history should record `shipped x`; history: %+v", st.History)
	}

	// Remote should have received phase/x directly (no synthetic
	// pr/<id> branch any more).
	remoteRefs := mustGit(t, remoteDir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if !strings.Contains(remoteRefs, "phase/x") {
		t.Errorf("expected phase/x on remote, got: %q", remoteRefs)
	}

	// The shipped record is written locally and stays there: state.json is
	// gitignored (locked state_tracking), so the pushed ref must carry no copy
	// of it at all. Re-staging it would put the clobber back.
	if gitAllowFail(remoteDir, "cat-file", "-e", "phase/x:.dross/state.json") {
		t.Error("the pushed ref carries .dross/state.json — it must stay machine-local")
	}
	var local state.State
	localBytes := mustRead(t, filepath.Join(dir, ".dross", state.File))
	if err := json.Unmarshal([]byte(localBytes), &local); err != nil {
		t.Fatalf("parse local state.json: %v", err)
	}
	if local.CurrentPhaseStatus != "shipped" {
		t.Errorf("ship should mark the phase shipped locally, got %q", local.CurrentPhaseStatus)
	}

	// Ship must return on a clean working tree: the PR-number record is
	// committed as part of ship, not left uncommitted. If that commit step is
	// dropped, the tree is dirty here.
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("working tree should be clean after ship, got: %q", st)
	}
	// t-1: the opened PR number (99 from the mock) is persisted into the
	// phase's changes.json and committed post-push, so `dross phase complete`
	// can look up THIS phase's authoritative merge status. That record commit
	// is HEAD.
	ch, err := changes.Load(changes.FilePath(filepath.Join(dir, ".dross"), "x"), "x")
	if err != nil {
		t.Fatalf("load changes.json: %v", err)
	}
	if ch.PR != 99 {
		t.Errorf("changes.json should carry the opened PR number 99, got %d", ch.PR)
	}
	// shipped_timing: the record commit is followed by the shipped-marker
	// commit, pushed after the record push landed — so HEAD is the marker,
	// local HEAD is origin's tip, and origin's changes.json carries BOTH the
	// PR number and the shipped status.
	if msg := mustGit(t, dir, "log", "-1", "--pretty=%s"); msg != "chore(dross): mark x shipped" {
		t.Errorf("HEAD should be the shipped-marker commit, got: %q", msg)
	}
	if local, remote := mustGit(t, dir, "rev-parse", "HEAD"), mustGit(t, remoteDir, "rev-parse", "phase/x"); local != remote {
		t.Errorf("local HEAD %s != origin phase/x %s — a ship commit was left unpushed", local, remote)
	}
	var pushed changes.Changes
	if err := json.Unmarshal([]byte(mustGit(t, remoteDir, "show", "phase/x:.dross/phases/x/changes.json")), &pushed); err != nil {
		t.Fatalf("parse pushed changes.json: %v", err)
	}
	if pushed.PR != 99 || pushed.Status != changes.StatusShipped {
		t.Errorf("origin's changes.json should carry pr 99 + status shipped, got pr=%d status=%q", pushed.PR, pushed.Status)
	}
	// There is no `chore(dross): ship x` commit any more: state.json was the
	// only thing it ever carried, and that write is machine-local now.
	if log := mustGit(t, dir, "log", "--pretty=%s"); strings.Contains(log, "chore(dross): ship x") {
		t.Errorf("ship should no longer commit a state fold:\n%s", log)
	}
}

// TestShipPushesPRRecordToPhaseBranch proves c-1/c-2: the PR-number record
// commit is PUSHED to origin's phase-branch ref, not left as a local-only
// post-push commit. It reads changes.json at the pushed branch tip in the bare
// remote and asserts it carries the opened PR number. Before this fix the record
// commit was made after the push and never reached origin, so the pushed tree's
// changes.json had PR:0 — which made phase complete's mergeGate fall back and
// refuse every squash-merged completion.
func TestShipPushesPRRecordToPhaseBranch(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)
	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == "POST" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":99,"html_url":"https://forge.example/me/p/pulls/99"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/requested_reviewers") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	// The API lives on a different host from [remote].url here, which is the
	// case the machine-local escape hatch exists for: authorize it by hand on
	// this machine, never through committed config.
	if err := runCmd(t, Local(), "set", "allow_hosts", server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, Project(), "set", "remote.api_base", server.URL); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: point api_base at mock")

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}

	// The record commit must be HEAD of the PUSHED ref, not a local-only commit
	// past origin. If the push were dropped, origin/phase-x would trail local
	// HEAD by the record commit and this SHA comparison would differ.
	localHead := mustGit(t, dir, "rev-parse", "HEAD")
	remoteHead := mustGit(t, remoteDir, "rev-parse", "phase/x")
	if localHead != remoteHead {
		t.Fatalf("PR-record commit is local-only: local HEAD %s != pushed phase/x %s", localHead, remoteHead)
	}

	// Read changes.json at the pushed branch tip in the bare remote and assert it
	// carries the opened PR number — i.e. the record actually reached origin (and
	// so will reach the base via the squash-merge).
	pushedChanges := mustGit(t, remoteDir, "show", "phase/x:.dross/phases/x/changes.json")
	var pushed changes.Changes
	if err := json.Unmarshal([]byte(pushedChanges), &pushed); err != nil {
		t.Fatalf("parse pushed changes.json: %v", err)
	}
	if pushed.PR != 99 {
		t.Errorf("pushed changes.json should carry PR 99, got %d (record left local-only)", pushed.PR)
	}
	// The base rides the same commit and push as the PR number, so the squash
	// carries a consistent (base, pr) pair onto the base branch — that pair is
	// what `dross phase complete` reconciles against.
	if pushed.Base != "main" {
		t.Errorf("pushed changes.json should carry base \"main\", got %q (base write left local-only)", pushed.Base)
	}

	// And the record commit itself must be in the pushed history (the
	// shipped-marker commit follows it, so it is one behind the tip).
	if log := mustGit(t, remoteDir, "log", "--pretty=%s", "phase/x"); !strings.Contains(log, "chore(dross): record PR #99 for x") {
		t.Errorf("pushed phase/x should carry the PR-record commit, got:\n%s", log)
	}
}

// TestShipNoPushIssuesNoRecordPush proves c-3's --no-push scope: with --no-push
// ship returns before opening a PR, so no PR-record push (or any push) can fire.
// A bare remote is wired up; if ship pushed anything, phase/x would appear there.
func TestShipNoPushIssuesNoRecordPush(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)

	if err := runCmd(t, Ship(), "--no-push"); err != nil {
		t.Fatalf("ship --no-push: %v", err)
	}

	// Nothing was pushed: the bare remote has no branches at all.
	if refs := mustGit(t, remoteDir, "for-each-ref", "--format=%(refname:short)", "refs/heads"); strings.TrimSpace(refs) != "" {
		t.Errorf("--no-push must not push any ref, but remote has: %q", refs)
	}
}

// TestShipDoesNotPersistPRWhenOpenFails pins t-1's guard: when the provider
// rejects the PR open (OpenPR returns res==nil), ship must persist NO PR
// number and leave no `record PR` commit — never a misleading PR:0.
func TestShipDoesNotPersistPRWhenOpenFails(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)
	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject the PR-open so openForgejoPR returns (nil, err).
		if strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == "POST" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	// The API lives on a different host from [remote].url here, which is the
	// case the machine-local escape hatch exists for: authorize it by hand on
	// this machine, never through committed config.
	if err := runCmd(t, Local(), "set", "allow_hosts", server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, Project(), "set", "remote.api_base", server.URL); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: point api_base at mock")

	if err := runCmd(t, Ship()); err == nil {
		t.Fatal("expected ship to fail when the provider rejects the PR open")
	}
	ch, err := changes.Load(changes.FilePath(filepath.Join(dir, ".dross"), "x"), "x")
	if err != nil {
		t.Fatalf("load changes.json: %v", err)
	}
	if ch.PR != 0 {
		t.Errorf("no PR should be persisted when open fails, got %d", ch.PR)
	}
	if msg := mustGit(t, dir, "log", "-1", "--pretty=%s"); strings.Contains(msg, "record PR") {
		t.Errorf("no PR-record commit should exist when open fails, HEAD: %q", msg)
	}
	// shipped_timing: the state flip happens after the record push, so a
	// failed open leaves state reading whatever it read before — never
	// shipped. A flip left ahead of OpenPR fails here.
	st, err := state.Load(filepath.Join(dir, ".dross", state.File))
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentPhaseStatus == "shipped" {
		t.Error("state reads shipped although no PR was opened")
	}
	if ch.Status == changes.StatusShipped {
		t.Error("changes.json reads shipped although no PR was opened")
	}
}

// shipCapture records what a mock provider received, so --auto assertions
// can check the reviewer endpoint was never hit and the posted body/title.
type shipCapture struct {
	openedTitle  string
	openedBody   string
	openedBase   string
	openedHead   string
	reviewersHit bool
	posts        int // POST /pulls count — one per PR actually opened
}

// shipMockFlow stands up a bare-init "remote" plus a mock Forgejo server for
// the given fixture repo, points remote.api_base at it (committing so the tree
// stays clean), and returns a capture the caller inspects after shipping. It
// mirrors TestShipFullFlowAgainstMockProvider's setup, factored out so the
// --auto tests don't duplicate the httptest scaffolding.
func shipMockFlow(t *testing.T, dir string) *shipCapture {
	t.Helper()
	cap, _ := shipMockFlowRemote(t, dir)
	return cap
}

// shipMockFlowRemote is shipMockFlow returning the bare origin's path too, for
// tests that install hooks on it or read its refs.
func shipMockFlowRemote(t *testing.T, dir string) (*shipCapture, string) {
	t.Helper()
	remoteDir := t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)
	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")

	cap := &shipCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == "POST" {
			var doc map[string]any
			_ = json.Unmarshal(body, &doc)
			cap.openedTitle, _ = doc["title"].(string)
			cap.openedBody, _ = doc["body"].(string)
			cap.openedBase, _ = doc["base"].(string)
			cap.openedHead, _ = doc["head"].(string)
			cap.posts++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"number":99,"html_url":"https://forge.example/me/p/pulls/99"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/requested_reviewers") {
			cap.reviewersHit = true
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(server.Close)

	// The API lives on a different host from [remote].url here, which is the
	// case the machine-local escape hatch exists for: authorize it by hand on
	// this machine, never through committed config.
	if err := runCmd(t, Local(), "set", "allow_hosts", server.Listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, Project(), "set", "remote.api_base", server.URL); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: point api_base at mock")
	return cap, remoteDir
}

// TestShipAutoRequestsZeroReviewers proves c-1's reviewer behaviour: with
// remote.reviewers=[alice] configured, `ship --auto` opens the PR requesting
// zero reviewers (the provider's requested_reviewers endpoint is never hit and
// no "Reviewers requested" line is printed), records a reviewers count of 0 in
// telemetry, and leaves the remote.reviewers config untouched (per the locked
// reviewers_under_auto decision — per-invocation, non-destructive).
func TestShipAutoRequestsZeroReviewers(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	// Isolate HOME and re-enable telemetry so we can read back the outcome
	// event's reviewers count. shipFixture's chdir pinned DROSS_NO_TELEMETRY=1.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROSS_NO_TELEMETRY", "")

	cap := shipMockFlow(t, dir)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship(), "--auto"); err != nil {
			t.Fatalf("ship --auto: %v", err)
		}
	})

	// Zero reviewers requested: the endpoint is never called, and the
	// narration line stays silent.
	if cap.reviewersHit {
		t.Error("--auto must request zero reviewers, but the requested_reviewers endpoint was hit")
	}
	if strings.Contains(out, "Reviewers requested") {
		t.Errorf("--auto must not print a 'Reviewers requested' line:\n%s", out)
	}

	// remote.reviewers config is left untouched (still the fixture's "alice").
	p, _, err := loadProject()
	if err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if len(p.Remote.Reviewers) != 1 || p.Remote.Reviewers[0] != "alice" {
		t.Errorf("--auto must not mutate remote.reviewers, got %v", p.Remote.Reviewers)
	}

	// Telemetry records a reviewers count of 0 and the auto tag.
	telem := mustRead(t, filepath.Join(home, ".claude/dross", "telemetry.jsonl"))
	if !strings.Contains(telem, `"reviewers":0`) {
		t.Errorf("--auto telemetry should record reviewers count 0:\n%s", telem)
	}
	if !strings.Contains(telem, `"auto":"true"`) {
		t.Errorf("--auto telemetry should carry the auto tag:\n%s", telem)
	}
}

// TestShipAutoStillHonorsVerifyGate proves c-3: --auto does not bypass the
// "verify must be pass" gate. A pending verdict still fails unless
// --force-unverified is also passed.
func TestShipAutoStillHonorsVerifyGate(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	verifyPath := filepath.Join(dir, ".dross", "phases", "x", "verify.toml")
	body, _ := os.ReadFile(verifyPath)
	body = []byte(strings.Replace(string(body), `verdict = "pass"`, `verdict = "pending"`, 1))
	if err := os.WriteFile(verifyPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	// --auto alone must still hit the gate on a pending verdict.
	err := runCmd(t, Ship(), "--auto", "--no-push")
	if err == nil {
		t.Fatal("--auto must still fail the verify gate on a pending verdict")
	}
	if !strings.Contains(err.Error(), "force-unverified") {
		t.Errorf("gate error should mention --force-unverified: %v", err)
	}

	// With --force-unverified the gate is bypassed even under --auto.
	gitCommit(t, dir, "test: flip verdict to pending") // clean tree
	if err := runCmd(t, Ship(), "--auto", "--no-push", "--force-unverified"); err != nil {
		t.Errorf("--auto --force-unverified should bypass the verify gate: %v", err)
	}
}

// TestShipAutoExplicitBodyWins proves the locked explicit_flags_win decision:
// --auto governs prompts/defaults only, so an explicit --body still overrides
// the generated body.
func TestShipAutoExplicitBodyWins(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)

	if err := runCmd(t, Ship(), "--auto", "--body", "CUSTOM BODY"); err != nil {
		t.Fatalf("ship --auto --body: %v", err)
	}
	if cap.openedBody != "CUSTOM BODY" {
		t.Errorf("explicit --body must win over --auto's generated default, got %q", cap.openedBody)
	}
}

// TestShipJSONEmitsSingleObjectAndSuppressesNarration proves c-5: `ship --json`
// writes exactly one parseable JSON object with keys url/number/result to stdout
// and suppresses the human narration lines.
func TestShipJSONEmitsSingleObjectAndSuppressesNarration(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship(), "--json"); err != nil {
			t.Fatalf("ship --json: %v", err)
		}
	})

	// No human narration leaked onto stdout. The last needle is ship's
	// post-PR line naming the completion-record owner — it moved with the
	// write, so the needle moves with it rather than going vacuous.
	for _, line := range []string{"Pushed", "PR opened", "dross phase complete"} {
		if strings.Contains(out, line) {
			t.Errorf("--json must suppress the %q narration line, got:\n%s", line, out)
		}
	}

	// Exactly one line, parseable as a JSON object with the three keys.
	trimmed := strings.TrimSpace(out)
	if strings.Contains(trimmed, "\n") {
		t.Errorf("--json should emit exactly one line, got:\n%s", out)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		t.Fatalf("--json output should parse as one JSON object, got %q: %v", trimmed, err)
	}
	for _, k := range []string{"url", "number", "result"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("--json object missing key %q: %v", k, obj)
		}
	}
}

// TestShipAutoJSONComposable proves c-5's composability clause: `ship --auto
// --json` emits clean JSON that parses, with result "opened" and the PR
// url/number, while still requesting zero reviewers.
func TestShipAutoJSONComposable(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship(), "--auto", "--json"); err != nil {
			t.Fatalf("ship --auto --json: %v", err)
		}
	})

	var obj struct {
		URL    string `json:"url"`
		Number int    `json:"number"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("--auto --json should emit parseable JSON, got %q: %v", out, err)
	}
	if obj.Result != "opened" {
		t.Errorf("result should be \"opened\", got %q", obj.Result)
	}
	if obj.URL == "" || obj.Number != 99 {
		t.Errorf("JSON should carry the PR url/number, got url=%q number=%d", obj.URL, obj.Number)
	}
	if cap.reviewersHit {
		t.Error("--auto --json must still request zero reviewers")
	}
}

// TestShipIsReShippable ships the same phase twice. A re-ship after review
// edits must exit 0, leave the phase reading `shipped`, and append exactly one
// `shipped <id>` entry — the breadcrumb is history-scan-guarded, so a second
// run re-asserts the status without doubling the row. It must also return on a
// clean tree, never bail on "nothing to commit".
func TestShipIsReShippable(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)

	// First ship — resolves the phase from current_phase and leaves it set.
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("first ship: %v", err)
	}
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("tree dirty after first ship: %q", st)
	}

	// Second ship — current_phase is still set, so no argument is needed. It
	// must succeed, leave a clean tree, open NO second PR (c-3: the record
	// names #99 and that wins), and report the existing PR by number.
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("re-ship should be idempotent, got: %v", err)
		}
	})
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("tree should be clean after re-ship, got: %q", st)
	}
	if cap.posts != 1 {
		t.Errorf("POST /pulls across two runs = %d, want 1 — the re-run opened a second PR", cap.posts)
	}
	if !strings.Contains(out, "#99") {
		t.Errorf("re-run should report the existing PR by number:\n%s", out)
	}

	s, err := state.Load(filepath.Join(dir, ".dross", state.File))
	if err != nil {
		t.Fatal(err)
	}
	if s.CurrentPhaseStatus != "shipped" {
		t.Errorf("re-ship should leave current_phase_status shipped, got %q", s.CurrentPhaseStatus)
	}
	shipped := 0
	for _, a := range s.History {
		if strings.Contains(a.Action, "shipped x") {
			shipped++
		}
	}
	if shipped != 1 {
		t.Errorf("re-ship should not double the `shipped x` entry, got %d: %+v", shipped, s.History)
	}
}

// shipCoverInitProject inits a dross repo in a fresh temp dir with an isolated
// HOME (no git origin, so [remote] starts empty), then applies the given
// remote.<field>=<value> pairs. Used by the shipComment coverage tests to drive
// its [remote] preflight gate into specific states.
func shipCoverInitProject(t *testing.T, sets ...[2]string) string {
	t.Helper()
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("HOME", t.TempDir())
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, kv := range sets {
		if err := runCmd(t, Project(), "set", kv[0], kv[1]); err != nil {
			t.Fatalf("project set %s=%s: %v", kv[0], kv[1], err)
		}
	}
	return dir
}

// TestShipCover_CommentEarlyReturns exercises shipComment's argument-validation
// gates that return before any project load: the --pr guard (line 339, both the
// boundary and the negation via pr=0, plus the negation-reverse via pr=5), the
// "need --body or --body-file" guard (line 342, both operands), and the
// --body-file read + error branch (lines 345, 347).
func TestShipCover_CommentEarlyReturns(t *testing.T) {
	chdir(t, t.TempDir()) // no .dross here: these gates all return before loadProject
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"pr zero", []string{"comment", "--pr", "0", "--body", "hi"}, "--pr is required"},
		{"no body and no file", []string{"comment", "--pr", "5"}, "either --body or --body-file"},
		{"unreadable body-file", []string{"comment", "--pr", "5", "--body-file", filepath.Join(t.TempDir(), "nope.md")}, "read --body-file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := runCmd(t, Ship(), c.args...)
			if err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should contain %q", err, c.want)
			}
		})
	}
}

// TestShipCover_CommentLoadProjectError drives shipComment's loadProject error
// branch (line 353): with --pr and --body both valid but no .dross root,
// loadProject returns ErrNoRoot and shipComment must surface it (the mutated
// `err == nil` branch would fall through to a nil-project deref instead).
func TestShipCover_CommentLoadProjectError(t *testing.T) {
	chdir(t, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	err := runCmd(t, Ship(), "comment", "--pr", "5", "--body", "hi")
	if err == nil {
		t.Fatal("expected loadProject error without a .dross root")
	}
	if !strings.Contains(err.Error(), "no .dross") {
		t.Errorf("error should surface the missing-root failure, got: %v", err)
	}
}

// TestShipCover_CommentRemoteMissingURL drives the first operand of shipComment's
// [remote] preflight (line 356): provider is set but url is empty, so the gate
// must fire before reaching PostComment.
func TestShipCover_CommentRemoteMissingURL(t *testing.T) {
	shipCoverInitProject(t, [2]string{"remote.provider", "forgejo"})
	err := runCmd(t, Ship(), "comment", "--pr", "5", "--body", "hi")
	if err == nil {
		t.Fatal("expected [remote] gate to fire with empty url")
	}
	if !strings.Contains(err.Error(), "[remote]") {
		t.Errorf("error should name the missing [remote] config, got: %v", err)
	}
	if strings.Contains(err.Error(), "post comment") {
		t.Errorf("must not reach PostComment when url is empty, got: %v", err)
	}
}

// TestShipCover_CommentRemoteMissingProvider drives the second operand of line
// 356: url is set but provider is empty.
func TestShipCover_CommentRemoteMissingProvider(t *testing.T) {
	shipCoverInitProject(t, [2]string{"remote.url", "https://forge.example/me/p"})
	err := runCmd(t, Ship(), "comment", "--pr", "5", "--body", "hi")
	if err == nil {
		t.Fatal("expected [remote] gate to fire with empty provider")
	}
	if !strings.Contains(err.Error(), "[remote]") {
		t.Errorf("error should name the missing [remote] config, got: %v", err)
	}
	if strings.Contains(err.Error(), "post comment") {
		t.Errorf("must not reach PostComment when provider is empty, got: %v", err)
	}
}

// TestShipCover_CommentReachesPostComment drives the both-configured path of
// line 356 (gate passes) into the PostComment error branch (line 362): with a
// valid url+provider but no api_base, PostComment fails and shipComment wraps it
// as "post comment". The mutated `err == nil` branch would instead print
// "Posted comment" and return nil.
func TestShipCover_CommentReachesPostComment(t *testing.T) {
	shipCoverInitProject(t,
		[2]string{"remote.provider", "forgejo"},
		[2]string{"remote.url", "https://forge.example/me/p"},
		[2]string{"remote.auth_env", "MOCK_FORGEJO_TOKEN"},
	)
	err := runCmd(t, Ship(), "comment", "--pr", "5", "--body", "hi")
	if err == nil {
		t.Fatal("expected PostComment to fail without api_base")
	}
	if !strings.Contains(err.Error(), "post comment") {
		t.Errorf("shipComment must wrap the PostComment failure as 'post comment', got: %v", err)
	}
}

// TestShipCover_ShipBadBodyFile drives the --body-file read-error branch of the
// main ship flow (line 155): a missing --body-file must abort with "read
// --body-file" before any push (guarded by --no-push). The mutated `err == nil`
// branch would swallow the read failure and return cleanly.
func TestShipCover_ShipBadBodyFile(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	err := runCmd(t, Ship(), "--no-push", "--body-file", filepath.Join(dir, "no-such-body.md"))
	if err == nil {
		t.Fatal("expected error for unreadable --body-file")
	}
	if !strings.Contains(err.Error(), "read --body-file") {
		t.Errorf("error should mention read --body-file, got: %v", err)
	}
}

// TestShipCover_ResultTag pins shipResultTag's four-way classification (lines
// 379/381/383): the (err, res) matrix maps to failed/partial/opened/noop, so
// negating any operand in those case guards misroutes at least one row.
func TestShipCover_ResultTag(t *testing.T) {
	boom := errors.New("boom")
	res := &ship.OpenResult{URL: "u", Number: 7}
	cases := []struct {
		name     string
		err      error
		res      *ship.OpenResult
		existing bool
		want     string
	}{
		{"failed", boom, nil, false, "failed"},
		{"partial", boom, res, false, "partial"},
		{"opened", nil, res, false, "opened"},
		{"noop", nil, nil, false, "noop"},
		// A re-run whose record already named the PR opened nothing.
		{"existing", nil, res, true, "existing"},
		// existing without a result is not a re-run shape; it stays noop.
		{"existing-nil", nil, nil, true, "noop"},
	}
	for _, c := range cases {
		if got := shipResultTag(c.res, c.err, c.existing); got != c.want {
			t.Errorf("%s: shipResultTag = %q, want %q", c.name, got, c.want)
		}
	}
}

// activateMilestone sets current_milestone and commits it (keeping the tree
// clean for ship), then creates the local milestone/<version> branch. The
// caller pushes it to origin (or not, to exercise the missing-remote guard).
func activateMilestone(t *testing.T, dir, version string) {
	t.Helper()
	if err := runCmd(t, State(), "set", "current_milestone", version); err != nil {
		t.Fatalf("state set current_milestone: %v", err)
	}
	gitCommit(t, dir, "scope "+version)
	mustGit(t, dir, "branch", "milestone/"+version)
}

func TestShipTargetsMilestoneBranch(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)
	activateMilestone(t, dir, "v0.9")
	mustGit(t, dir, "push", "origin", "milestone/v0.9") // base present on origin

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if cap.openedBase != "milestone/v0.9" || cap.openedHead != "phase/x" {
		t.Errorf("PR base/head = %q/%q; want milestone/v0.9 / phase/x", cap.openedBase, cap.openedHead)
	}
}

// readShippedBase reads a phase's recorded forked-from base from the working
// tree.
func readShippedBase(t *testing.T, dir, phaseID string) string {
	t.Helper()
	ch, err := changes.Load(changes.FilePath(filepath.Join(dir, ".dross"), phaseID), phaseID)
	if err != nil {
		t.Fatalf("load changes for %s: %v", phaseID, err)
	}
	return ch.Base
}

// TestShipOverwritesRecordedBase is the authoritative half of
// base_write_timing (c-2): create recorded "main", but the PR was opened
// against milestone/v0.9, and completion must reconcile against what the PR
// actually targeted. A milestone scoped after the fork is the ordinary way the
// two diverge.
func TestShipOverwritesRecordedBase(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)

	if got := readShippedBase(t, dir, "x"); got != "main" {
		t.Fatalf("fixture precondition: create-time base = %q, want %q", got, "main")
	}

	activateMilestone(t, dir, "v0.9")
	mustGit(t, dir, "push", "origin", "milestone/v0.9")

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if cap.openedBase != "milestone/v0.9" {
		t.Fatalf("fixture precondition: PR opened against %q, want milestone/v0.9", cap.openedBase)
	}
	if got := readShippedBase(t, dir, "x"); got != "milestone/v0.9" {
		t.Errorf("recorded base = %q, want %q — ship must overwrite the create-time value", got, "milestone/v0.9")
	}
}

// TestShipEarlyReturnsLeaveBaseIntact pins that the base write sits with the
// PR record, past --no-push and --print-body: those flags open no PR, so
// there is no authoritative base to record and no commit to make.
func TestShipEarlyReturnsLeaveBaseIntact(t *testing.T) {
	for _, flag := range []string{"--no-push", "--print-body"} {
		t.Run(flag, func(t *testing.T) {
			dir := shipFixture(t, "https://forge.example/me/p.git")
			remoteDir := t.TempDir()
			mustGit(t, remoteDir, "init", "-q", "--bare")
			mustGit(t, dir, "remote", "set-url", "origin", remoteDir)

			headBefore := mustGit(t, dir, "rev-parse", "HEAD")
			if err := runCmd(t, Ship(), flag); err != nil {
				t.Fatalf("ship %s: %v", flag, err)
			}
			if got := readShippedBase(t, dir, "x"); got != "main" {
				t.Errorf("%s changed the recorded base to %q, want the create-time %q", flag, got, "main")
			}
			if got := mustGit(t, dir, "rev-parse", "HEAD"); got != headBefore {
				t.Errorf("%s produced an extra commit: HEAD %s -> %s", flag, headBefore, got)
			}
		})
	}
}

// TestShipReconcilesRecordedQuickBase (c-7) is the ship-side twin of
// TestPhaseCompleteReconcilesRecordedQuickBase. pushQuickBaseIfRecorded has two
// call sites — phase complete's and ship's — and the criterion says "any
// command that later reconciles it", so both need a failing test behind them:
// with only the complete-side test, deleting ship's call leaves the suite green
// while a standalone quick task's chores sit unpushed through the whole ship.
func TestShipReconcilesRecordedQuickBase(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)

	// A branch standing in for a standalone quick task's target, carrying an
	// unpushed .dross-only chore. It is not this phase's base (that is main),
	// which is exactly the case the record exists for.
	mustGit(t, dir, "push", "-q", "origin", "main:quick-target")
	mustGit(t, dir, "branch", "quick-target", "main")
	mustGit(t, dir, "fetch", "-q", "origin")
	mustGit(t, dir, "checkout", "-q", "quick-target")
	mustWrite(t, filepath.Join(dir, ".dross", "handoff.md"), "# quick chore\n")
	mustGit(t, dir, "add", ".dross/handoff.md")
	mustGit(t, dir, "commit", "-q", "-m", "chore(dross): quick task bookkeeping")
	mustGit(t, dir, "checkout", "-q", "phase/x")

	if err := runCmd(t, Local(), "set", "quick_base", "quick-target"); err != nil {
		t.Fatalf("local set: %v", err)
	}
	if ahead := mustGit(t, dir, "rev-list", "origin/quick-target..quick-target"); ahead == "" {
		t.Fatal("fixture precondition: quick-target should be ahead of origin")
	}

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if ahead := mustGit(t, dir, "rev-list", "origin/quick-target..quick-target"); ahead != "" {
		t.Errorf("ship left the recorded quick_base unpushed: %q", ahead)
	}
}

func TestShipTargetsMainNoMilestone(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if cap.openedBase != "main" {
		t.Errorf("PR base = %q; want main (no milestone)", cap.openedBase)
	}
}

func TestShipRefusesMissingRemoteBase(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)
	activateMilestone(t, dir, "v0.9") // local branch, deliberately NOT pushed

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("expected refusal when the milestone base is absent on origin")
	}
	if !strings.Contains(err.Error(), "not on origin") {
		t.Errorf("error should point at the missing origin base: %v", err)
	}
}

func TestShipNudgesNoMilestone(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("ship: %v", err)
		}
	})
	if !strings.Contains(out, "dross milestone") {
		t.Errorf("no-milestone ship should nudge naming `dross milestone`; got:\n%s", out)
	}
}

// c-1: ship's pre-stage gate refuses real code dirt before anything is staged
// or pushed — the bare remote must never see phase/x.
func TestShipRefusesStrayCodeDirtBeforePush(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)
	mustWrite(t, filepath.Join(dir, "src/stray.ts"), "x\n")

	err := runCmd(t, Ship())
	if err == nil || !strings.Contains(err.Error(), "working tree is dirty") {
		t.Fatalf("expected dirty-tree refusal on stray code dirt, got: %v", err)
	}
	if !strings.Contains(err.Error(), "stray.ts") {
		t.Errorf("refusal should name the offending path: %v", err)
	}
	remoteDir := mustGit(t, dir, "remote", "get-url", "origin")
	refs := mustGit(t, remoteDir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if strings.Contains(refs, "phase/x") {
		t.Errorf("refusal must land before the push; remote has: %q", refs)
	}
}

// c-1: ship with only .dross dirt auto-commits it and proceeds — the pushed
// ref carries the bookkeeping, and the local tree ends clean.
func TestShipAutoCommitsDrossDirtThenPushes(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)
	mustWrite(t, filepath.Join(dir, ".dross", "handoff.md"), "# handoff\n")

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship should proceed past .dross-only dirt: %v", err)
	}
	log := mustGit(t, dir, "log", "--format=%s")
	if !strings.Contains(log, "chore(dross): auto-commit bookkeeping") {
		t.Errorf("expected an auto-commit chore in the log:\n%s", log)
	}
	remoteDir := mustGit(t, dir, "remote", "get-url", "origin")
	pushed := mustGit(t, remoteDir, "show", "phase/x:.dross/handoff.md")
	if !strings.Contains(pushed, "handoff") {
		t.Errorf("pushed ref should carry the auto-committed file, got: %q", pushed)
	}
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("tree should be clean after ship, got: %q", st)
	}
}

// --- ship auto-heal of unfinalized verdicts (verify-auto-finalize c-1) ---

// TestShipHealsUnfinalizedVerdict: shipping a phase whose verify.toml
// verdict is resolved but never finalized must record the verify
// outcome event (with the verdict tag) and write the finalized marker
// back, before the pass gate evaluates.
func TestShipHealsUnfinalizedVerdict(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROSS_NO_TELEMETRY", "") // re-enable (chdir pins it to "1")

	if err := runCmd(t, Ship(), "--no-push"); err != nil {
		t.Fatalf("ship --no-push: %v", err)
	}

	telemBody := mustRead(t, filepath.Join(home, ".claude/dross", "telemetry.jsonl"))
	if !strings.Contains(telemBody, `"verdict":"pass"`) {
		t.Errorf("ship should record the resolved verdict outcome event:\n%s", telemBody)
	}
	vbody := mustRead(t, filepath.Join(dir, ".dross", "phases", "x", "verify.toml"))
	if !strings.Contains(vbody, "finalized = true") {
		t.Errorf("ship heal must write finalized=true into verify.toml:\n%s", vbody)
	}
}

// TestShipHealIdempotent: shipping an already-finalized phase must not
// emit a duplicate outcome event.
func TestShipHealIdempotent(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROSS_NO_TELEMETRY", "")

	// Manual finalize first — the primary path.
	if err := runCmd(t, Verify(), "finalize", "x"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	gitCommit(t, dir, "chore(dross): record verify") // marker write dirties the tree

	if err := runCmd(t, Ship(), "--no-push"); err != nil {
		t.Fatalf("ship --no-push: %v", err)
	}

	telemBody := mustRead(t, filepath.Join(home, ".claude/dross", "telemetry.jsonl"))
	if got := strings.Count(telemBody, `"verdict":"pass"`); got != 1 {
		t.Errorf("ship on finalized phase must not duplicate the outcome event, got %d:\n%s", got, telemBody)
	}
}

// TestShipHealRecordsPartialThenRefuses: a partial verdict is recorded
// (heal fires) but the pass-only gate still refuses the ship.
func TestShipHealRecordsPartialThenRefuses(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DROSS_NO_TELEMETRY", "")

	verifyPath := filepath.Join(dir, ".dross", "phases", "x", "verify.toml")
	body, _ := os.ReadFile(verifyPath)
	body = []byte(strings.Replace(string(body), `verdict = "pass"`, `verdict = "partial"`, 1))
	if err := os.WriteFile(verifyPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCmd(t, Ship(), "--no-push")
	if err == nil {
		t.Fatal("expected refusal for partial verdict")
	}
	if !strings.Contains(err.Error(), "force-unverified") {
		t.Errorf("refusal should mention --force-unverified: %v", err)
	}
	telemBody := mustRead(t, filepath.Join(home, ".claude/dross", "telemetry.jsonl"))
	if !strings.Contains(telemBody, `"verdict":"partial"`) {
		t.Errorf("partial verdict should be recorded before the refusal:\n%s", telemBody)
	}
}

// TestShipRecordsShippedNotCompleted (c-2): ship records the intermediate
// truth and nothing more. A phase is not complete until its PR is merged, and
// ship runs before that is known — so it marks the phase `shipped`, leaves
// current_phase set, and writes no `completed <id>` breadcrumb. Restoring
// either clear fails this. The record stays machine-local, as before.
func TestShipRecordsShippedNotCompleted(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}

	s, err := state.Load(filepath.Join(dir, ".dross", state.File))
	if err != nil {
		t.Fatal(err)
	}
	if s.CurrentPhase != "x" {
		t.Errorf("ship must leave current_phase set — only a confirmed merge clears it; got %q", s.CurrentPhase)
	}
	if s.CurrentPhaseStatus != "shipped" {
		t.Errorf("ship should set current_phase_status to shipped, got %q", s.CurrentPhaseStatus)
	}
	shipped := false
	for _, a := range s.History {
		if strings.Contains(a.Action, "completed x") {
			t.Errorf("ship must not write the completion record — that is phase complete's: %+v", s.History)
		}
		if strings.Contains(a.Action, "shipped x") {
			shipped = true
		}
	}
	if !shipped {
		t.Errorf("ship should record `shipped x` locally: %+v", s.History)
	}
	// …and nothing on HEAD carries it.
	if gitAllowFail(dir, "cat-file", "-e", "HEAD:.dross/"+state.File) {
		t.Error("HEAD carries .dross/state.json — the record must stay machine-local")
	}
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("ship should leave a clean tree, got: %q", st)
	}
}

// TestShipNarratesCompleteOwnsRecord (c-3): the post-PR narration must not
// claim the completion record rides the squash — it doesn't any more. It has
// to name the command that actually writes it, so the user knows the run isn't
// finished at ship time.
func TestShipNarratesCompleteOwnsRecord(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("ship: %v", err)
		}
	})

	// The retired phrases come from retiredSquashClaims (squash_claim_guard_test.go)
	// rather than being spelled out here: that file is the only one the
	// repo-wide scan skips, so naming a forbidden phrase anywhere else would
	// trip the scan with the very assertion that forbids it.
	lower := strings.ToLower(out)
	for _, stale := range retiredSquashClaims {
		if strings.Contains(lower, stale) {
			t.Errorf("ship still narrates the retired claim %q:\n%s", stale, out)
		}
	}
	if !strings.Contains(out, "dross phase complete") {
		t.Errorf("ship should name `dross phase complete` as the completion-record writer:\n%s", out)
	}
}

// TestPromptsNeverStageStateJSONByPath (c-3): an explicit `git add
// .dross/state.json` in a shipped prompt hard-fails every run of that slash
// command once the file is gitignored. The directory form is the only safe one.
func TestPromptsNeverStageStateJSONByPath(t *testing.T) {
	dir := filepath.Join(repoRootFromTest(t), "assets", "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "git add") && strings.Contains(line, state.File) {
				t.Errorf("%s:%d stages state.json by explicit path: %s", e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestAutoCommitDrossDirtLeavesTheIndexEmpty is the evidence behind
// ship.go:264's acceptance.
//
// Ship's staged-changes guard reads `git diff --cached --quiet` and commits
// whatever is left staged — but by the time it runs, autoCommitDrossDirt has
// already `git add .dross` and committed, and the pre-stage gate refuses any
// non-.dross dirt before anything is staged at all. So the index is empty on
// every path into that guard and its commit branch cannot be reached.
//
// This pins the premise rather than the conclusion: if autoCommitDrossDirt ever
// stops draining the index, this fails and the branch becomes reachable — at
// which point it owes a test, not a reason.
func TestAutoCommitDrossDirtLeavesTheIndexEmpty(t *testing.T) {
	cases := []struct {
		name  string
		dirty bool
	}{
		{"with .dross dirt to commit", true},
		{"with nothing to commit", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir, "")
			chdir(t, dir)
			if err := runCmd(t, Init()); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dir, "README.md"), "base\n")
			gitCommit(t, dir, "baseline")

			if tc.dirty {
				mustWrite(t, filepath.Join(dir, ".dross", "handoff.md"), "# handoff\n")
			}

			committed, err := autoCommitDrossDirt(dir, "shipping")
			if err != nil {
				t.Fatalf("autoCommitDrossDirt: %v", err)
			}
			if committed != tc.dirty {
				t.Errorf("committed = %v, want %v", committed, tc.dirty)
			}

			// The index must be empty afterwards — that is what makes ship's
			// later staged-changes commit unreachable.
			if err := gitNoOut(dir, "diff", "--cached", "--quiet"); err != nil {
				staged := mustGit(t, dir, "diff", "--cached", "--name-only")
				t.Errorf("the index is not empty after autoCommitDrossDirt (%v) — ship.go's "+
					"staged-changes commit is now reachable and needs a test, not an acceptance. Staged:\n%s",
					err, staged)
			}
		})
	}
}

// ---- push-gates-on-origin: the record push is gated on origin and the
// shipped flip happens after it (c-1, c-3, c-4). ----

// installPreReceive writes a pre-receive hook on the bare origin. The script
// reads the standard `old new ref` lines on stdin.
func installPreReceive(t *testing.T, remoteDir, script string) string {
	t.Helper()
	hook := filepath.Join(remoteDir, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return hook
}

// rejectSubjectHook is a pre-receive script that refuses any push whose new
// tip's subject contains needle, and accepts everything else.
func rejectSubjectHook(needle string) string {
	return `while read old new ref; do
  if git log -1 --format=%s "$new" | grep -q '` + needle + `'; then
    echo 'refused by policy hook' >&2
    exit 1
  fi
done
exit 0
`
}

// installForeignCommitAfterPush writes a post-receive hook on the bare origin
// that, once, lands a foreign commit on top of the pushed phase/x tip — but
// only when the pushed tip's subject contains needle ("" = the first push of
// any subject). It simulates a review commit pushed from elsewhere in the
// window between two of ship's own pushes.
func installForeignCommitAfterPush(t *testing.T, remoteDir, needle string) {
	t.Helper()
	hook := filepath.Join(remoteDir, "hooks", "post-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
marker="$(dirname "$0")/foreign-done"
[ -e "$marker" ] && exit 0
export GIT_AUTHOR_NAME=other GIT_AUTHOR_EMAIL=other@example.com
export GIT_COMMITTER_NAME=other GIT_COMMITTER_EMAIL=other@example.com
while read old new ref; do
  [ "$ref" = "refs/heads/phase/x" ] || continue
  if [ -n "` + needle + `" ] && ! git log -1 --format=%s "$new" | grep -q '` + needle + `'; then
    continue
  fi
  tree=$(git rev-parse "$new^{tree}")
  c=$(echo "review: foreign commit from elsewhere" | git commit-tree "$tree" -p "$new")
  git update-ref refs/heads/phase/x "$c" "$new"
  touch "$marker"
done
exit 0
`
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func loadChangesX(t *testing.T, dir string) *changes.Changes {
	t.Helper()
	ch, err := changes.Load(changes.FilePath(filepath.Join(dir, ".dross"), "x"), "x")
	if err != nil {
		t.Fatalf("load changes.json: %v", err)
	}
	return ch
}

func loadStateT(t *testing.T, dir string) *state.State {
	t.Helper()
	st, err := state.Load(filepath.Join(dir, ".dross", state.File))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func historyCount(st *state.State, needle string) int {
	n := 0
	for _, a := range st.History {
		if strings.Contains(a.Action, needle) {
			n++
		}
	}
	return n
}

func originChangesX(t *testing.T, remoteDir string) changes.Changes {
	t.Helper()
	var pushed changes.Changes
	if err := json.Unmarshal([]byte(mustGit(t, remoteDir, "show", "phase/x:.dross/phases/x/changes.json")), &pushed); err != nil {
		t.Fatalf("parse pushed changes.json: %v", err)
	}
	return pushed
}

// TestShipFailedRecordPushIsNotShipped is c-4's load-bearing case. The first
// push and the PR open succeed; the push carrying the PR record is refused.
// The phase must NOT read shipped anywhere, the record commit stays local as
// the retry unit (failed_push_residue), status names the retry — and a second
// run pushes the record so origin's tip carries the PR number, opening no
// second PR (c-3).
func TestShipFailedRecordPushIsNotShipped(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap, remoteDir := shipMockFlowRemote(t, dir)
	// status answers the shipped/pending question from local refs only and
	// stays silent without origin/<base>; give it one.
	mustGit(t, dir, "push", "-q", "origin", "main")
	hook := installPreReceive(t, remoteDir, rejectSubjectHook("record PR"))

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("ship must fail when the record push is refused")
	}
	if !strings.Contains(err.Error(), "dross ship x") {
		t.Errorf("error should name the retry `dross ship x`: %v", err)
	}

	st := loadStateT(t, dir)
	if st.CurrentPhaseStatus == "shipped" {
		t.Error("state reads shipped although the record never reached origin")
	}
	if historyCount(st, "shipped x") != 0 {
		t.Errorf("history carries `shipped x` after a failed record push: %+v", st.History)
	}
	ch := loadChangesX(t, dir)
	if ch.PR != 99 {
		t.Errorf("local record should carry pr 99 (the retry unit), got %d", ch.PR)
	}
	if ch.Status == changes.StatusShipped {
		t.Error("local changes.json reads shipped — the status must not ride the record commit")
	}
	if msg := mustGit(t, dir, "log", "-1", "--pretty=%s"); msg != "chore(dross): record PR #99 for x" {
		t.Errorf("HEAD should be the local record commit, got %q", msg)
	}
	if pushed := originChangesX(t, remoteDir); pushed.PR != 0 {
		t.Errorf("origin's changes.json should not carry the PR yet, got %d", pushed.PR)
	}
	if phase.Done(filepath.Join(dir, ".dross"), "x") {
		t.Error("phase.Done reads true for a phase whose record never reached origin")
	}
	listOut := captureStdout(t, func() { runCmd(t, Phase(), "list") })
	if strings.Contains(listOut, "✓ x") {
		t.Errorf("phase list ticks x as done:\n%s", listOut)
	}
	statusOut := captureStdout(t, func() { runCmd(t, Status()) })
	if strings.Contains(statusOut, "shipped:") {
		t.Errorf("status reports shipped:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, "dross ship") {
		t.Errorf("status should name the ship retry:\n%s", statusOut)
	}

	// Second run: the refusal is gone. The record is ahead of origin and goes
	// out at the origin gate; no second PR; the flip lands.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("second ship should push the record: %v", err)
		}
	})
	if cap.posts != 1 {
		t.Errorf("POST /pulls across both runs = %d, want 1", cap.posts)
	}
	if pushed := originChangesX(t, remoteDir); pushed.PR != 99 || pushed.Status != changes.StatusShipped {
		t.Errorf("origin's changes.json after the retry: pr=%d status=%q, want 99/shipped", pushed.PR, pushed.Status)
	}
	if st := loadStateT(t, dir); st.CurrentPhaseStatus != "shipped" {
		t.Errorf("state should read shipped after the retry, got %q", st.CurrentPhaseStatus)
	}
	if st := mustGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("tree should be clean after the retry, got %q", st)
	}
	if !strings.Contains(out, "#99") {
		t.Errorf("retry should report the existing PR by number:\n%s", out)
	}
}

// TestShipPushesLocalOnlyRecordWithNothingStaged (c-1): after a clean ship,
// wind origin's phase/x back one commit. The index is clean, so the old
// `diff --cached --quiet` gate would push nothing; the origin gate sees the
// local-only commit and pushes it.
func TestShipPushesLocalOnlyRecordWithNothingStaged(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap, remoteDir := shipMockFlowRemote(t, dir)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	tip := mustGit(t, remoteDir, "rev-parse", "phase/x")
	mustGit(t, remoteDir, "update-ref", "refs/heads/phase/x", tip+"~1")
	if staged := mustGit(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("fixture broke: index must be clean, got %q", staged)
	}

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if got := mustGit(t, remoteDir, "rev-parse", "phase/x"); got != mustGit(t, dir, "rev-parse", "HEAD") {
		t.Errorf("origin phase/x = %s, want local HEAD — the local-only commit was not pushed", got)
	}
	if cap.posts != 1 {
		t.Errorf("POST /pulls = %d, want 1", cap.posts)
	}
}

// TestShipInSyncIssuesNoPush: a re-run on a branch already level with origin
// pushes nothing at all — a hook that refuses every push must never fire.
func TestShipInSyncIssuesNoPush(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	_, remoteDir := shipMockFlowRemote(t, dir)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	marker := filepath.Join(remoteDir, "hooks", "push-attempted")
	installPreReceive(t, remoteDir, "touch '"+marker+"'\necho refused >&2\nexit 1\n")

	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("re-run on an in-sync branch must not push, got: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a push was attempted on a branch already level with origin")
	}
}

// TestShipDivergedPhaseBranchRefusesThenForces (diverged_phase_branch): a
// foreign commit on origin's phase/x plus a local commit is a divergence;
// the re-run refuses naming the pull and --force, opens nothing, moves
// nothing. With --force the re-run lands local HEAD on origin.
func TestShipDivergedPhaseBranchRefusesThenForces(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap, remoteDir := shipMockFlowRemote(t, dir)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	pushForeignToOrigin(t, dir)
	commitLocal(t, dir, "review-fix.txt")
	remoteBefore := mustGit(t, remoteDir, "rev-parse", "phase/x")

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("a diverged phase branch must refuse")
	}
	for _, want := range []string{"git pull", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
	if cap.posts != 1 {
		t.Errorf("POST /pulls = %d, want 1", cap.posts)
	}
	if after := mustGit(t, remoteDir, "rev-parse", "phase/x"); after != remoteBefore {
		t.Errorf("refusal must move nothing: origin phase/x %s -> %s", remoteBefore, after)
	}

	if err := runCmd(t, Ship(), "--force"); err != nil {
		t.Fatalf("--force re-run: %v", err)
	}
	if got := mustGit(t, remoteDir, "rev-parse", "phase/x"); got != mustGit(t, dir, "rev-parse", "HEAD") {
		t.Errorf("after --force origin phase/x = %s, want local HEAD", got)
	}
}

// TestShipMarkerPushFailureIsShippedButErrors pins stage (f)'s deliberate
// second retry state: the record push landed, both markers flipped, and only
// the push of the shipped-marker commit failed. The phase IS shipped — both
// markers say so, only origin's copy lags — so the error says shipped and
// names the re-run, --json is still emitted, and the re-run pushes the marker.
func TestShipMarkerPushFailureIsShippedButErrors(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	_, remoteDir := shipMockFlowRemote(t, dir)
	hook := installPreReceive(t, remoteDir, rejectSubjectHook("mark x shipped"))

	var err error
	out := captureStdout(t, func() { err = runCmd(t, Ship(), "--json") })
	if err == nil {
		t.Fatal("a refused marker push must return an error")
	}
	for _, want := range []string{"shipped", "dross ship"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	var obj struct {
		Number int `json:"number"`
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); jerr != nil || obj.Number != 99 {
		t.Errorf("--json must still be emitted with number 99 before the error, got %q (%v)", out, jerr)
	}
	if st := loadStateT(t, dir); st.CurrentPhaseStatus != "shipped" {
		t.Errorf("state should read shipped — the record is on origin; got %q", st.CurrentPhaseStatus)
	}
	if ch := loadChangesX(t, dir); ch.Status != changes.StatusShipped {
		t.Errorf("changes.json should read shipped, got %q", ch.Status)
	}

	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("re-run should push the marker: %v", err)
	}
	if msg := mustGit(t, remoteDir, "log", "-1", "--pretty=%s", "phase/x"); msg != "chore(dross): mark x shipped" {
		t.Errorf("origin tip should be the marker commit after the re-run, got %q", msg)
	}
}

// TestShipFlipIsAtomic: the two markers flip together — both true after a
// clean ship, both false after the c-4 failure.
func TestShipFlipIsAtomic(t *testing.T) {
	t.Run("clean ship", func(t *testing.T) {
		dir := shipFixture(t, "https://forge.example/me/p.git")
		shipMockFlow(t, dir)
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("ship: %v", err)
		}
		stateShipped := loadStateT(t, dir).CurrentPhaseStatus == "shipped"
		recordShipped := loadChangesX(t, dir).Status == changes.StatusShipped
		if !stateShipped || !recordShipped {
			t.Errorf("markers disagree after a clean ship: state=%v record=%v", stateShipped, recordShipped)
		}
	})
	t.Run("failed record push", func(t *testing.T) {
		dir := shipFixture(t, "https://forge.example/me/p.git")
		_, remoteDir := shipMockFlowRemote(t, dir)
		installPreReceive(t, remoteDir, rejectSubjectHook("record PR"))
		if err := runCmd(t, Ship()); err == nil {
			t.Fatal("expected the record push to be refused")
		}
		stateShipped := loadStateT(t, dir).CurrentPhaseStatus == "shipped"
		recordShipped := loadChangesX(t, dir).Status == changes.StatusShipped
		if stateShipped || recordShipped {
			t.Errorf("a marker flipped before the record push landed: state=%v record=%v", stateShipped, recordShipped)
		}
	})
}

// TestShipJSONReportsExistingPR pins the re-run's --json shape: the existing
// number, result "existing", and an EMPTY url — changes.json stores none, and
// a fabricated one would be a lie.
func TestShipJSONReportsExistingPR(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship(), "--json"); err != nil {
			t.Fatalf("re-run --json: %v", err)
		}
	})
	var obj struct {
		URL    string `json:"url"`
		Number int    `json:"number"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("parse --json output %q: %v", out, err)
	}
	if obj.Number != 99 || obj.Result != "existing" || obj.URL != "" {
		t.Errorf("got %+v, want {URL:\"\" Number:99 Result:existing}", obj)
	}
}

// TestShipRecordPushDivergedNamesPull: a foreign commit lands on origin's
// phase/x between the branch push (b) and the record push (e). The record
// push refuses with pushPhaseBranch's own guidance — pull and --force — not
// the bare re-run, which would loop on the same refusal. The record commit
// stays as HEAD; nothing reads shipped.
func TestShipRecordPushDivergedNamesPull(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	_, remoteDir := shipMockFlowRemote(t, dir)
	installForeignCommitAfterPush(t, remoteDir, "")

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("the record push into a diverged branch must refuse")
	}
	for _, want := range []string{"git pull --rebase origin phase/x", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
	if msg := mustGit(t, dir, "log", "-1", "--pretty=%s"); msg != "chore(dross): record PR #99 for x" {
		t.Errorf("HEAD should be the record commit, got %q", msg)
	}
	if st := loadStateT(t, dir); st.CurrentPhaseStatus == "shipped" {
		t.Error("state reads shipped although the record never reached origin")
	}
}

// TestShipMarkerPushDivergedNamesPull: the foreign commit lands between the
// record push (e) and the marker push (f). The phase is shipped — both
// markers flipped — and the error carries the pull / --force guidance so the
// re-run cannot loop on the same refusal.
func TestShipMarkerPushDivergedNamesPull(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	_, remoteDir := shipMockFlowRemote(t, dir)
	installForeignCommitAfterPush(t, remoteDir, "record PR")

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("the marker push into a diverged branch must refuse")
	}
	for _, want := range []string{"shipped", "git pull --rebase origin phase/x", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
	if st := loadStateT(t, dir); st.CurrentPhaseStatus != "shipped" {
		t.Errorf("state should read shipped — the record is on origin; got %q", st.CurrentPhaseStatus)
	}
	if ch := loadChangesX(t, dir); ch.Status != changes.StatusShipped {
		t.Errorf("changes.json should read shipped, got %q", ch.Status)
	}
}

// TestShipBehindOnlyRefusesBeforeOpen: origin's phase/x is one commit ahead
// of local before the first ship. The origin gate refuses naming the pull —
// before any PR exists, before any record commit.
func TestShipBehindOnlyRefusesBeforeOpen(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap, _ := shipMockFlowRemote(t, dir)
	mustGit(t, dir, "push", "-q", "-u", "origin", "phase/x")
	pushForeignToOrigin(t, dir)

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("a behind-only phase branch must refuse")
	}
	if !strings.Contains(err.Error(), "git pull --rebase origin phase/x") {
		t.Errorf("error should name the pull: %v", err)
	}
	if cap.posts != 0 {
		t.Errorf("POST /pulls = %d, want 0 — refused before open", cap.posts)
	}
	if log := mustGit(t, dir, "log", "--pretty=%s"); strings.Contains(log, "record PR") {
		t.Errorf("no record commit may exist:\n%s", log)
	}
}

// ---- c-5: provider fallback when the record carries no PR number. ----

// headStub records what the FindOpenPRByHead seam was asked.
type headStub struct {
	calls int
	heads []string
}

// stubOpenPRByHead replaces ship.FindOpenPRByHeadFunc for the test (restored
// on cleanup, mirroring stubPRMerged) with one that returns res/err and
// records each call.
func stubOpenPRByHead(t *testing.T, res *ship.OpenResult, err error) *headStub {
	t.Helper()
	st := &headStub{}
	prev := ship.FindOpenPRByHeadFunc
	ship.FindOpenPRByHeadFunc = func(_ ship.OpenOpts, head string) (*ship.OpenResult, error) {
		st.calls++
		st.heads = append(st.heads, head)
		return res, err
	}
	t.Cleanup(func() { ship.FindOpenPRByHeadFunc = prev })
	return st
}

// TestShipRecoversPRAfterDeathBeforeRecord is c-5's load-bearing shape: ship
// died between opening the PR and writing the record, so changes.json carries
// no number. The re-run asks the provider by head, records the hit, opens no
// second PR, and pushes the record so origin's tip carries it.
func TestShipRecoversPRAfterDeathBeforeRecord(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap, remoteDir := shipMockFlowRemote(t, dir)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	// Model the death: the record loses its PR number (and the status that
	// only ever follows it).
	ch := loadChangesX(t, dir)
	ch.PR = 0
	ch.Status = ""
	if err := ch.Save(changes.FilePath(filepath.Join(dir, ".dross"), "x")); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "test: drop the PR record")
	stub := stubOpenPRByHead(t, &ship.OpenResult{Number: 77, URL: "https://forge.example/me/p/pulls/77"}, nil)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("re-run: %v", err)
		}
	})
	if stub.calls != 1 || len(stub.heads) != 1 || stub.heads[0] != "phase/x" {
		t.Errorf("lookup calls = %d heads = %v, want one call with phase/x", stub.calls, stub.heads)
	}
	if cap.posts != 1 {
		t.Errorf("POST /pulls = %d, want 1 — the re-run opened a second PR", cap.posts)
	}
	if pushed := originChangesX(t, remoteDir); pushed.PR != 77 {
		t.Errorf("origin's changes.json should carry the recovered PR 77, got %d", pushed.PR)
	}
	if !strings.Contains(out, "#77") {
		t.Errorf("re-run should report the recovered PR by number:\n%s", out)
	}
}

// A first ship with nothing open: the lookup says none, exactly one PR opens.
func TestShipLookupNoneOpensOnce(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)
	stub := stubOpenPRByHead(t, nil, nil)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	if stub.calls != 1 || cap.posts != 1 {
		t.Errorf("lookup calls = %d, POST /pulls = %d; want 1 and 1", stub.calls, cap.posts)
	}
}

// A lookup that fails refuses BEFORE opening — fail-closed. "Could not check"
// read as "none" is the duplicate the lookup exists to prevent.
func TestShipLookupFailureRefusesBeforeOpen(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)
	stubOpenPRByHead(t, nil, errors.New("boom"))

	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("a failed lookup must refuse")
	}
	if !strings.Contains(err.Error(), "dross ship") {
		t.Errorf("error should name the re-run: %v", err)
	}
	if cap.posts != 0 {
		t.Errorf("POST /pulls = %d, want 0", cap.posts)
	}
	if log := mustGit(t, dir, "log", "--pretty=%s"); strings.Contains(log, "record PR") {
		t.Errorf("no record commit may exist:\n%s", log)
	}
	if ch := loadChangesX(t, dir); ch.PR != 0 {
		t.Errorf("changes.json pr should stay 0, got %d", ch.PR)
	}
}

// An unwired provider announces the skip and opens.
func TestShipLookupUnsupportedNarratesSkipAndOpens(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	cap := shipMockFlow(t, dir)
	stubOpenPRByHead(t, nil, ship.ErrHeadPRLookupUnsupported)

	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("ship: %v", err)
		}
	})
	if !strings.Contains(out, "skipped") {
		t.Errorf("the skip must be announced:\n%s", out)
	}
	if cap.posts != 1 {
		t.Errorf("POST /pulls = %d, want 1", cap.posts)
	}
}

// Record first: with pr 99 recorded, the provider is never asked on a
// re-run. A lookup-first mutant fails here.
func TestShipRecordWinsOverLookup(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	stub := stubOpenPRByHead(t, &ship.OpenResult{Number: 555}, nil)
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("lookup calls = %d, want 0 — the record wins, the provider is never asked", stub.calls)
	}
	if ch := loadChangesX(t, dir); ch.PR != 99 {
		t.Errorf("record should still read 99, got %d", ch.PR)
	}
}

// --no-push and --print-body return before any network: no lookup.
func TestShipEarlyReturnsSkipLookup(t *testing.T) {
	for _, flag := range []string{"--no-push", "--print-body"} {
		t.Run(flag, func(t *testing.T) {
			dir := shipFixture(t, "https://forge.example/me/p.git")
			shipMockFlow(t, dir)
			stub := stubOpenPRByHead(t, nil, errors.New("must not be called"))
			captureStdout(t, func() {
				if err := runCmd(t, Ship(), flag); err != nil {
					t.Fatalf("ship %s: %v", flag, err)
				}
			})
			if stub.calls != 0 {
				t.Errorf("%s: lookup calls = %d, want 0", flag, stub.calls)
			}
		})
	}
}

// TestShipRecoverFetchFailureOutputGoesToStderr: git's own output can quote
// remote responses and hook output, so gitRun prints it to stderr and the
// error carries git's exit status only. A stub git on PATH prints a canary and
// fails the fetch; every other git call runs for real.
func TestShipRecoverFetchFailureOutputGoesToStderr(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitInit(t, dir, filepath.Join(t.TempDir(), "origin.git"))
	mustWrite(t, filepath.Join(dir, ".dross", "project.toml"), "[project]\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-q", "-m", "init")

	stub := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = fetch ]; then echo CANARY-GIT; echo CANARY-GIT >&2; exit 1; fi\ndone\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stub, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stderr strings.Builder
	prev := gitrun.Stderr
	gitrun.Stderr = &stderr
	defer func() { gitrun.Stderr = prev }()

	err = runDrossRecovery(dir, filepath.Join(dir, ".dross"), &state.State{}, "p", "", "main")
	if err == nil {
		t.Fatal("a failing fetch did not fail the recovery")
	}
	if strings.Contains(err.Error(), "CANARY-GIT") {
		t.Errorf("git's output reached the error: %q", err)
	}
	if !strings.Contains(err.Error(), "git fetch") || !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("err = %q, want the subcommand and git's exit status", err)
	}
	if !strings.Contains(stderr.String(), "CANARY-GIT") {
		t.Errorf("stderr = %q, want git's own output there", stderr.String())
	}
}

// TestShipPrintBodyShowsReviews (solo-task-review c-6): the body ship opens
// the PR with carries what the solo reviewer flagged, read from changes.json.
func TestShipPrintBodyShowsReviews(t *testing.T) {
	dir := shipFixture(t, "https://forge.example/me/p.git")
	if err := changes.SetReview(filepath.Join(dir, ".dross"), "x", "t-1", changes.TaskReview{
		Outcome: "pass", Rounds: 2, Findings: []changes.ReviewFinding{
			{Round: 1, Kind: "spec", Severity: "BLOCKING", Criterion: "c-1", Text: "REVIEWER-SAW-THIS", Resolution: "fixed in the fix round"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship(), "--print-body"); err != nil {
			t.Errorf("ship --print-body: %v", err)
		}
	})
	if !strings.Contains(out, "## Reviewer findings") || !strings.Contains(out, "REVIEWER-SAW-THIS") {
		t.Fatalf("--print-body omits the reviewer findings:\n%s", out)
	}
}

// --- Azure DevOps end to end (azure-devops-provider c-2, c-4..c-7) ---

const adoShipPAT = "ado-ship-pat"

// adoShipCall is one request the mock Azure DevOps Server collection saw.
type adoShipCall struct {
	method, path string
	apiVersion   []string
	auth         []string
}

type adoShipRoute struct {
	method, part string
	status       int
	body         string
}

type adoShipMock struct {
	*httptest.Server
	mu    sync.Mutex
	calls []adoShipCall
}

func (m *adoShipMock) seen() []adoShipCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]adoShipCall(nil), m.calls...)
}

func (m *adoShipMock) count(method, part string) int {
	n := 0
	for _, c := range m.seen() {
		if c.method == method && strings.Contains(c.path, part) {
			n++
		}
	}
	return n
}

// newADOShipMock answers each request from the first route whose method
// matches and whose path contains part. Anything unrouted is a failure: the
// flows below pin every request ship makes.
func newADOShipMock(t *testing.T, routes ...adoShipRoute) *adoShipMock {
	t.Helper()
	m := &adoShipMock{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.calls = append(m.calls, adoShipCall{r.Method, r.URL.EscapedPath(), r.URL.Query()["api-version"], r.Header.Values("Authorization")})
		m.mu.Unlock()
		for _, rt := range routes {
			if r.Method == rt.method && strings.Contains(r.URL.EscapedPath(), rt.part) {
				w.WriteHeader(rt.status)
				_, _ = w.Write([]byte(rt.body))
				return
			}
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(m.Close)
	return m
}

// adoShipRemote writes a mock Server collection into [remote] through
// `dross project set`, exactly as a user would — so a key the setter refuses
// (an enum that lags ShipProviders, say) fails the flow here.
func adoShipRemote(t *testing.T, m *adoShipMock, reviewers string) {
	t.Helper()
	t.Setenv("MOCK_ADO_PAT", adoShipPAT)
	sets := [][]string{
		{"set", "remote.provider", "azure-devops"},
		{"set", "remote.url", m.URL + "/coll/P/_git/R"},
		{"set", "remote.api_base", m.URL + "/coll"},
		{"set", "remote.api_version", "6.0"},
		{"set", "remote.auth_env", "MOCK_ADO_PAT"},
		{"set", "remote.reviewers", reviewers},
	}
	if reviewers == "" {
		sets[len(sets)-1] = []string{"set", "--unset", "remote.reviewers"}
	}
	for _, set := range sets {
		if err := runCmd(t, Project(), set...); err != nil {
			t.Fatalf("project %v: %v", set, err)
		}
	}
}

// adoShipFixture is shipFixture re-pointed at a mock Azure DevOps Server: the
// head lookup is the real FindOpenPRByHead again, origin is a bare repo so the
// push lands, and [remote] names the mock collection.
func adoShipFixture(t *testing.T, reviewers string, routes ...adoShipRoute) (dir, remoteDir string, m *adoShipMock) {
	t.Helper()
	dir = shipFixture(t, "https://forge.example/me/p.git")
	prev := ship.FindOpenPRByHeadFunc
	ship.FindOpenPRByHeadFunc = ship.FindOpenPRByHead
	t.Cleanup(func() { ship.FindOpenPRByHeadFunc = prev })

	m = newADOShipMock(t, routes...)
	remoteDir = t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)
	adoShipRemote(t, m, reviewers)
	gitCommit(t, dir, "test: point [remote] at the mock collection")
	return dir, remoteDir, m
}

var (
	adoShipNone    = adoShipRoute{"GET", "/pullrequests", 200, `{"count":0,"value":[]}`}
	adoShipCreated = adoShipRoute{"POST", "/pullrequests", 201, `{"pullRequestId":42}`}
)

// TestShipAzureDevOpsFullFlow: a first ship asks for an open PR by source,
// finds none, opens one — every call on the collection's repo endpoint, at
// the configured api-version, with one Basic :PAT credential — and records it.
func TestShipAzureDevOpsFullFlow(t *testing.T) {
	dir, remoteDir, m := adoShipFixture(t, "", adoShipNone, adoShipCreated)
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("ship: %v", err)
		}
	})

	calls := m.seen()
	if len(calls) != 2 || calls[0].method != "GET" || calls[1].method != "POST" {
		t.Fatalf("want the lookup GET, then the create POST; got %+v", calls)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+adoShipPAT))
	for _, c := range calls {
		if !strings.HasPrefix(c.path, "/coll/P/_apis/git/repositories/R/") {
			t.Errorf("%s %s is not on the collection's repo endpoint", c.method, c.path)
		}
		if len(c.apiVersion) != 1 || c.apiVersion[0] != "6.0" {
			t.Errorf("%s %s: api-version = %v, want [6.0]", c.method, c.path, c.apiVersion)
		}
		if len(c.auth) != 1 || c.auth[0] != wantAuth {
			t.Errorf("%s %s: Authorization = %v, want exactly one Basic :PAT", c.method, c.path, c.auth)
		}
	}

	opened := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "PR opened: ") {
			opened = line
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(opened), "/coll/P/_git/R/pullrequest/42 (#42)") {
		t.Errorf("want 'PR opened: .../_git/R/pullrequest/42 (#42)', got %q in:\n%s", opened, out)
	}
	if ch := loadChangesX(t, dir); ch.PR != 42 {
		t.Errorf("changes.json pr = %d, want 42", ch.PR)
	}
	if pushed := originChangesX(t, remoteDir); pushed.PR != 42 {
		t.Errorf("origin's changes.json pr = %d, want 42", pushed.PR)
	}
}

// TestShipAzureDevOpsDedupeHit: an active PR from phase/x already exists, so
// ship records it and opens nothing.
func TestShipAzureDevOpsDedupeHit(t *testing.T) {
	dir, _, m := adoShipFixture(t, "",
		adoShipRoute{"GET", "/pullrequests", 200, `{"count":1,"value":[{"pullRequestId":17,"status":"active","sourceRefName":"refs/heads/phase/x"}]}`},
		adoShipCreated)
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("ship: %v", err)
		}
	})
	if n := m.count("POST", "/pullrequests"); n != 0 {
		t.Errorf("%d PR create(s) — the existing PR #17 was duplicated", n)
	}
	if !strings.Contains(out, "found open PR #17") {
		t.Errorf("want 'found open PR #17' in:\n%s", out)
	}
	if ch := loadChangesX(t, dir); ch.PR != 17 {
		t.Errorf("changes.json pr = %d, want 17", ch.PR)
	}
}

// TestShipAzureDevOpsLookupFailureRefuses: a lookup that fails refuses before
// opening — "could not check" read as "none" is the duplicate dedupe exists
// to prevent.
func TestShipAzureDevOpsLookupFailureRefuses(t *testing.T) {
	dir, _, m := adoShipFixture(t, "",
		adoShipRoute{"GET", "/pullrequests", 500, `{"message":"boom"}`},
		adoShipCreated)
	err := runCmd(t, Ship())
	if err == nil {
		t.Fatal("a failed lookup did not refuse")
	}
	if !strings.Contains(err.Error(), "could not check origin") || !strings.Contains(err.Error(), "dross ship x") {
		t.Errorf("want 'could not check origin' naming `dross ship x`, got: %v", err)
	}
	if n := m.count("POST", "/pullrequests"); n != 0 {
		t.Errorf("%d PR create(s) after a failed lookup", n)
	}
	if ch := loadChangesX(t, dir); ch.PR != 0 {
		t.Errorf("changes.json pr = %d, want 0", ch.PR)
	}
}

// TestShipAzureDevOpsReviewerFailureWarns: a reviewer the Identities API
// cannot find is a warning on an opened PR, never a failed ship.
func TestShipAzureDevOpsReviewerFailureWarns(t *testing.T) {
	dir, _, m := adoShipFixture(t, "alice@example.com",
		adoShipNone, adoShipCreated,
		adoShipRoute{"GET", "/_apis/identities", 404, `{"message":"not found"}`})
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship()); err != nil {
			t.Fatalf("a reviewer lookup failure failed the ship: %v", err)
		}
	})
	if n := m.count("GET", "/coll/_apis/identities"); n != 1 {
		t.Errorf("identity lookups on the collection = %d, want 1", n)
	}
	if !strings.Contains(out, "Warning:") {
		t.Errorf("want a Warning: line for the failed reviewer in:\n%s", out)
	}
	if ch := loadChangesX(t, dir); ch.PR != 42 {
		t.Errorf("changes.json pr = %d, want 42", ch.PR)
	}
}

// TestShipAzureDevOpsCommentPostsThread: `dross ship comment` on azure-devops
// posts one new thread on the PR, on the collection's repo endpoint.
func TestShipAzureDevOpsCommentPostsThread(t *testing.T) {
	shipCoverInitProject(t)
	m := newADOShipMock(t, adoShipRoute{"POST", "/threads", 200, `{"id":1}`})
	adoShipRemote(t, m, "")
	out := captureStdout(t, func() {
		if err := runCmd(t, Ship(), "comment", "--pr", "7", "--body", "panel findings"); err != nil {
			t.Fatalf("ship comment: %v", err)
		}
	})
	calls := m.seen()
	if len(calls) != 1 || calls[0].method != "POST" || calls[0].path != "/coll/P/_apis/git/repositories/R/pullRequests/7/threads" {
		t.Errorf("want one POST .../pullRequests/7/threads, got %+v", calls)
	}
	if !strings.Contains(out, "Posted comment to PR #7") {
		t.Errorf("want 'Posted comment to PR #7' in:\n%s", out)
	}
}
