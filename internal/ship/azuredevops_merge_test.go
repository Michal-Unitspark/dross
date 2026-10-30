package ship

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	adoProjectGUID = "3c1f2b4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	adoRepoGUID    = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	adoHeadSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// adoMergeMock answers a PR's reads in sequence (the last answer repeats), its
// completion PATCH and its policy evaluations, and records every request.
type adoMergeMock struct {
	*httptest.Server
	mu          sync.Mutex
	calls       []adoCall
	rawQueries  []string
	prs         []string
	reads       int
	patchStatus int
	patchBody   string
	evalStatus  int
	evalBody    string
}

func newADOMergeMock(t *testing.T, prs ...string) *adoMergeMock {
	t.Helper()
	m := &adoMergeMock{prs: prs, patchStatus: 200, patchBody: `{}`, evalStatus: 200, evalBody: adoEvals()}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		path := r.URL.EscapedPath()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.calls = append(m.calls, adoCall{r.Method, path, r.URL.Query(), body})
		m.rawQueries = append(m.rawQueries, r.URL.RawQuery)
		low := strings.ToLower(path)
		switch {
		case r.Method == "GET" && strings.HasSuffix(low, "/pullrequests/7"):
			i := m.reads
			if i >= len(m.prs) {
				i = len(m.prs) - 1
			}
			m.reads++
			_, _ = w.Write([]byte(m.prs[i]))
		case r.Method == "PATCH" && strings.HasSuffix(low, "/pullrequests/7"):
			w.WriteHeader(m.patchStatus)
			_, _ = w.Write([]byte(m.patchBody))
		case r.Method == "GET" && strings.HasSuffix(path, "/_apis/policy/evaluations"):
			// Only this PR's artifact, by project GUID, gets evaluations.
			if r.URL.Query().Get("artifactId") != "vstfs:///CodeReview/CodeReviewId/"+adoProjectGUID+"/7" {
				_, _ = w.Write([]byte(adoEvals()))
				return
			}
			w.WriteHeader(m.evalStatus)
			_, _ = w.Write([]byte(m.evalBody))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no route"}`))
		}
	}))
	t.Cleanup(m.Close)
	// No completion path may tear anything down provider-side.
	t.Cleanup(func() { m.noTeardown(t) })
	return m
}

func (m *adoMergeMock) count(method, part string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.method == method && strings.Contains(strings.ToLower(c.path), strings.ToLower(part)) {
			n++
		}
	}
	return n
}

// noTeardown fails the test on any DELETE or any request under .../refs.
func (m *adoMergeMock) noTeardown(t *testing.T) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.method == "DELETE" || strings.Contains(c.path, "/refs") {
			t.Errorf("provider-side teardown: %s %s", c.method, c.path)
		}
	}
}

// adoMergePR is a PR read with a status, mergeStatus, head and draft flag.
func adoMergePR(status, mergeStatus, head string, draft bool) string {
	pr := map[string]any{
		"pullRequestId": 7,
		"status":        status,
		"isDraft":       draft,
		"sourceRefName": "refs/heads/phase/x",
		"repository":    map[string]any{"id": adoRepoGUID, "project": map[string]any{"id": adoProjectGUID, "name": "p"}},
	}
	if mergeStatus != "" {
		pr["mergeStatus"] = mergeStatus
	}
	if head != "" {
		pr["lastMergeSourceCommit"] = map[string]any{"commitId": head}
	}
	b, _ := json.Marshal(pr)
	return string(b)
}

// adoEval is one policy evaluation record.
func adoEval(typeID, name, status string, blocking bool) string {
	b, _ := json.Marshal(map[string]any{
		"evaluationId":  "e-" + status,
		"status":        status,
		"configuration": map[string]any{"isBlocking": blocking, "isEnabled": true, "type": map[string]any{"id": typeID, "displayName": name}},
		"artifactId":    "vstfs:///CodeReview/CodeReviewId/" + adoProjectGUID + "/7",
	})
	return string(b)
}

