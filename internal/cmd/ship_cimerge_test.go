package cmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/changes"
	"github.com/Rivil/dross/internal/ship"
)

// --- dross ship ci / dross ship merge (azure-devops-ship-loop c-5, c-6, c-10, c-11) ---

// ciMergeRepo is shipFixture with phase/x pushed to a bare origin and PR #7
// recorded in changes.json; a commit made after the push leaves HEAD ahead of
// origin, so the watched head is provably origin's, not the working copy's.
// Returns the repo, its origin, and the watched sha.
func ciMergeRepo(t *testing.T) (dir, remoteDir, sha string) {
	t.Helper()
	dir = shipFixture(t, "https://forge.example/me/p.git")
	remoteDir = t.TempDir()
	mustGit(t, remoteDir, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "set-url", "origin", remoteDir)
	mustGit(t, dir, "push", "-q", "origin", "phase/x")
	if err := changes.SetPR(filepath.Join(dir, ".dross"), "x", 7); err != nil {
		t.Fatal(err)
	}
	gitCommit(t, dir, "chore(dross): record PR #7")
	sha = strings.TrimSpace(gitOutT(t, dir, "rev-parse", "refs/remotes/origin/phase/x"))
	if head := strings.TrimSpace(gitOutT(t, dir, "rev-parse", "HEAD")); head == sha {
		t.Fatal("fixture: HEAD should be ahead of origin/phase/x")
	}
	return dir, remoteDir, sha
}

// seamCall is one call a stubbed CI or merge seam saw.
type seamCall struct {
	pr  int
	sha string
}

func stubCIStatus(t *testing.T, res ship.CIResult, err error) *[]seamCall {
	t.Helper()
	var calls []seamCall
	prev := ship.CIStatusFunc
	ship.CIStatusFunc = func(o ship.OpenOpts, sha string) (ship.CIResult, error) {
		calls = append(calls, seamCall{o.PRNumber, sha})
		return res, err
	}
	t.Cleanup(func() { ship.CIStatusFunc = prev })
	return &calls
}

func stubMergePR(t *testing.T, res ship.MergeResult, err error) *[]seamCall {
	t.Helper()
	var calls []seamCall
	prev := ship.MergePRFunc
	ship.MergePRFunc = func(o ship.OpenOpts, sha string) (ship.MergeResult, error) {
		calls = append(calls, seamCall{o.PRNumber, sha})
		return res, err
	}
	t.Cleanup(func() { ship.MergePRFunc = prev })
	return &calls
}

// runShipVerb runs `dross ship <args...>`, returning stdout and the error.
func runShipVerb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runCmd(t, Ship(), args...) })
	return out, err
}

// TestShipCIExitCodes: each verdict has its own exit status and `ci:` line; a
// read that fails is exit 1 and prints no verdict at all.
func TestShipCIExitCodes(t *testing.T) {
	ciMergeRepo(t)
	link := "https://ci.example/build/9"
	for _, tc := range []struct {
		res  ship.CIResult
		code int
		line string
	}{
		{ship.CIResult{Verdict: ship.CIPass}, 0, "ci: pass\n"},
		{ship.CIResult{Verdict: ship.CIFail, Link: link}, 2, "ci: fail " + link + "\n"},
		{ship.CIResult{Verdict: ship.CIPending, Queued: true}, 3, "ci: pending (queued)\n"},
		{ship.CIResult{Verdict: ship.CIPending}, 3, "ci: pending (running)\n"},
		{ship.CIResult{Verdict: ship.CINone}, 4, "ci: none\n"},
		{ship.CIResult{Verdict: ship.CIAsk, Reason: "a build policy expired"}, 5, "ci: ask - a build policy expired\n"},
	} {
		stubCIStatus(t, tc.res, nil)
		out, err := runShipVerb(t, "ci", "x")
		if got := ExitCode(err); got != tc.code {
			t.Errorf("%+v: exit %d (%v), want %d", tc.res, got, err, tc.code)
		}
		if out != tc.line {
			t.Errorf("%+v: printed %q, want %q", tc.res, out, tc.line)
		}
	}

	for _, fault := range []error{
		errors.New("read PR #7: HTTP 500"),
		errors.New("read PR #7: HTTP 203 — a sign-in page, not the API"),
	} {
		stubCIStatus(t, ship.CIResult{Verdict: ship.CIPass}, fault)
		out, err := runShipVerb(t, "ci", "x")
		if got := ExitCode(err); got != 1 || err == nil || !strings.Contains(err.Error(), fault.Error()) {
			t.Errorf("a failed read (%v): exit %d, err %v; want exit 1 carrying it", fault, got, err)
		}
		if strings.Contains(out, "ci:") {
			t.Errorf("a failed read printed a verdict: %q", out)
		}
	}

	stubCIStatus(t, ship.CIResult{Verdict: "green"}, nil)
	out, err := runShipVerb(t, "ci", "x")
	if ExitCode(err) != 1 || strings.Contains(out, "ci:") {
		t.Errorf("an unknown verdict: exit %d, printed %q; want exit 1 and no `ci:` line", ExitCode(err), out)
	}
}

