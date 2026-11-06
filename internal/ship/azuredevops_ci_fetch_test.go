package ship

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// adoBuildRec is one build in a builds list.
func adoBuildRec(id int, status, result, version, repoID string) string {
	b, _ := json.Marshal(map[string]any{
		"id":            id,
		"status":        status,
		"result":        result,
		"sourceBranch":  "refs/heads/phase/x",
		"sourceVersion": version,
		"repository":    map[string]any{"id": repoID, "type": "TfsGit"},
		"_links":        map[string]any{"web": map[string]any{"href": "https://dev.azure.com/o/p/_build/results?buildId=" + strconv.Itoa(id)}},
	})
	return string(b)
}

func adoBuilds(builds ...string) string {
	return `{"value":[` + strings.Join(builds, ",") + `],"count":` + strconv.Itoa(len(builds)) + `}`
}

// adoEvalCtx is a policy evaluation carrying a build context.
func adoEvalCtx(typeID, status string, buildID int, expired bool) string {
	b, _ := json.Marshal(map[string]any{
		"status":        status,
		"configuration": map[string]any{"isBlocking": true, "type": map[string]any{"id": typeID, "displayName": "policy"}},
		"context":       map[string]any{"buildId": buildID, "isExpired": expired},
	})
	return string(b)
}

// adoCIMock serves a PR read, its builds and its evaluations.
func adoCIMock(t *testing.T, pr, builds, evals string) *adoPRMock {
	t.Helper()
	return newADOPRMock(t,
		adoRoute{"GET", "/pullRequests/7", 200, pr},
		adoRoute{"GET", "/_apis/build/builds", 200, builds},
		adoRoute{"GET", "/_apis/policy/evaluations", 200, evals},
	)
}

func readADOCI(t *testing.T, m *adoPRMock) (CIResult, error) {
	t.Helper()
	opts := adoTestOpts(m.Server)
	opts.PRNumber = 7
	return adoCIStatus(opts, adoHeadSHA)
}

// ciReads counts the builds and evaluations requests the mock saw.
func ciReads(m *adoPRMock) int {
	return len(m.matching("GET", "/_apis/build/builds")) + len(m.matching("GET", "/_apis/policy/evaluations"))
}

var adoHeadPR = adoMergePR("active", "succeeded", adoHeadSHA, false)

// TestADOCIBuildsAtHeadOnly: a build of an older push, or of another
// repository, says nothing about the head; a failed build at the head, in any
// hex case, fails with its link.
func TestADOCIBuildsAtHeadOnly(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	const older = "cccccccccccccccccccccccccccccccccccccccc"
	m := adoCIMock(t, adoHeadPR, adoBuilds(
		adoBuildRec(1, "completed", "succeeded", older, adoRepoGUID),
		adoBuildRec(2, "completed", "failed", adoHeadSHA, "other-repo"),
	), adoEvals())
	got, err := readADOCI(t, m)
	if err != nil || got.Verdict != CINone {
		t.Errorf("stale and foreign builds: got %+v, %v; want none", got, err)
	}

	m = adoCIMock(t, adoHeadPR, adoBuilds(
		adoBuildRec(1, "completed", "succeeded", older, adoRepoGUID),
		adoBuildRec(3, "completed", "failed", strings.ToUpper(adoHeadSHA), strings.ToUpper(adoRepoGUID)),
	), adoEvals())
	got, err = readADOCI(t, m)
	if err != nil || got.Verdict != CIFail || got.Link != "https://dev.azure.com/o/p/_build/results?buildId=3" {
		t.Errorf("failed build at the head: got %+v, %v; want fail linking build 3", got, err)
	}
}