func adoEvals(evals ...string) string {
	return `{"value":[` + strings.Join(evals, ",") + `]}`
}

// fastADOMerge makes the completion wait instant, bounded at polls polls.
func fastADOMerge(t *testing.T, polls int) {
	t.Helper()
	interval, wait, sleep := adoMergeInterval, adoMergeWait, adoMergeSleep
	adoMergeInterval, adoMergeWait = time.Millisecond, time.Duration(polls)*time.Millisecond
	adoMergeSleep = func(time.Duration) {}
	t.Cleanup(func() { adoMergeInterval, adoMergeWait, adoMergeSleep = interval, wait, sleep })
}

func adoMergeOpts(m *adoMergeMock) OpenOpts {
	opts := adoTestOpts(m.Server)
	opts.PRNumber = 7
	return opts
}

// findKey reports whether key appears at any depth of v.
func findKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if strings.EqualFold(k, key) || findKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if findKey(child, key) {
				return true
			}
		}
	}
	return false
}

// TestADOCompletePayload: one PATCH completes the PR as a squash pinned to the
// watched head, keeping the source branch; nothing bypasses a policy, arms
// auto-complete or tears anything down.
func TestADOCompletePayload(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	fastADOMerge(t, 5)
	m := newADOMergeMock(t, adoMergePR("active", "notSet", adoHeadSHA, false), adoMergePR("completed", "succeeded", adoHeadSHA, false))
	got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://dev.azure.com/o/p/_git/r/pullrequest/7"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
	var patches []adoCall
	for _, c := range m.calls {
		if c.method == "PATCH" {
			patches = append(patches, c)
		}
	}
	if len(patches) != 1 || !strings.HasSuffix(patches[0].path, "/_apis/git/repositories/r/pullrequests/7") {
		t.Fatalf("want exactly one PATCH to .../pullrequests/7, got %+v", patches)
	}
	b := patches[0].body
	if b["status"] != "completed" {
		t.Errorf("status = %v, want completed", b["status"])
	}
	if lm, _ := b["lastMergeSourceCommit"].(map[string]any); lm["commitId"] != adoHeadSHA {
		t.Errorf("lastMergeSourceCommit = %v, want commitId %s", b["lastMergeSourceCommit"], adoHeadSHA)
	}
	co, _ := b["completionOptions"].(map[string]any)
	if co["mergeStrategy"] != "squash" || co["squashMerge"] != true || co["transitionWorkItems"] != false {
		t.Errorf("completionOptions = %v", co)
	}
	if del, ok := co["deleteSourceBranch"]; !ok || del != false {
		t.Errorf("deleteSourceBranch = %v (present %v), want present and false", del, ok)
	}
	for _, banned := range []string{"bypassPolicy", "bypassReason", "autoCompleteSetBy"} {
		if findKey(b, banned) {
			t.Errorf("the completion body carries %s", banned)
		}
	}
}

// TestADOCompletePinsHead: a head other than the watched one, an abandoned PR
// and a draft are refused with no write; an already-completed PR is merged
// with none.
func TestADOCompletePinsHead(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	fastADOMerge(t, 5)
	const other = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for name, pr := range map[string]string{
		"moved head": adoMergePR("active", "notSet", other, false),
		"no head":    adoMergePR("active", "notSet", "", false),
		"abandoned":  adoMergePR("abandoned", "", adoHeadSHA, false),
		"draft":      adoMergePR("active", "notSet", adoHeadSHA, true),
		"unknown":    adoMergePR("notSet", "", adoHeadSHA, false),
	} {
		m := newADOMergeMock(t, pr)
		got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
		if err == nil || got != (MergeResult{}) {
			t.Errorf("%s: got %+v, %v; want a refusal", name, got, err)
		}
		if name == "moved head" && (err == nil || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), adoHeadSHA)) {
			t.Errorf("moved head: the error must name both commits: %v", err)
		}
		if n := m.count("PATCH", ""); n != 0 {
			t.Errorf("%s: %d PATCH(es) went out", name, n)
		}
	}

	m := newADOMergeMock(t, adoMergePR("completed", "succeeded", adoHeadSHA, false))
	got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
	if err != nil || got.URL == "" {
		t.Errorf("already completed: got %+v, %v; want merged", got, err)
	}
	if n := m.count("PATCH", ""); n != 0 {
		t.Errorf("already completed: %d PATCH(es) went out", n)
	}
}