// TestShipCIPrintsReason: a reason rides on the verdict's line; a verdict
// without one has no trailing separator. A reasoned pending is not about CI,
// so it claims neither queued nor running.
func TestShipCIPrintsReason(t *testing.T) {
	ciMergeRepo(t)
	reason := "PR head bbb is not watched aaa - run dross ship x"
	stubCIStatus(t, ship.CIResult{Verdict: ship.CIPending, Reason: reason}, nil)
	out, _ := runShipVerb(t, "ci", "x")
	if out != "ci: pending - "+reason+"\n" {
		t.Errorf("a reasoned pending printed %q", out)
	}

	stubCIStatus(t, ship.CIResult{Verdict: ship.CIAsk, Reason: "an unknown check state"}, nil)
	if out, _ = runShipVerb(t, "ci", "x"); out != "ci: ask - an unknown check state\n" {
		t.Errorf("an ask printed %q", out)
	}

	for _, v := range []ship.CIVerdict{ship.CIPass, ship.CINone, ship.CIFail} {
		stubCIStatus(t, ship.CIResult{Verdict: v}, nil)
		out, _ = runShipVerb(t, "ci", "x")
		if strings.Contains(out, " - ") || strings.HasSuffix(strings.TrimRight(out, "\n"), " ") {
			t.Errorf("%s without a reason printed a trailing separator: %q", v, out)
		}
	}
}

// TestShipCIMergeWatchedHead: both verbs hand the seam PR #7 at the commit
// origin's phase/x names — not HEAD. A missing origin ref, an invalid id and
// an unrecorded PR each refuse before the seam is called.
func TestShipCIMergeWatchedHead(t *testing.T) {
	dir, _, sha := ciMergeRepo(t)
	ci := stubCIStatus(t, ship.CIResult{Verdict: ship.CIPass}, nil)
	merge := stubMergePR(t, ship.MergeResult{URL: "https://forge.example/pr/7"}, nil)
	if _, err := runShipVerb(t, "ci", "x"); err != nil {
		t.Fatalf("ship ci: %v", err)
	}
	if _, err := runShipVerb(t, "merge", "x"); err != nil {
		t.Fatalf("ship merge: %v", err)
	}
	want := []seamCall{{7, sha}}
	if fmt.Sprint(*ci) != fmt.Sprint(want) || fmt.Sprint(*merge) != fmt.Sprint(want) {
		t.Errorf("ci saw %v, merge saw %v; want %v", *ci, *merge, want)
	}

	*ci, *merge = nil, nil
	// Refused as an id — not merely for lacking a recorded PR further on.
	for _, id := range []string{"-x", "../x", "x/y", " x", ".."} {
		for _, verb := range []string{"ci", "merge"} {
			_, err := runShipVerb(t, verb, "--", id)
			if err == nil || !(strings.Contains(err.Error(), "invalid phase id") || strings.Contains(err.Error(), "unsafe git ref")) {
				t.Errorf("ship %s %q: %v, want a refusal of the id itself", verb, id, err)
			}
		}
	}

	mustGit(t, dir, "update-ref", "-d", "refs/remotes/origin/phase/x")
	for _, verb := range []string{"ci", "merge"} {
		if _, err := runShipVerb(t, verb, "x"); err == nil || !strings.Contains(err.Error(), "refs/remotes/origin/phase/x") {
			t.Errorf("ship %s with no origin ref: %v, want a refusal naming it", verb, err)
		}
	}

	mustGit(t, dir, "update-ref", "refs/remotes/origin/phase/x", sha)
	if err := changes.SetPR(filepath.Join(dir, ".dross"), "x", 0); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"ci", "merge"} {
		if _, err := runShipVerb(t, verb, "x"); err == nil || !strings.Contains(err.Error(), "dross ship x") {
			t.Errorf("ship %s with pr 0: %v, want a refusal naming `dross ship x`", verb, err)
		}
	}
	if len(*ci) != 0 || len(*merge) != 0 {
		t.Errorf("a refused run reached the seam: ci %v, merge %v", *ci, *merge)
	}
}

