package cmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/ship"
)

// A protected milestone branch on an azure-devops remote (azure-boards-backend
// c-10): milestoneOrigin's hook stands in for Azure Repos refusing a push to a
// branch with a blocking policy, and the branch-policy read is the
// ship.ADOBranchProtectionFunc seam. GitHub's rules seam answers "unprotected"
// throughout, so only the ADO dispatch can route anything.

// adoMsRepo is a dross repo on an azure-devops remote with main pushed and
// milestone/<version> cut from it and pushed, checked out.
func adoMsRepo(t *testing.T, version string) (dir, origin string) {
	t.Helper()
	dir, origin = setupMilestoneRepo(t)
	path := filepath.Join(dir, ".dross", project.File)
	p, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Remote = project.Remote{Provider: "azure-devops", URL: adoChoreRepoURL, APIBase: "https://dev.azure.com/o", AuthEnv: adoChoreEnv}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "commit", "-q", "-am", "remote")
	mustGit(t, dir, "push", "-q", "-u", "origin", "main")
	mustGit(t, dir, "switch", "-q", "-c", "milestone/"+version)
	mustGit(t, dir, "push", "-q", "-u", "origin", "milestone/"+version)
	return dir, origin
}

func adoMsComplete(t *testing.T, version string) (string, error) {
	t.Helper()
	return runCapture(t, nil, func() error {
		return runCmd(t, Milestone(), "complete", version, "--force-unverified")
	})
}

// TestADOMilestoneChoresRouteThroughChorePR: .dross chores ahead on a
// protected milestone/v1.8 go through dross-chores/milestone-v1.8, armed with a
// merge commit; the hook is never offered an update of milestone/v1.8, and the
// integration PR opens only once the chore PR has completed.
func TestADOMilestoneChoresRouteThroughChorePR(t *testing.T) {
	t.Run("completed at once: the integration PR opens", func(t *testing.T) {
		dir, origin := adoMsRepo(t, "v1.8")
		choreCommit(t, dir, "scope.md")
		log := milestoneOrigin(t, origin, false)
		asked := stubADOProtection(t, adoProtected)
		stubBranchRules(t, ship.BranchRulesResult{Known: true})
		s := &choreSeams{autoRes: ship.AutoMergeResult{Merged: true}}
		stubChoreSeams(t, s)

		out, err := adoMsComplete(t, "v1.8")
		if err != nil {
			t.Fatalf("complete: %v\n%s", err, out)
		}
		heads := openedHeads(s)
		if len(heads) != 2 || heads[0] != "dross-chores/milestone-v1.8→milestone/v1.8" || heads[1] != "milestone/v1.8→main" {
			t.Errorf("opened %v; want the chore PR, then the integration PR", heads)
		}
		if originRef(t, origin, "refs/heads/dross-chores/milestone-v1.8") == "" {
			t.Error("the chores never reached dross-chores/milestone-v1.8")
		}
		if got := milestoneUpdates(t, log); len(got) != 0 {
			t.Errorf("the hook was offered %v", got)
		}
		if len(*asked) == 0 || (*asked)[0] != "milestone/v1.8" {
			t.Errorf("branch policies read for %v; want milestone/v1.8 first", *asked)
		}
		if len(s.armed) != 1 || s.armed[0] != "merge" {
			t.Errorf("armed %v; want one merge-commit auto-complete", s.armed)
		}
	})
	t.Run("still open: the integration PR waits", func(t *testing.T) {
		dir, origin := adoMsRepo(t, "v1.8")
		choreCommit(t, dir, "scope.md")
		log := milestoneOrigin(t, origin, false)
		stubADOProtection(t, adoProtected)
		stubBranchRules(t, ship.BranchRulesResult{Known: true})
		s := &choreSeams{autoRes: ship.AutoMergeResult{AutoEnabled: true}}
		stubChoreSeams(t, s)

		_, err := adoMsComplete(t, "v1.8")
		if err == nil || !strings.Contains(err.Error(), adoChoreRepoURL+"/pullrequest/41") {
			t.Fatalf("err = %v; want a wait naming .../pullrequest/41", err)
		}
		if !strings.Contains(err.Error(), "auto-complete is armed") {
			t.Errorf("err = %v; want it to say the armed chore PR completes itself", err)
		}
		if heads := openedHeads(s); len(heads) != 1 {
			t.Errorf("opened %v; the integration PR must wait for the chore PR", heads)
		}
		if got := milestoneUpdates(t, log); len(got) != 0 {
			t.Errorf("the hook was offered %v", got)
		}
	})
	t.Run("could not arm: the wait says complete it by hand", func(t *testing.T) {
		dir, origin := adoMsRepo(t, "v1.8")
		choreCommit(t, dir, "scope.md")
		log := milestoneOrigin(t, origin, false)
		stubADOProtection(t, adoProtected)
		stubBranchRules(t, ship.BranchRulesResult{Known: true})
		s := &choreSeams{autoErr: ship.ErrAutoMergeUnavailable}
		stubChoreSeams(t, s)

		out, err := adoMsComplete(t, "v1.8")
		if err == nil || !strings.Contains(err.Error(), adoChoreRepoURL+"/pullrequest/41") {
			t.Fatalf("err = %v; want a wait naming .../pullrequest/41", err)
		}
		if !strings.Contains(out, "complete it by hand") || !strings.Contains(err.Error(), "by hand") || strings.Contains(err.Error(), "auto-complete is armed") {
			t.Errorf("narration and wait disagree about an unarmed chore PR:\n%s\nerr = %v", out, err)
		}
		if heads := openedHeads(s); len(heads) != 1 {
			t.Errorf("opened %v; the integration PR must wait for the chore PR", heads)
		}
		if got := milestoneUpdates(t, log); len(got) != 0 {
			t.Errorf("the hook was offered %v", got)
		}
	})
	t.Run("unprotected: the chores are pushed", func(t *testing.T) {
		dir, origin := adoMsRepo(t, "v1.8")
		choreCommit(t, dir, "scope.md")
		stubADOProtection(t, ship.ADOProtection{Known: true})
		stubBranchRules(t, ship.BranchRulesResult{Known: true})
		s := &choreSeams{}
		stubChoreSeams(t, s)

		if out, err := adoMsComplete(t, "v1.8"); err != nil {
			t.Fatalf("complete: %v\n%s", err, out)
		}
		if originRef(t, origin, "refs/heads/milestone/v1.8") != mustGit(t, dir, "rev-parse", "milestone/v1.8") {
			t.Error("an unprotected milestone branch's chores were not pushed")
		}
		if heads := openedHeads(s); len(heads) != 1 || heads[0] != "milestone/v1.8→main" {
			t.Errorf("opened %v; want only the integration PR", heads)
		}
	})
	t.Run("unknown: nothing is pushed", func(t *testing.T) {
		dir, origin := adoMsRepo(t, "v1.8")
		before := originRef(t, origin, "refs/heads/milestone/v1.8")
		choreCommit(t, dir, "scope.md")
		log := milestoneOrigin(t, origin, true)
		stubADOProtection(t, ship.ADOProtection{Reason: "Azure DevOps answered 401"})
		stubBranchRules(t, ship.BranchRulesResult{Known: true})
		stubChoreSeams(t, &choreSeams{})

		_, _, err := pushMilestoneHeadIfAhead(dir, "milestone/v1.8")
		if err == nil || !strings.Contains(err.Error(), "answered 401") || !strings.Contains(err.Error(), "$"+adoChoreEnv) {
			t.Fatalf("err = %v; want a refusal naming the reason and the PAT", err)
		}
		if got := milestoneUpdates(t, log); len(got) != 0 {
			t.Errorf("an unknown read offered the hook %v", got)
		}
		if originRef(t, origin, "refs/heads/milestone/v1.8") != before {
			t.Error("origin's milestone branch moved")
		}
	})
}