// TestADOCompleteWaitsForCompleted: a queued or succeeded merge preview is not
// a merge; only status completed is. Conflicts, failure and a policy
// rejection are errors, never a MergeResult.
func TestADOCompleteWaitsForCompleted(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	fastADOMerge(t, 10)
	pre := adoMergePR("active", "notSet", adoHeadSHA, false)
	m := newADOMergeMock(t, pre,
		adoMergePR("active", "queued", adoHeadSHA, false),
		adoMergePR("active", "succeeded", adoHeadSHA, false),
		adoMergePR("completed", "succeeded", adoHeadSHA, false))
	got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
	if err != nil || got.URL == "" {
		t.Fatalf("got %+v, %v; want merged", got, err)
	}
	if m.reads != 4 {
		t.Errorf("%d PR reads, want 1 before the PATCH and 3 polls", m.reads)
	}

	for status, check := range map[string]func(error) bool{
		"conflicts": func(err error) bool {
			return strings.Contains(err.Error(), "conflicts") && strings.Contains(err.Error(), "phase/x") && !errors.Is(err, ErrMergeBlocked)
		},
		"failure":          func(err error) bool { return !errors.Is(err, ErrMergeBlocked) },
		"rejectedByPolicy": func(err error) bool { return errors.Is(err, ErrMergeBlocked) },
	} {
		m := newADOMergeMock(t, pre, adoMergePR("active", status, adoHeadSHA, false))
		got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
		if err == nil || got != (MergeResult{}) {
			t.Errorf("%s: got %+v, %v; want an error", status, got, err)
			continue
		}
		if !check(err) {
			t.Errorf("%s: wrong error: %v", status, err)
		}
	}
}

// TestADOCompletionBlockedByPolicy: a refused PATCH, or a PR still active at
// the bound, held by a blocking non-CI policy is ErrMergeBlocked naming it.
// The evaluations are read for this PR, by project GUID, escaped once, at the
// preview api-version.
func TestADOCompletionBlockedByPolicy(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	fastADOMerge(t, 3)
	pre := adoMergePR("active", "notSet", adoHeadSHA, false)

	t.Run("refused PATCH", func(t *testing.T) {
		m := newADOMergeMock(t, pre)
		m.patchStatus = 400
		m.patchBody = `{"message":"TF401027: You need at least 1 approving reviewer to complete this pull request."}`
		m.evalBody = adoEvals(
			adoEval(adoTypeBuild, "Build", "approved", true),
			adoEval(adoTypeRequired, "Required reviewers", "rejected", true),
			adoEval(adoTypeRequired, "Required reviewers", "queued", true),
		)
		got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
		if !errors.Is(err, ErrMergeBlocked) || got != (MergeResult{}) {
			t.Fatalf("got %+v, %v; want ErrMergeBlocked", got, err)
		}
		if msg := err.Error(); strings.Count(msg, "Required reviewers") != 1 || !strings.Contains(msg, "TF401027") {
			t.Errorf("the block must name the policy once and keep ADO's message: %v", err)
		}
		m.checkEvaluationsRead(t)
	})

	t.Run("still active at the bound", func(t *testing.T) {
		m := newADOMergeMock(t, pre, adoMergePR("active", "succeeded", adoHeadSHA, false))
		m.evalBody = adoEvals(adoEval(adoTypeComments, "Comment requirements", "queued", true))
		got, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
		if !errors.Is(err, ErrMergeBlocked) || got != (MergeResult{}) {
			t.Fatalf("got %+v, %v; want ErrMergeBlocked", got, err)
		}
		if !strings.Contains(err.Error(), "Comment requirements") {
			t.Errorf("the block does not name the policy: %v", err)
		}
		m.checkEvaluationsRead(t)
	})
}

