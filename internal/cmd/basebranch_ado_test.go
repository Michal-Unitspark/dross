package cmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/ship"
)

// The base safety net on an azure-devops remote (azure-boards-backend c-10):
// the same pre-receive hook as basebranch_chores_test.go stands in for Azure
// Repos refusing a push to a branch with a blocking policy (TF402455), and
// the branch-policy read is the ship.ADOBranchProtectionFunc seam.

const (
	adoChoreRepoURL = "https://dev.azure.com/o/p/_git/r"
	adoChoreEnv     = "DROSS_ADO_CHORE_PAT"
)

// adoChoreBase is a dross repo on an azure-devops remote with main pushed and
// one .dross-only chore committed on top.
func adoChoreBase(t *testing.T) (dir, origin string) {
	t.Helper()
	dir, origin = setupMilestoneRepo(t)
	path := filepath.Join(dir, ".dross", project.File)
	p, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Remote = project.Remote{
		Provider: "azure-devops",
		URL:      adoChoreRepoURL,
		APIBase:  "https://dev.azure.com/o",
		AuthEnv:  adoChoreEnv,
	}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "commit", "-q", "-am", "remote")
	mustGit(t, dir, "push", "-q", "-u", "origin", "main")
	choreCommit(t, dir, "handoff.md")
	return dir, origin
}

// stubADOProtection answers every branch-policy read with res, recording the
// branches asked about.
func stubADOProtection(t *testing.T, res ship.ADOProtection) *[]string {
	t.Helper()
	var asked []string
	prev := ship.ADOBranchProtectionFunc
	ship.ADOBranchProtectionFunc = func(_ ship.OpenOpts, branch string) ship.ADOProtection {
		asked = append(asked, branch)
		return res
	}
	t.Cleanup(func() { ship.ADOBranchProtectionFunc = prev })
	return &asked
}

var adoProtected = ship.ADOProtection{Known: true, Protected: true, Policies: []string{"Minimum number of reviewers #12"}}

// TestProtectedADOBaseRoutesThroughChorePR: a protected ADO base — main or a
// milestone branch — reaches origin through a chore PR armed with a merge
// commit, and the hook is never offered the base.
func TestProtectedADOBaseRoutesThroughChorePR(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		dir, origin := adoChoreBase(t)
		log := protectedOrigin(t, origin, false)
		asked := stubADOProtection(t, adoProtected)
		rules := stubBranchRules(t, ship.BranchRulesResult{Known: true})
		s := &choreSeams{autoRes: ship.AutoMergeResult{AutoEnabled: true}}
		stubChoreSeams(t, s)

		r, err := routeBaseChores(dir, "main")
		if err != nil {
			t.Fatal(err)
		}
		if r.Pushed || r.ChorePR == nil || len(s.opened) != 1 {
			t.Fatalf("result %+v, %d PRs opened; want one chore PR", r, len(s.opened))
		}
		if got := originRef(t, origin, "refs/heads/dross-chores/main"); got != mustGit(t, dir, "rev-parse", "main") {
			t.Errorf("dross-chores/main = %q, want local main's tip", got)
		}
		if got := baseUpdates(t, log); len(got) != 0 {
			t.Errorf("the hook was offered %v", got)
		}
		if !reflect.DeepEqual(s.armed, []string{"merge"}) {
			t.Errorf("armed with %v, want only [merge]", s.armed)
		}
		if !reflect.DeepEqual(*asked, []string{"main"}) || *rules != 0 {
			t.Errorf("ADO policy reads %v and GitHub rule reads %d; want [main] and 0", *asked, *rules)
		}
		if o := s.opened[0]; o.HeadBranch != "dross-chores/main" || o.BaseBranch != "main" {
			t.Errorf("opened %s → %s, want dross-chores/main → main", o.HeadBranch, o.BaseBranch)
		}
		if !strings.Contains(s.opened[0].Body, "Auto-complete is armed") {
			t.Errorf("chore PR body does not say auto-complete: %q", s.opened[0].Body)
		}
	})

	t.Run("milestone branch", func(t *testing.T) {
		dir, origin := adoChoreBase(t)
		mustGit(t, dir, "branch", "-q", "milestone/v1.8", "main~1")
		mustGit(t, dir, "push", "-q", "origin", "milestone/v1.8")
		mustGit(t, dir, "switch", "-q", "milestone/v1.8")
		choreCommit(t, dir, "phase-chore.md")
		log := protectedOrigin(t, origin, false)
		asked := stubADOProtection(t, adoProtected)
		s := &choreSeams{autoRes: ship.AutoMergeResult{AutoEnabled: true}}
		stubChoreSeams(t, s)

		r, err := routeBaseChores(dir, "milestone/v1.8")
		if err != nil {
			t.Fatal(err)
		}
		if r.ChorePR == nil || len(s.opened) != 1 {
			t.Fatalf("result %+v; want one chore PR", r)
		}
		if o := s.opened[0]; o.HeadBranch != "dross-chores/milestone-v1.8" || o.BaseBranch != "milestone/v1.8" {
			t.Errorf("opened %s → %s, want dross-chores/milestone-v1.8 → milestone/v1.8", o.HeadBranch, o.BaseBranch)
		}
		if !reflect.DeepEqual(*asked, []string{"milestone/v1.8"}) {
			t.Errorf("branch-policy reads %v, want [milestone/v1.8] — the milestone base's own policies", *asked)
		}
		if originRef(t, origin, "refs/heads/dross-chores/milestone-v1.8") == "" {
			t.Error("dross-chores/milestone-v1.8 is not on origin")
		}
		for _, ref := range baseUpdates(t, log) {
			if strings.HasPrefix(ref, "refs/heads/milestone/") {
				t.Errorf("the hook was offered %s", ref)
			}
		}
	})
}