// TestADOCIBuildsQuery: the builds read asks for this repository's builds of
// the source branch, newest first, and nothing narrower — no definition or
// YAML filter, so classic pipelines count. A full page is refused, and branch
// policy configuration is never read.
func TestADOCIBuildsQuery(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := adoCIMock(t, adoHeadPR, adoBuilds(adoBuildRec(1, "completed", "succeeded", adoHeadSHA, adoRepoGUID)),
		adoEvals(adoEval(adoTypeBuild, "Build", "approved", true)))
	got, err := readADOCI(t, m)
	if err != nil || got.Verdict != CIPass {
		t.Fatalf("got %+v, %v; want pass", got, err)
	}
	reads := m.matching("GET", "/_apis/build/builds")
	if len(reads) != 1 || !strings.HasSuffix(reads[0].path, "/o/p/_apis/build/builds") {
		t.Fatalf("want one GET <api_base>/<project>/_apis/build/builds, got %+v", m.calls)
	}
	q := reads[0].query
	for key, want := range map[string]string{
		"branchName":     "refs/heads/phase/x",
		"repositoryId":   adoRepoGUID,
		"repositoryType": "TfsGit",
		"queryOrder":     "queueTimeDescending",
		"$top":           strconv.Itoa(adoBuildListTop),
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for key := range q {
		switch key {
		case "branchName", "repositoryId", "repositoryType", "queryOrder", "$top", "api-version":
		default:
			t.Errorf("the builds read narrows by %s=%v; classic pipelines would drop out", key, q[key])
		}
	}
	for _, c := range m.calls {
		if strings.Contains(c.path, "/_apis/policy/configurations") {
			t.Errorf("branch policy configuration was read: %s", c.path)
		}
	}

	var full []string
	for i := 1; i <= adoBuildListTop; i++ {
		full = append(full, adoBuildRec(i, "completed", "succeeded", adoHeadSHA, adoRepoGUID))
	}
	m = adoCIMock(t, adoHeadPR, adoBuilds(full...), adoEvals())
	if got, err := readADOCI(t, m); err == nil {
		t.Errorf("a full builds page gave %+v with a nil error", got)
	}
}

// TestADOEvaluationArtifactID: evaluations are read for this PR by project
// GUID, escaped once, at the preview api-version; a PR that names no project
// or repository id is an error before any CI read.
func TestADOEvaluationArtifactID(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := adoCIMock(t, adoHeadPR, adoBuilds(), adoEvals(adoEval(adoTypeStatus, "Status", "approved", true)))
	got, err := readADOCI(t, m)
	if err != nil || got.Verdict != CIPass {
		t.Fatalf("got %+v, %v; want pass", got, err)
	}
	evals := m.matching("GET", "/o/p/_apis/policy/evaluations")
	if len(evals) != 1 {
		t.Fatalf("want one evaluations read, got %+v", m.calls)
	}
	if got, want := evals[0].query.Get("artifactId"), "vstfs:///CodeReview/CodeReviewId/"+adoProjectGUID+"/7"; got != want {
		t.Errorf("artifactId = %q, want %q (escaped once)", got, want)
	}
	if got := evals[0].query.Get("api-version"); got != "7.1-preview.1" {
		t.Errorf("api-version = %q, want 7.1-preview.1", got)
	}

	for name, strip := range map[string]string{"project id": `,"project":{"id":"` + adoProjectGUID + `","name":"p"}`, "repository id": `"id":"` + adoRepoGUID + `",`} {
		pr := strings.Replace(adoHeadPR, strip, "", 1)
		// The stripped body must still be a PR — a parse failure would pass
		// this row without ever reaching the id guard.
		if pr == adoHeadPR || !json.Valid([]byte(pr)) {
			t.Fatalf("%s: fixture did not change, or is no longer JSON: %s", name, pr)
		}
		m := adoCIMock(t, pr, adoBuilds(), adoEvals())
		if got, err := readADOCI(t, m); err == nil {
			t.Errorf("no %s: got %+v with a nil error", name, got)
		}
		if n := ciReads(m); n != 0 {
			t.Errorf("no %s: %d CI read(s) went out", name, n)
		}
	}
}

// TestADOCIStaleHead: a PR that does not carry the watched head yet is
// pending with a reason naming both commits — never pass or none — and its
// CI is not read.
func TestADOCIStaleHead(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	const other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for name, pr := range map[string]string{
		"moved":  adoMergePR("active", "succeeded", other, false),
		"absent": adoMergePR("active", "succeeded", "", false),
	} {
		m := adoCIMock(t, pr, adoBuilds(adoBuildRec(1, "completed", "succeeded", adoHeadSHA, adoRepoGUID)), adoEvals())
		got, err := readADOCI(t, m)
		if err != nil || got.Verdict != CIPending {
			t.Errorf("%s: got %+v, %v; want pending", name, got, err)
		}
		if !strings.Contains(got.Reason, adoHeadSHA) || !strings.Contains(got.Reason, "dross ship x") {
			t.Errorf("%s: reason %q must name the watched head and `dross ship x`", name, got.Reason)
		}
		if name == "moved" && !strings.Contains(got.Reason, other) {
			t.Errorf("moved: reason %q must name the PR's head %s", got.Reason, other)
		}
		if n := ciReads(m); n != 0 {
			t.Errorf("%s: %d CI read(s) went out for a head the PR does not carry", name, n)
		}
	}
}

// TestADOCIReadFailures: a failed PR, builds or evaluations read is an error,
// never a verdict; a rejected build-validation evaluation links its build.
func TestADOCIReadFailures(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for _, faulty := range []string{"/pullRequests/7", "/_apis/build/builds", "/_apis/policy/evaluations"} {
		for name, fault := range map[string]struct {
			status int
			body   string
		}{
			"500":              {500, `{"message":"boom"}`},
			"203 sign-in page": {http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`},
			"non-JSON body":    {200, `<html>nope</html>`},
		} {
			routes := map[string]adoRoute{
				"/pullRequests/7":           {"GET", "/pullRequests/7", 200, adoHeadPR},
				"/_apis/build/builds":       {"GET", "/_apis/build/builds", 200, adoBuilds()},
				"/_apis/policy/evaluations": {"GET", "/_apis/policy/evaluations", 200, adoEvals()},
			}
			routes[faulty] = adoRoute{"GET", faulty, fault.status, fault.body}
			m := newADOPRMock(t, routes["/pullRequests/7"], routes["/_apis/build/builds"], routes["/_apis/policy/evaluations"])
			got, err := readADOCI(t, m)
			if err == nil || got != (CIResult{}) {
				t.Errorf("%s on %s: got %+v, %v; want (CIResult{}, err)", name, faulty, got, err)
			}
		}
	}

	m := adoCIMock(t, adoHeadPR, adoBuilds(), adoEvals(adoEvalCtx(adoTypeBuild, "rejected", 88, false)))
	got, err := readADOCI(t, m)
	if want := m.URL + "/o/p/_build/results?buildId=88"; err != nil || got.Verdict != CIFail || got.Link != want {
		t.Errorf("rejected build policy: got %+v, %v; want fail linking %s", got, err, want)
	}
}

// TestADOStatusPolicyFailLink: a rejected status check has no build to link,
// so the fail links the PR — never an empty link.
func TestADOStatusPolicyFailLink(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := adoCIMock(t, adoHeadPR, adoBuilds(), adoEvals(adoEval(adoTypeStatus, "Status", "rejected", true)))
	got, err := readADOCI(t, m)
	if want := "https://dev.azure.com/o/p/_git/r/pullrequest/7"; err != nil || got.Verdict != CIFail || got.Link != want {
		t.Errorf("got %+v, %v; want fail linking %s", got, err, want)
	}
}

// adoBuildRun is a build of pipeline def at the head, queued at queued.
func adoBuildRun(id, def int, queued, status, result string) string {
	b, _ := json.Marshal(map[string]any{
		"id":            id,
		"status":        status,
		"result":        result,
		"queueTime":     queued,
		"definition":    map[string]any{"id": def},
		"sourceVersion": adoHeadSHA,
		"repository":    map[string]any{"id": adoRepoGUID},
		"_links":        map[string]any{"web": map[string]any{"href": "https://dev.azure.com/o/p/_build/results?buildId=" + strconv.Itoa(id)}},
	})
	return string(b)
}

// TestADOCIRerunSupersedes: a pipeline run again at the same head counts once,
// as its latest run by queue time — whatever order the server lists them in —
// while different pipelines each count.
func TestADOCIRerunSupersedes(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	const early, late = "2026-10-30T10:00:00.1Z", "2026-10-30T10:05:00.25Z"
	for _, tc := range []struct {
		name   string
		builds []string
		want   CIResult
	}{
		{"failed then re-run green", []string{adoBuildRun(1, 5, early, "completed", "failed"), adoBuildRun(2, 5, late, "completed", "succeeded")},
			CIResult{Verdict: CIPass}},
		{"re-run green listed first", []string{adoBuildRun(2, 5, late, "completed", "succeeded"), adoBuildRun(1, 5, early, "completed", "failed")},
			CIResult{Verdict: CIPass}},
		{"green then re-run red", []string{adoBuildRun(1, 5, early, "completed", "succeeded"), adoBuildRun(2, 5, late, "completed", "failed")},
			CIResult{Verdict: CIFail, Link: "https://dev.azure.com/o/p/_build/results?buildId=2"}},
		{"re-run still running", []string{adoBuildRun(1, 5, early, "completed", "failed"), adoBuildRun(2, 5, late, "inProgress", "")},
			CIResult{Verdict: CIPending}},
		{"two pipelines both count", []string{adoBuildRun(1, 5, late, "completed", "succeeded"), adoBuildRun(2, 6, early, "completed", "failed")},
			CIResult{Verdict: CIFail, Link: "https://dev.azure.com/o/p/_build/results?buildId=2"}},
	} {
		m := adoCIMock(t, adoHeadPR, adoBuilds(tc.builds...), adoEvals())
		got, err := readADOCI(t, m)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

// TestADOCIForPRNeedsAHead: a PR that names no head commit is an error, never
// a verdict from its evaluations alone — and nothing is read for it.
func TestADOCIForPRNeedsAHead(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := adoCIMock(t, adoHeadPR, adoBuilds(), adoEvals(adoEval(adoTypeBuild, "Build", "approved", true)))
	c, err := adoOpenConn(adoTestOpts(m.Server))
	if err != nil {
		t.Fatal(err)
	}
	pr := adoPullRequest{Status: "active", SourceRefName: "refs/heads/phase/x", RepoID: adoRepoGUID, ProjectID: adoProjectGUID}
	if got, err := adoCIForPR(c, 7, pr); err == nil || got != (CIResult{}) {
		t.Errorf("no head: got %+v, %v; want (CIResult{}, err)", got, err)
	}
	if len(m.calls) != 0 {
		t.Errorf("%d request(s) went out for a PR with no head", len(m.calls))
	}
}
