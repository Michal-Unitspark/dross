package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/milestone"
)

// writeMilestoneWithBase drops a milestone toml recording `base`, the fact the
// delete gate reads. Status "active" — these are the still-open dependents.
func writeMilestoneWithBase(t *testing.T, dir, version, base string) {
	t.Helper()
	writeMilestoneWithBaseStatus(t, dir, version, base, "active")
}

// writeMilestoneWithBaseStatus is the same record with the status spelled out.
// The milestone prune is being aimed AT has to read "complete": the stale
// detector reports only finished milestones, so a fixture that leaves the
// target active is testing nothing — prune would decline it before ever
// reaching the dependent gate under test.
func writeMilestoneWithBaseStatus(t *testing.T, dir, version, base, status string) {
	t.Helper()
	m := &milestone.Milestone{}
	m.Milestone.Version = version
	m.Milestone.Status = status
	m.Milestone.Base = base
	if err := m.Save(milestone.FilePath(filepath.Join(dir, ".dross"), version)); err != nil {
		t.Fatal(err)
	}
}

// stackedPruneFixture is stackedDeleteFixture aimed at prune: v1.2 is recorded
// COMPLETE. The stale detector only ever names a finished milestone, so a prune
// test whose target is still active would be declined before it ever reached
// the dependent gate it means to exercise. The finalize tests keep the active
// record — finalize is what flips it.
func stackedPruneFixture(t *testing.T) string {
	t.Helper()
	dir := stackedDeleteFixture(t, true)
	writeMilestoneWithBaseStatus(t, dir, "v1.2", "main", milestoneStatusComplete)
	mustGit(t, dir, "add", ".dross")
	mustGit(t, dir, "commit", "-q", "-m", "chore(dross): record v1.2 complete")
	return dir
}

// stackedDeleteFixture builds main <- milestone/v1.2 <- milestone/v1.3, with
// v1.2's work already landed on main and v1.3 still open on top of it. squash
// lands v1.2 the way a forge squash-merge does (so the stale detector names it,
// which is what `prune` acts on); otherwise it lands as a merge commit on
// origin only, leaving local main behind for `--finalize` to fast-forward.
func stackedDeleteFixture(t *testing.T, squash bool) string {
	t.Helper()
	dir := pruneFixture(t)
	// Recorded before the branching and committed on main, so every later
	// checkout carries them and no command meets a dirty tree.
	writeMilestoneWithBase(t, dir, "v1.2", "main")
	writeMilestoneWithBase(t, dir, "v1.3", "milestone/v1.2")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "chore: milestone records")
	mustGit(t, dir, "push", "-q", "origin", "main")

	mustGit(t, dir, "checkout", "-q", "-b", "milestone/v1.2", "main")
	commitOn(t, dir, "milestone/v1.2", "v12.txt", "v12\n", "feat: v1.2 work")
	mustGit(t, dir, "push", "-q", "origin", "milestone/v1.2")

	mustGit(t, dir, "checkout", "-q", "-b", "milestone/v1.3", "milestone/v1.2")
	commitOn(t, dir, "milestone/v1.3", "v13.txt", "v13\n", "feat: v1.3 work")
	mustGit(t, dir, "push", "-q", "origin", "milestone/v1.3")

	if squash {
		squashOnto(t, dir, "milestone/v1.2", "feat(squash): v1.2")
		mustGit(t, dir, "push", "-q", "origin", "main")
	} else {
		mustGit(t, dir, "checkout", "-q", "-b", "mergetmp", "main")
		mustGit(t, dir, "merge", "--no-ff", "-q", "-m", "merge v1.2", "milestone/v1.2")
		mustGit(t, dir, "push", "-q", "origin", "mergetmp:main")
		mustGit(t, dir, "checkout", "-q", "main")
		mustGit(t, dir, "branch", "-D", "mergetmp")
	}

	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "fetch", "-q", "origin")
	return dir
}

// The core protection: a branch an unmerged stacked child still sits on is not
// deleted, however stale it looks on its own.
func TestPruneRefusesBranchWithUnmergedDependent(t *testing.T) {
	dir := stackedPruneFixture(t)

	err := runCmd(t, Milestone(), "prune")
	if err == nil {
		t.Fatal("prune deleted a branch an unmerged milestone is stacked on")
	}
	if !strings.Contains(err.Error(), "v1.3") {
		t.Errorf("the refusal must name the dependent milestone: %v", err)
	}
	if !branchExists(t, dir, "milestone/v1.2") {
		t.Error("local milestone/v1.2 was deleted despite the refusal")
	}
	if !remoteHas(t, dir, "milestone/v1.2") {
		t.Error("origin/milestone/v1.2 was deleted despite the refusal")
	}
}