// TestUnprotectedADOBasePushesDirectly: no blocking policy keeps the direct
// push — no chore branch, no PR.
func TestUnprotectedADOBasePushesDirectly(t *testing.T) {
	dir, origin := adoChoreBase(t)
	asked := stubADOProtection(t, ship.ADOProtection{Known: true})
	s := &choreSeams{}
	stubChoreSeams(t, s)

	r, err := routeBaseChores(dir, "main")
	if err != nil || !r.Pushed || r.ChorePR != nil {
		t.Fatalf("result %+v, %v; want a direct push", r, err)
	}
	if got, want := originRef(t, origin, "refs/heads/main"), mustGit(t, dir, "rev-parse", "main"); got != want {
		t.Errorf("origin main = %q, want local main %q", got, want)
	}
	if originRef(t, origin, "refs/heads/dross-chores/main") != "" || len(s.opened) != 0 || s.lookups != 0 {
		t.Errorf("an unprotected base still reached a chore branch or PR (opened %d, lookups %d)", len(s.opened), s.lookups)
	}
	if len(*asked) != 1 {
		t.Errorf("%d branch-policy reads, want 1", len(*asked))
	}
}

// TestUnknownADOProtectionNeverPushes: an unreadable policy list pushes
// nothing — not even to an origin that would take it — and the fix names the
// PAT in [remote].auth_env, never gh.
func TestUnknownADOProtectionNeverPushes(t *testing.T) {
	dir, origin := adoChoreBase(t)
	log := protectedOrigin(t, origin, true)
	stubADOProtection(t, ship.ADOProtection{Reason: "read branch policies: HTTP 401: sign-in required"})
	s := &choreSeams{}
	stubChoreSeams(t, s)

	r, err := routeBaseChores(dir, "main")
	if err == nil {
		t.Fatal("an unknown policy read did not refuse")
	}
	if r.Pushed || r.ChorePR != nil || len(s.opened) != 0 {
		t.Errorf("result %+v after an unknown read", r)
	}
	msg := err.Error()
	for _, want := range []string{"HTTP 401: sign-in required", "[remote].auth_env", "$" + adoChoreEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("err %q does not name %q", msg, want)
		}
	}
	if strings.Contains(msg, "gh auth login") {
		t.Errorf("an azure-devops refusal tells the user to log in to gh: %q", msg)
	}
	if got := baseUpdates(t, log); len(got) != 0 {
		t.Errorf("an unknown read offered the hook %v", got)
	}
}

