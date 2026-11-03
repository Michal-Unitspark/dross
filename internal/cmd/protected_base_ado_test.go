package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/ship"
)

// ship and complete on a main-based phase on an azure-devops remote
// (azure-boards-backend c-10) — protected_base_test.go's flow, with Azure
// Repos in GitHub's place. origin's pre-receive hook logs every ref update
// and rejects main, as a blocking branch policy does (TF402455); the merges
// Azure Repos makes are played by the same scratch clone, behind the hook's
// back. GitHub's rules seam answers "unprotected" throughout, so only the ADO
// branch-policy read (ship.ADOBranchProtectionFunc) can route a chore PR.

// adoProtectedFlowRepo is protectedFlowRepo on an azure-devops [remote]: main
// pushed, a .dross chore committed on local main and not pushed, and a
// verified phase x checked out, ready to ship.
func adoProtectedFlowRepo(t *testing.T) (dir, origin string) {
	t.Helper()
	dir, origin = t.TempDir(), t.TempDir()
	mustGit(t, origin, "init", "--bare", "-q", "-b", "main")
	gitInit(t, dir, origin)
	chdir(t, dir)
	if err := runCmd(t, Init()); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, set := range [][]string{
		{"set", "remote.provider", "azure-devops"},
		{"set", "remote.url", adoChoreRepoURL},
		{"set", "remote.api_base", "https://dev.azure.com/o"},
		{"set", "remote.auth_env", adoChoreEnv},
		{"set", "repo.git_main_branch", "main"},
	} {
		if err := runCmd(t, Project(), set...); err != nil {
			t.Fatalf("project %v: %v", set, err)
		}
	}
	mustWrite(t, filepath.Join(dir, "README.md"), "base\n")
	gitCommit(t, dir, "initial baseline")
	mustGit(t, dir, "push", "-q", "-u", "origin", "main")
	choreCommit(t, dir, "handoff.md") // a pause snapshot, unpushed

	if err := runCmd(t, Phase(), "create", "x"); err != nil {
		t.Fatalf("phase create: %v", err)
	}
	phaseDir := filepath.Join(dir, ".dross", "phases", "x")
	mustWrite(t, filepath.Join(phaseDir, "verify.toml"), "[verify]\nphase = \"x\"\ngenerated_at = 2026-05-02T10:00:00Z\nverdict = \"pass\"\n\n"+
		"[summary]\nmutation_score = 0.85\nmutants_killed = 17\nmutants_survived = 3\ncriteria_total = 1\ncriteria_covered = 1\ncriteria_uncovered = 0\n\n"+
		"[[criterion]]\nid = \"C1\"\nstatus = \"covered\"\ntests = [\"tag.test.ts:42\"]\n")
	mustWrite(t, filepath.Join(dir, "src/tag.ts"), "export const tag = 1\n")
	mustWrite(t, filepath.Join(phaseDir, "spec.toml"), "[phase]\nid = \"x\"\ntitle = \"Tagging\"\n\n[[criteria]]\nid = \"C1\"\ntext = \"Tags can be added\"\n")
	gitCommit(t, dir, "feat(tag): add tagging")
	return dir, origin
}

// adoPRURLs makes the PRs ghPlay opens answer with Azure Repos URLs.
func adoPRURLs(t *testing.T) {
	t.Helper()
	prev := ship.OpenPRFunc
	ship.OpenPRFunc = func(o ship.OpenOpts) (*ship.OpenResult, error) {
		res, err := prev(o)
		if res != nil {
			res.URL = adoChoreRepoURL + "/pullrequest/" + itoa(res.Number)
		}
		return res, err
	}
	t.Cleanup(func() { ship.OpenPRFunc = prev })
}

// TestProtectedADOBaseShipThenComplete: ship → Azure Repos squash-merges the
// phase PR and completes the ship chore PR → complete → Azure Repos completes
// the completion-record chore PR. dross never offers main to the hook, both
// commands succeed, every chore PR is armed with a merge commit, and local
// main ends level.
func TestProtectedADOBaseShipThenComplete(t *testing.T) {
	dir, origin := adoProtectedFlowRepo(t)
	log := protectedOrigin(t, origin, false)
	f := &ghPlay{protect: false}
	stubForge(t, f)
	adoPRURLs(t)
	asked := stubADOProtection(t, adoProtected)
	repos := newGithubSim(t, origin) // forge-agnostic: it only moves origin's refs

	out, err := runCapture(t, nil, func() error { return runCmd(t, Ship()) })
	if err != nil {
		t.Fatalf("ship: %v\n%s", err, out)
	}
	if !strings.Contains(out, "chore PR #") || !strings.Contains(out, adoChoreRepoURL+"/pullrequest/") {
		t.Errorf("ship did not narrate the chore PR at its Azure Repos URL:\n%s", out)
	}
	if originRef(t, origin, "refs/heads/dross-chores/main") == "" {
		t.Fatal("ship's .dross chores never reached dross-chores/main")
	}

	repos.squash()
	repos.mergeCommit("dross-chores/main")
	delete(f.open, "dross-chores/main")
	f.merged = true

	out, err = runCapture(t, nil, func() error { return runCmd(t, Phase(), "complete", "x") })
	if err != nil {
		t.Fatalf("complete: %v\n%s", err, out)
	}
	pr := f.open["dross-chores/main"]
	if pr == nil {
		t.Fatal("complete opened no completion-record chore PR")
	}
	if !strings.Contains(out, "completion record:") || !strings.Contains(out, pr.URL) {
		t.Errorf("complete did not narrate the completion-record chore PR %s:\n%s", pr.URL, out)
	}

	repos.mergeCommit("dross-chores/main")
	mustGit(t, dir, "fetch", "-q", "origin")
	if ahead := mustGit(t, dir, "rev-list", "origin/main..main"); ahead != "" {
		t.Errorf("local main is ahead of origin after the chore PRs completed: %s", ahead)
	}
	if got := baseUpdates(t, log); len(got) != 0 {
		t.Errorf("dross offered the hook %v", got)
	}
	if len(f.armed) < 2 {
		t.Errorf("armed %v; want both chore PRs armed", f.armed)
	}
	for _, m := range f.armed {
		if m != "merge" {
			t.Errorf("a chore PR was armed with %q", m)
		}
	}
	readMain := false
	for _, b := range *asked {
		readMain = readMain || b == "main"
	}
	if !readMain {
		t.Errorf("branch policies read for %v; want main", *asked)
	}
}

// TestUnprotectedADOBaseStillPushesDirectly: with no blocking policy on main,
// ship's safety net pushes the chores straight to origin, as before.
func TestUnprotectedADOBaseStillPushesDirectly(t *testing.T) {
	dir, origin := adoProtectedFlowRepo(t)
	f := &ghPlay{protect: false}
	stubForge(t, f)
	asked := stubADOProtection(t, ship.ADOProtection{Known: true})
	out, err := runCapture(t, nil, func() error { return runCmd(t, Ship()) })
	if err != nil {
		t.Fatalf("ship: %v\n%s", err, out)
	}
	if originRef(t, origin, "refs/heads/main") != mustGit(t, dir, "rev-parse", "main") {
		t.Error("an unprotected main's chores were not pushed directly")
	}
	if originRef(t, origin, "refs/heads/dross-chores/main") != "" {
		t.Error("an unprotected main got a chore branch")
	}
	if !strings.Contains(out, "pushed unpushed .dross chores on main to origin") {
		t.Errorf("the direct push is not narrated:\n%s", out)
	}
	if len(*asked) == 0 {
		t.Error("ship pushed without reading main's branch policies")
	}
}