func TestFinalizeRefusesBranchWithUnmergedDependent(t *testing.T) {
	dir := stackedDeleteFixture(t, false)
	mainBefore := mustGit(t, dir, "rev-parse", "main")

	err := runCmd(t, Milestone(), "complete", "v1.2", "--finalize")
	if err == nil {
		t.Fatal("finalize deleted a branch an unmerged milestone is stacked on")
	}
	if !strings.Contains(err.Error(), "v1.3") {
		t.Errorf("the refusal must name the dependent milestone: %v", err)
	}
	if !branchExists(t, dir, "milestone/v1.2") {
		t.Error("local milestone/v1.2 was deleted despite the refusal")
	}
	if !remoteHas(t, dir, "milestone/v1.2") {
		t.Error("origin/milestone/v1.2 was deleted despite the refusal")
	}
	if got := mustGit(t, dir, "rev-parse", "main"); got != mainBefore {
		t.Error("local main advanced on a refused finalize")
	}
}

// A gate keyed on "a dependent record exists" rather than "an unmerged
// dependent exists" would wedge the repo permanently — a merged child keeps its
// recorded base forever.
func TestPruneDeletesOnceDependentHasMerged(t *testing.T) {
	dir := stackedPruneFixture(t)
	// Land v1.3 on origin/main too.
	mustGit(t, dir, "checkout", "-q", "-b", "mergetmp", "main")
	mustGit(t, dir, "merge", "--no-ff", "-q", "-m", "merge v1.3", "milestone/v1.3")
	mustGit(t, dir, "push", "-q", "origin", "mergetmp:main")
	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "branch", "-D", "mergetmp")
	mustGit(t, dir, "fetch", "-q", "origin")

	if err := runCmd(t, Milestone(), "prune", "--yes"); err != nil {
		t.Fatalf("prune should proceed once the dependent has merged: %v", err)
	}
	if branchExists(t, dir, "milestone/v1.2") {
		t.Error("local milestone/v1.2 should have been pruned")
	}
	if remoteHas(t, dir, "milestone/v1.2") {
		t.Error("origin/milestone/v1.2 should have been pruned")
	}
}

// Every milestone shipped before v1.2 records no base, so it is a dependent of
// nothing — otherwise the gate would block every prune in every repo.
func TestNoRecordedBaseIsNeverADependent(t *testing.T) {
	dir := stackedDeleteFixture(t, true)
	writeMilestoneWithBase(t, dir, "v1.3", "")

	deps, err := dependentMilestones(filepath.Join(dir, ".dross"), dir, "milestone/v1.2", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Errorf("an unrecorded base made %v a dependent", deps)
	}
}

// A milestone naming its own branch as base must not block its own deletion.
func TestSelfBaseDoesNotSelfBlock(t *testing.T) {
	dir := stackedDeleteFixture(t, true)
	writeMilestoneWithBase(t, dir, "v1.3", "milestone/v1.3")

	deps, err := dependentMilestones(filepath.Join(dir, ".dross"), dir, "milestone/v1.3", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Errorf("a self-recorded base blocked its own branch: %v", deps)
	}
}

// Fail closed. An unreadable toml is indistinguishable from "no dependents",
// and the consequence of guessing is an irreversible remote delete.
func TestPruneRefusesOnUnreadableMilestoneToml(t *testing.T) {
	dir := stackedPruneFixture(t)
	broken := filepath.Join(dir, ".dross", "milestones", "v1.4.toml")
	if err := os.WriteFile(broken, []byte("[milestone\n  version = ***\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCmd(t, Milestone(), "prune")
	if err == nil {
		t.Fatal("prune deleted on a short scan; an unreadable record must fail closed")
	}
	if !strings.Contains(err.Error(), "v1.4") {
		t.Errorf("the error should name the unreadable record: %v", err)
	}
	if !branchExists(t, dir, "milestone/v1.2") {
		t.Error("local milestone/v1.2 was deleted despite the failed scan")
	}
}

// mergedIntoParentFixture: milestone/v1.2 is stacked on milestone/v1.1 and has
// merged into it on origin — but main has NOT advanced. This is the stacking
// model's second half, and a finalize guard fixed on origin/main refuses it
// forever.
func mergedIntoParentFixture(t *testing.T) string {
	t.Helper()
	dir := pruneFixture(t)
	writeMilestoneWithBase(t, dir, "v1.2", "milestone/v1.1")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "chore: milestone records")
	mustGit(t, dir, "push", "-q", "origin", "main")

	mustGit(t, dir, "checkout", "-q", "-b", "milestone/v1.1", "main")
	commitOn(t, dir, "milestone/v1.1", "v11.txt", "v11\n", "feat: v1.1 work")
	mustGit(t, dir, "push", "-q", "origin", "milestone/v1.1")

	mustGit(t, dir, "checkout", "-q", "-b", "milestone/v1.2", "milestone/v1.1")
	commitOn(t, dir, "milestone/v1.2", "v12.txt", "v12\n", "feat: v1.2 work")
	mustGit(t, dir, "push", "-q", "origin", "milestone/v1.2")

	// v1.2 -> v1.1 merge, on origin and locally: the parent received it.
	mustGit(t, dir, "checkout", "-q", "milestone/v1.1")
	mustGit(t, dir, "merge", "--no-ff", "-q", "-m", "merge v1.2 into v1.1", "milestone/v1.2")
	mustGit(t, dir, "push", "-q", "origin", "milestone/v1.1")

	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "fetch", "-q", "origin")
	return dir
}