// TestADOMilestoneCompleteWaitsForOpenChorePR: a chore PR an earlier run left
// open into a protected milestone/v1.8 blocks the integration PR, named by its
// Azure Repos URL.
func TestADOMilestoneCompleteWaitsForOpenChorePR(t *testing.T) {
	adoMsRepo(t, "v1.8")
	asked := stubADOProtection(t, adoProtected)
	stubBranchRules(t, ship.BranchRulesResult{Known: true})
	s := &choreSeams{open: &ship.OpenResult{Number: 9, URL: adoChoreRepoURL + "/pullrequest/9"}}
	stubChoreSeams(t, s)

	_, err := adoMsComplete(t, "v1.8")
	if err == nil || !strings.Contains(err.Error(), adoChoreRepoURL+"/pullrequest/9") || !strings.Contains(err.Error(), "milestone/v1.8") {
		t.Fatalf("err = %v; want a refusal naming .../pullrequest/9 into milestone/v1.8", err)
	}
	if strings.Contains(err.Error(), "/pull/9") {
		t.Errorf("err = %v; a GitHub-shaped PR URL on Azure Repos", err)
	}
	if strings.Contains(err.Error(), "auto-complete is armed") {
		t.Errorf("err = %v; an earlier run's PR is not known to be armed", err)
	}
	if len(s.opened) != 0 {
		t.Errorf("opened %v while a chore PR was open", openedHeads(s))
	}
	if !reflect.DeepEqual(*asked, []string{"milestone/v1.8"}) {
		t.Errorf("branch policies read for %v; want exactly [milestone/v1.8] — the wait's own base", *asked)
	}
}