// TestCheckWatchedSHA: only a full 40-hex id passes, lower-cased; anything
// else git could print is refused.
func TestCheckWatchedSHA(t *testing.T) {
	good := strings.Repeat("aB", 20)
	if got, err := checkWatchedSHA("r", good); err != nil || got != strings.ToLower(good) {
		t.Errorf("%q: got %q, %v", good, got, err)
	}
	for _, bad := range []string{
		"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 64),
		strings.Repeat("g", 40), "--output=/tmp/x" + strings.Repeat("a", 25), strings.Repeat("a", 39) + "\n",
	} {
		if _, err := checkWatchedSHA("r", bad); err == nil {
			t.Errorf("%q was admitted", bad)
		}
	}
}

// TestShipMergeExitCodes: merged exits 0 with the URL; a policy block exits 2
// naming awaiting-merge; anything else exits 1 and never says merged. On
// github neither verb sends a request — its ship.md recipe owns both steps.
func TestShipMergeExitCodes(t *testing.T) {
	ciMergeRepo(t)
	stubMergePR(t, ship.MergeResult{URL: "https://forge.example/pr/7"}, nil)
	out, err := runShipVerb(t, "merge", "x")
	if err != nil || !strings.Contains(out, "https://forge.example/pr/7") {
		t.Errorf("a merge: %v, printed %q", err, out)
	}

	stubMergePR(t, ship.MergeResult{}, fmt.Errorf("complete PR #7: %w — Required reviewers not approved", ship.ErrMergeBlocked))
	out, err = runShipVerb(t, "merge", "x")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "awaiting-merge") || strings.Contains(out, "merged") {
		t.Errorf("a policy block: exit %d, %v, printed %q; want exit 2 naming awaiting-merge", ExitCode(err), err, out)
	}

	for _, fault := range []error{
		errors.New("complete PR #7: the merge has conflicts"),
		errors.New("complete PR #7: completion not confirmed"),
		ship.ErrMergeUnsupported,
	} {
		stubMergePR(t, ship.MergeResult{URL: "https://forge.example/pr/7"}, fault)
		out, err = runShipVerb(t, "merge", "x")
		if ExitCode(err) != 1 || strings.Contains(out, "merged") {
			t.Errorf("%v: exit %d, printed %q; want exit 1 and never merged", fault, ExitCode(err), out)
		}
	}
}