// threeDependentsFixture stacks three sibling milestones (v1.3, v1.4, v1.5)
// directly on milestone/v1.2 — each on its own branch, so none reads as
// "vanished" — then squash-merges v1.2 onto main the way stackedDeleteFixture
// does, so the stale detector names v1.2 and prune attempts to delete it.
// Three simultaneous dependents exercise joinVersions' first, middle and last
// branches in one refusal, which a single- or double-dependent fixture cannot.
func threeDependentsFixture(t *testing.T) string {
	t.Helper()
	dir := pruneFixture(t)
	writeMilestoneWithBaseStatus(t, dir, "v1.2", "main", milestoneStatusComplete)
	writeMilestoneWithBase(t, dir, "v1.3", "milestone/v1.2")
	writeMilestoneWithBase(t, dir, "v1.4", "milestone/v1.2")
	writeMilestoneWithBase(t, dir, "v1.5", "milestone/v1.2")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "chore: milestone records")
	mustGit(t, dir, "push", "-q", "origin", "main")

	mustGit(t, dir, "checkout", "-q", "-b", "milestone/v1.2", "main")
	commitOn(t, dir, "milestone/v1.2", "v12.txt", "v12\n", "feat: v1.2 work")
	mustGit(t, dir, "push", "-q", "origin", "milestone/v1.2")

	for _, v := range []string{"v1.3", "v1.4", "v1.5"} {
		branch := "milestone/" + v
		mustGit(t, dir, "checkout", "-q", "-b", branch, "milestone/v1.2")
		commitOn(t, dir, branch, v+".txt", v+"\n", "feat: "+v+" work")
		mustGit(t, dir, "push", "-q", "origin", branch)
	}

	squashOnto(t, dir, "milestone/v1.2", "feat(squash): v1.2")
	mustGit(t, dir, "push", "-q", "origin", "main")

	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "fetch", "-q", "origin")
	return dir
}

// A single- or double-dependent fixture never reaches joinVersions' middle
// (i != 0 && i != len(vs)-1) branch — only three or more simultaneous
// dependents do.
func TestPruneRefusesNamingAllStackedDependents(t *testing.T) {
	dir := threeDependentsFixture(t)

	err := runCmd(t, Milestone(), "prune")
	if err == nil {
		t.Fatal("prune deleted a branch with three unmerged milestones stacked on it")
	}
	if !strings.Contains(err.Error(), "v1.3, v1.4 and v1.5") {
		t.Errorf("the refusal must name all three dependents, comma-then-and joined: %v", err)
	}
	if !branchExists(t, dir, "milestone/v1.2") {
		t.Error("local milestone/v1.2 was deleted despite the refusal")
	}
	if !remoteHas(t, dir, "milestone/v1.2") {
		t.Error("origin/milestone/v1.2 was deleted despite the refusal")
	}
}

func TestFinalizeAcceptsMergeIntoRecordedParent(t *testing.T) {
	dir := mergedIntoParentFixture(t)
	mainBefore := mustGit(t, dir, "rev-parse", "main")

	out := captureStdout(t, func() {
		if err := runCmd(t, Milestone(), "complete", "v1.2", "--finalize"); err != nil {
			t.Fatalf("a guard fixed on origin/main refuses this forever: %v", err)
		}
	})

	if branchExists(t, dir, "milestone/v1.2") {
		t.Error("local milestone/v1.2 should have been deleted")
	}
	if remoteHas(t, dir, "milestone/v1.2") {
		t.Error("origin/milestone/v1.2 should have been deleted")
	}
	// main did not receive this merge, so it must not have moved.
	if got := mustGit(t, dir, "rev-parse", "main"); got != mainBefore {
		t.Errorf("local main advanced to %s; the merge landed on the parent, not main", got)
	}
	if !strings.Contains(out, "milestone/v1.1") {
		t.Errorf("the finalize line must name the branch that received the merge:\n%s", out)
	}
	if strings.Contains(out, "main is at origin") {
		t.Errorf("the output claims main advanced when it did not:\n%s", out)
	}
}