// TestProtectedADOBaseCodeAheadRefuses: code ahead on a protected ADO base is
// refused naming the file, and no chore PR carries it.
func TestProtectedADOBaseCodeAheadRefuses(t *testing.T) {
	dir, origin := adoChoreBase(t)
	mustWrite(t, filepath.Join(dir, "src.go"), "package src\n")
	mustGit(t, dir, "add", "src.go")
	mustGit(t, dir, "commit", "-q", "-m", "feat: code on main")
	log := protectedOrigin(t, origin, false)
	stubADOProtection(t, adoProtected)
	s := &choreSeams{}
	stubChoreSeams(t, s)

	_, err := routeBaseChores(dir, "main")
	if err == nil || !strings.Contains(err.Error(), "src.go") {
		t.Fatalf("code ahead on a protected ADO base: %v; want a refusal naming src.go", err)
	}
	if len(s.opened) != 0 || originRef(t, origin, "refs/heads/dross-chores/main") != "" {
		t.Error("code ahead still reached a chore branch or PR")
	}
	if got := baseUpdates(t, log); len(got) != 0 {
		t.Errorf("the hook was offered %v", got)
	}
}

// TestChorePRNarrationADO: an ADO chore PR narrates auto-complete, or says to
// complete it by hand at its Azure Repos URL — never a GitHub /pull/ link.
func TestChorePRNarrationADO(t *testing.T) {
	const prURL = adoChoreRepoURL + "/pullrequest/41"
	for _, tc := range []struct {
		name    string
		autoRes ship.AutoMergeResult
		autoErr error
		openURL string // what the open answers; "" falls back to pullURL
		want    string
	}{
		{"unavailable", ship.AutoMergeResult{}, ship.ErrAutoMergeUnavailable, prURL, "complete it by hand"},
		{"unavailable, URL built from config", ship.AutoMergeResult{}, ship.ErrAutoMergeUnavailable, "", "complete it by hand"},
		{"armed", ship.AutoMergeResult{AutoEnabled: true}, nil, prURL, "auto-complete armed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, origin := adoChoreBase(t)
			protectedOrigin(t, origin, false)
			stubADOProtection(t, adoProtected)
			s := &choreSeams{autoRes: tc.autoRes, autoErr: tc.autoErr}
			stubChoreSeams(t, s)
			prev := ship.OpenPRFunc
			ship.OpenPRFunc = func(o ship.OpenOpts) (*ship.OpenResult, error) {
				s.opened = append(s.opened, o)
				return &ship.OpenResult{Number: 41, URL: tc.openURL}, nil
			}
			t.Cleanup(func() { ship.OpenPRFunc = prev })

			r, err := routeBaseChores(dir, "main")
			if err != nil || r.ChorePR == nil {
				t.Fatalf("result %+v, %v; want a chore PR", r, err)
			}
			line := r.ChorePR.narrate()
			if !strings.Contains(line, tc.want) || !strings.Contains(line, prURL) {
				t.Errorf("narration %q does not say %q at %s", line, tc.want, prURL)
			}
			if strings.Contains(line, "/pull/41") || strings.Contains(line, "auto-merge") {
				t.Errorf("narration is GitHub-shaped: %q", line)
			}
		})
	}
}

// TestPullURLByForge: Azure Repos gets <repo>/pullrequest/<n>; GitHub keeps
// <repo>/pull/<n>.
func TestPullURLByForge(t *testing.T) {
	for repo, want := range map[string]string{
		"https://dev.azure.com/o/p/_git/r":       "https://dev.azure.com/o/p/_git/r/pullrequest/41",
		"https://dev.azure.com/o/p/_git/r/":      "https://dev.azure.com/o/p/_git/r/pullrequest/41",
		"https://o@dev.azure.com/o/p/_git/r":     "https://dev.azure.com/o/p/_git/r/pullrequest/41",
		"https://tfs.corp/tfs/Coll/P/_git/R":     "https://tfs.corp/tfs/Coll/P/_git/R/pullrequest/41",
		"https://acme.visualstudio.com/P/_git/R": "https://acme.visualstudio.com/P/_git/R/pullrequest/41",
		"https://github.com/o/r":                 "https://github.com/o/r/pull/41",
		"https://github.com/o/r.git":             "https://github.com/o/r/pull/41",
		"https://github.com/o/r/":                "https://github.com/o/r/pull/41",
	} {
		if got := pullURL(repo, 41); got != want {
			t.Errorf("pullURL(%q, 41) = %q, want %q", repo, got, want)
		}
	}
}