func TestShipCIMergeGitHubUsesRecipe(t *testing.T) {
	ciMergeRepo(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	mustRunSet(t, "remote.provider", "github")
	mustRunSet(t, "remote.url", "https://github.com/me/p")
	mustRunSet(t, "remote.api_base", srv.URL)
	for _, verb := range []string{"ci", "merge"} {
		out, err := runShipVerb(t, verb, "x")
		if ExitCode(err) == 0 || !strings.Contains(err.Error(), "ship.md recipe") {
			t.Errorf("ship %s on github: exit %d, %v; want non-zero naming the ship.md recipe", verb, ExitCode(err), err)
		}
		if strings.Contains(out, "ci:") || strings.Contains(out, "merged") {
			t.Errorf("ship %s on github printed %q", verb, out)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d HTTP request(s) on github", n)
	}
}

// --- against a mock Azure DevOps Server collection ---

const (
	adoCMRepoGUID    = "0d6b3d4e-1111-4a2b-8c3d-4e5f6a7b8c9d"
	adoCMProjectGUID = "7f1e2d3c-2222-4b5a-9c8d-7e6f5a4b3c2d"
	adoCMBuildPolicy = "0609b952-1397-4640-95ec-e00a01b2c241"
)

// adoCIMergeForge is PR #7 on a Server collection: its builds, its policy
// evaluations, and a completion PATCH that turns the PR completed. Every
// request is recorded with its query, which newADOShipMock does not keep —
// the builds query is what proves the branch filter — and the PATCH needs
// state that a fixed route table cannot hold.
type adoCIMergeForge struct {
	*httptest.Server
	mu        sync.Mutex
	sha       string
	status    string
	builds    string
	evals     string
	reqs      []string
	patchBody string
}

func newADOCIMergeForge(t *testing.T, sha, builds, evals string) *adoCIMergeForge {
	t.Helper()
	f := &adoCIMergeForge{sha: sha, status: "active", builds: builds, evals: evals}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI())
		p := strings.ToLower(r.URL.EscapedPath())
		switch {
		case r.Method == "GET" && strings.HasSuffix(p, "/coll/p/_apis/git/repositories/r/pullrequests/7"):
			_, _ = fmt.Fprintf(w, `{"pullRequestId":7,"status":%q,"isDraft":false,"mergeStatus":"succeeded","sourceRefName":"refs/heads/phase/x","targetRefName":"refs/heads/main","lastMergeSourceCommit":{"commitId":%q},"repository":{"id":%q,"project":{"id":%q}}}`,
				f.status, f.sha, adoCMRepoGUID, adoCMProjectGUID)
		case r.Method == "PATCH" && strings.HasSuffix(p, "/coll/p/_apis/git/repositories/r/pullrequests/7"):
			b, _ := io.ReadAll(r.Body)
			f.patchBody = string(b)
			f.status = "completed"
			_, _ = w.Write([]byte(`{"pullRequestId":7,"status":"active"}`))
		case r.Method == "GET" && strings.HasSuffix(p, "/coll/p/_apis/build/builds"):
			_, _ = w.Write([]byte(f.builds))
		case r.Method == "GET" && strings.HasSuffix(p, "/coll/p/_apis/policy/evaluations"):
			_, _ = w.Write([]byte(f.evals))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *adoCIMergeForge) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// adoCMRemote points [remote] at an Azure DevOps Server collection on base
// through `dross project set`, as a user would.
func adoCMRemote(t *testing.T, base string) {
	t.Helper()
	t.Setenv("MOCK_ADO_PAT", adoShipPAT)
	mustRunSet(t, "remote.provider", "azure-devops")
	mustRunSet(t, "remote.url", base+"/coll/P/_git/R")
	mustRunSet(t, "remote.api_base", base+"/coll")
	mustRunSet(t, "remote.api_version", "6.0")
	mustRunSet(t, "remote.auth_env", "MOCK_ADO_PAT")
}

func adoCMBuilds(sha string) string {
	return fmt.Sprintf(`{"count":1,"value":[{"status":"completed","result":"succeeded","queueTime":"2026-10-30T10:00:00Z","sourceVersion":%q,"definition":{"id":1},"repository":{"id":%q},"_links":{"web":{"href":"https://ci.example/build/1"}}}]}`,
		sha, adoCMRepoGUID)
}

var adoCMApproved = `{"count":1,"value":[{"status":"approved","configuration":{"isBlocking":true,"type":{"id":"` + adoCMBuildPolicy + `","displayName":"Build"}},"context":{"buildId":1}}]}`

// TestShipCIAzureDevOpsFlow: in a repo with no azure-pipelines.yml, a build
// that succeeded at the watched head plus an approved Build evaluation is a
// pass, read through the builds query filtered to phase/x.
func TestShipCIAzureDevOpsFlow(t *testing.T) {
	dir, _, sha := ciMergeRepo(t)
	if _, err := os.Stat(filepath.Join(dir, "azure-pipelines.yml")); !os.IsNotExist(err) {
		t.Fatalf("fixture: azure-pipelines.yml should be absent (%v)", err)
	}
	f := newADOCIMergeForge(t, sha, adoCMBuilds(sha), adoCMApproved)
	adoCMRemote(t, f.URL)
	out, err := runShipVerb(t, "ci", "x")
	if err != nil || out != "ci: pass\n" {
		t.Fatalf("ship ci: %v, printed %q; want exit 0 and `ci: pass`", err, out)
	}
	var builds string
	for _, r := range f.requests() {
		if strings.Contains(r, "/_apis/build/builds?") {
			builds = r
		}
	}
	if !strings.Contains(builds, "branchName=refs%2Fheads%2Fphase%2Fx") {
		t.Errorf("the builds query is not filtered to phase/x: %q (all: %v)", builds, f.requests())
	}
}

// TestShipMergeAzureDevOpsPinsHead: the completion PATCH pins the watched
// head and keeps the source branch, and phase/x is still on origin after.
func TestShipMergeAzureDevOpsPinsHead(t *testing.T) {
	dir, _, sha := ciMergeRepo(t)
	f := newADOCIMergeForge(t, sha, adoCMBuilds(sha), adoCMApproved)
	adoCMRemote(t, f.URL)
	out, err := runShipVerb(t, "merge", "x")
	if err != nil || !strings.Contains(out, "/coll/P/_git/R/pullrequest/7") {
		t.Fatalf("ship merge: %v, printed %q", err, out)
	}
	f.mu.Lock()
	raw := f.patchBody
	f.mu.Unlock()
	var body struct {
		Status                string `json:"status"`
		LastMergeSourceCommit struct {
			CommitID string `json:"commitId"`
		} `json:"lastMergeSourceCommit"`
		CompletionOptions struct {
			MergeStrategy      string `json:"mergeStrategy"`
			DeleteSourceBranch *bool  `json:"deleteSourceBranch"`
		} `json:"completionOptions"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("PATCH body %q: %v", raw, err)
	}
	if body.LastMergeSourceCommit.CommitID != sha {
		t.Errorf("lastMergeSourceCommit = %q, want the watched %s", body.LastMergeSourceCommit.CommitID, sha)
	}
	if d := body.CompletionOptions.DeleteSourceBranch; d == nil || *d {
		t.Errorf("deleteSourceBranch = %v, want an explicit false", d)
	}
	if body.Status != "completed" || body.CompletionOptions.MergeStrategy != "squash" {
		t.Errorf("PATCH body = %s, want a squash completion", raw)
	}
	if ls := gitOutT(t, dir, "ls-remote", "origin", "phase/x"); !strings.Contains(ls, "refs/heads/phase/x") {
		t.Errorf("phase/x is gone from origin after the merge: %q", ls)
	}
}

// TestShipCIMergeTokenNeverLeaks: a forge that answers 403 and echoes the
// credential back leaves neither the token nor its Basic form in what ship ci
// or ship merge print or return, on Azure DevOps and on Bitbucket.
func TestShipCIMergeTokenNeverLeaks(t *testing.T) {
	ciMergeRepo(t)
	var hits atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"message":"denied for Authorization=%s"}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(mirror.Close)

	const bbToken, bbUser = "bb-c4n4ry-do-not-leak-5e1f", "rivil"
	for _, tc := range []struct {
		name, token, basic string
		setup              func()
	}{
		{"azure-devops", adoShipPAT, base64.StdEncoding.EncodeToString([]byte(":" + adoShipPAT)), func() {
			adoCMRemote(t, mirror.URL)
		}},
		{"bitbucket", bbToken, base64.StdEncoding.EncodeToString([]byte(bbUser + ":" + bbToken)), func() {
			t.Setenv("MOCK_BB_TOKEN", bbToken)
			mustRunSet(t, "remote.provider", "bitbucket")
			mustRunSet(t, "remote.url", "https://bitbucket.org/acme/widgets")
			mustRunSet(t, "remote.api_base", mirror.URL+"/2.0")
			mustRunSet(t, "remote.auth_env", "MOCK_BB_TOKEN")
			mustRunSet(t, "remote.auth_user", bbUser)
			if err := runCmd(t, Local(), "set", "allow_hosts", mirror.Listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		tc.setup()
		for _, verb := range []string{"ci", "merge"} {
			hits.Store(0)
			out, err := runShipVerb(t, verb, "x")
			if err == nil {
				t.Fatalf("%s ship %s: a 403 succeeded", tc.name, verb)
			}
			if hits.Load() == 0 {
				t.Fatalf("%s ship %s: the mirror was never reached, so nothing was proven", tc.name, verb)
			}
			for _, secret := range []string{tc.token, tc.basic} {
				if strings.Contains(out, secret) || strings.Contains(err.Error(), secret) {
					t.Errorf("%s ship %s leaked %q:\nout: %s\nerr: %v", tc.name, verb, secret, out, err)
				}
			}
		}
	}
}