// adoDependentPR is an active PR as ADO's list returns it.
func adoDependentPR(id int, target string) string {
	return fmt.Sprintf(`{"pullRequestId":%d,"title":"PR %d","status":"active","sourceRefName":"refs/heads/phase/p%d","targetRefName":"refs/heads/%s"}`, id, id, id, target)
}

// TestGuardOpenPRsTargetingAzureDevOps drives the dependents check against an
// Azure DevOps Server collection: an active PR into the branch refuses the
// delete naming it and its browser URL, even on a full page; a failed lookup
// degrades to the announced skip; an api_base off the host allowlist is the
// policy's refusal, never the skip, and reaches nothing.
func TestGuardOpenPRsTargetingAzureDevOps(t *testing.T) {
	var hits atomic.Int32
	var code atomic.Int32
	var list atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != "GET" || !strings.HasSuffix(r.URL.EscapedPath(), "/coll/P/_apis/git/repositories/R/pullrequests") ||
			r.URL.Query().Get("searchCriteria.targetRefName") != "refs/heads/milestone/v1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(int(code.Load()))
		_, _ = w.Write([]byte(list.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	serve := func(status int, prs ...string) {
		code.Store(int32(status))
		list.Store(`{"value":[` + strings.Join(prs, ",") + `],"count":` + fmt.Sprint(len(prs)) + `}`)
		hits.Store(0)
	}

	prRepo(t)
	t.Setenv("MOCK_ADO_PAT", adoShipPAT)
	setRemote := func(apiBase string) {
		t.Helper()
		for _, set := range [][]string{
			{"set", "remote.provider", "azure-devops"},
			{"set", "remote.url", srv.URL + "/coll/P/_git/R"},
			{"set", "remote.api_base", apiBase},
			{"set", "remote.auth_env", "MOCK_ADO_PAT"},
		} {
			if err := runCmd(t, Project(), set...); err != nil {
				t.Fatalf("project %v: %v", set, err)
			}
		}
	}
	setRemote(srv.URL + "/coll")
	// The remote's browser URL is canonicalised to https.
	browser := strings.Replace(srv.URL, "http://", "https://", 1) + "/coll/P/_git/R/pullrequest/5"

	serve(200, adoDependentPR(5, "milestone/v1"))
	var out string
	err := captureInto(t, &out, func() error { return guardOpenPRsTargeting("milestone/v1") })
	if err == nil || !strings.Contains(err.Error(), "#5") || !strings.Contains(err.Error(), browser) {
		t.Errorf("an active dependent: got %v, want a refusal naming #5 and %s", err, browser)
	}

	full := make([]string, 100)
	for i := range full {
		full[i] = adoDependentPR(100+i, "main")
	}
	full[57] = adoDependentPR(5, "milestone/v1")
	serve(200, full...)
	err = captureInto(t, &out, func() error { return guardOpenPRsTargeting("milestone/v1") })
	if err == nil || !strings.Contains(err.Error(), "#5") {
		t.Errorf("a full page holding the dependent: got %v, want a refusal naming #5", err)
	}

	serve(500)
	list.Store(`{"message":"boom"}`)
	err = captureInto(t, &out, func() error { return guardOpenPRsTargeting("milestone/v1") })
	if err != nil || !strings.Contains(out, "open-PR check skipped") {
		t.Errorf("a 500: got %v and\n%s\nwant the announced skip", err, out)
	}

	// Same server, named by another host: not on the derived allowlist.
	setRemote(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/coll")
	serve(200, adoDependentPR(5, "milestone/v1"))
	err = captureInto(t, &out, func() error { return guardOpenPRsTargeting("milestone/v1") })
	if !errors.Is(err, hostallow.ErrRefused) {
		t.Errorf("an off-allowlist api_base: got %v, want ErrRefused", err)
	}
	if strings.Contains(out, "open-PR check skipped") {
		t.Errorf("a host refusal was announced as a skip:\n%s", out)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the off-allowlist api_base received %d request(s)", n)
	}
}