// checkEvaluationsRead: one evaluations read, for vstfs:///CodeReview/
// CodeReviewId/<project GUID>/7 escaped exactly once, at 7.1-preview.1.
func (m *adoMergeMock) checkEvaluationsRead(t *testing.T) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i, c := range m.calls {
		if !strings.HasSuffix(c.path, "/o/p/_apis/policy/evaluations") {
			continue
		}
		n++
		if got := c.query.Get("api-version"); got != "7.1-preview.1" {
			t.Errorf("evaluations api-version = %q, want 7.1-preview.1", got)
		}
		want := "artifactId=vstfs%3A%2F%2F%2FCodeReview%2FCodeReviewId%2F" + adoProjectGUID + "%2F7"
		if !strings.Contains(m.rawQueries[i], want) {
			t.Errorf("evaluations query %q does not carry %q", m.rawQueries[i], want)
		}
	}
	if n != 1 {
		t.Errorf("%d evaluations reads, want 1", n)
	}
}

// TestADOCompletionRefusedNotBlocked: a refusal no blocking non-CI policy
// explains is a plain error carrying Azure DevOps' own message, never a block.
func TestADOCompletionRefusedNotBlocked(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	fastADOMerge(t, 3)
	pre := adoMergePR("active", "notSet", adoHeadSHA, false)
	ciOnly := adoEvals(adoEval(adoTypeBuild, "Build", "rejected", true), adoEval(adoTypeStatus, "Status", "queued", true))
	allApproved := adoEvals(adoEval(adoTypeRequired, "Required reviewers", "approved", true), adoEval(adoTypeComments, "Comment requirements", "notApplicable", true))
	optional := adoEvals(adoEval(adoTypeMinimum, "Minimum number of reviewers", "queued", false))
	const tfMsg = "TF401181: The pull request cannot be completed."

	for _, status := range []int{400, 409} {
		for name, evals := range map[string]string{"CI only": ciOnly, "all approved": allApproved, "optional reviewers": optional} {
			m := newADOMergeMock(t, pre)
			m.patchStatus, m.patchBody, m.evalBody = status, `{"message":"`+tfMsg+` `+adoTestTok+`"}`, evals
			_, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA)
			if err == nil || errors.Is(err, ErrMergeBlocked) {
				t.Errorf("%d %s: got %v; want a plain error", status, name, err)
				continue
			}
			if !strings.Contains(err.Error(), "TF401181") || strings.Contains(err.Error(), adoTestTok) {
				t.Errorf("%d %s: want ADO's scrubbed message, got %v", status, name, err)
			}
		}
	}

	m := newADOMergeMock(t, pre, adoMergePR("active", "succeeded", adoHeadSHA, false))
	m.evalBody = ciOnly
	if _, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA); err == nil || errors.Is(err, ErrMergeBlocked) ||
		!strings.Contains(err.Error(), "completion not confirmed") {
		t.Errorf("still active, no blocker: got %v; want 'completion not confirmed'", err)
	}

	m = newADOMergeMock(t, pre)
	m.patchStatus, m.evalStatus = 400, 500
	if _, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA); err == nil || errors.Is(err, ErrMergeBlocked) {
		t.Errorf("failed evaluations read: got %v; want a plain error", err)
	}

	m = newADOMergeMock(t, pre)
	m.patchStatus = 500
	if _, err := adoCompletePR(adoMergeOpts(m), adoHeadSHA); err == nil || errors.Is(err, ErrMergeBlocked) {
		t.Errorf("PATCH 500: got %v; want a plain error", err)
	}
	if n := m.count("GET", "/policy/evaluations"); n != 0 {
		t.Errorf("PATCH 500 read the evaluations %d time(s); only a refusal is diagnosed", n)
	}
}
