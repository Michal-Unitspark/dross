package ship

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

// adoOpenPRsMock serves the active-PR list, PR #7's own read (for ship ci),
// builds, and evaluations — answered per PR by the artifactId's trailing
// number, so one PR's evaluations can fail while another's are fine.
type adoOpenPRsMock struct {
	*httptest.Server
	list      string
	listCode  int
	pr7       string
	builds    string
	evals     string
	evalsCode map[int]int
	mu        sync.Mutex
	calls     []adoCall
}

func newADOOpenPRsMock(t *testing.T, m *adoOpenPRsMock) *adoOpenPRsMock {
	t.Helper()
	if m.builds == "" {
		m.builds = adoBuilds()
	}
	if m.evals == "" {
		m.evals = adoEvals()
	}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.calls = append(m.calls, adoCall{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query()})
		m.mu.Unlock()
		p := r.URL.EscapedPath()
		switch {
		case r.Method != "GET":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case strings.HasSuffix(p, "/pullrequests"):
			if m.listCode != 0 {
				w.WriteHeader(m.listCode)
			}
			_, _ = w.Write([]byte(m.list))
		case strings.HasSuffix(p, "/pullRequests/7"):
			_, _ = w.Write([]byte(m.pr7))
		case strings.HasSuffix(p, "/_apis/build/builds"):
			_, _ = w.Write([]byte(m.builds))
		case strings.HasSuffix(p, "/_apis/policy/evaluations"):
			art := r.URL.Query().Get("artifactId")
			n, _ := strconv.Atoi(art[strings.LastIndex(art, "/")+1:])
			if code := m.evalsCode[n]; code != 0 {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			_, _ = w.Write([]byte(m.evals))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *adoOpenPRsMock) count(part string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if strings.Contains(c.path, part) {
			n++
		}
	}
	return n
}

func (m *adoOpenPRsMock) ciReads() int {
	return m.count("/_apis/build/builds") + m.count("/_apis/policy/evaluations")
}

func (m *adoOpenPRsMock) opts() OpenOpts {
	o := adoTestOpts(m.Server)
	o.Provider = "azure-devops"
	return o
}

// adoOpenPR is one active PR as the list returns it; extra overrides fields
// (a nil value deletes the field).
func adoOpenPR(id int, source string, extra map[string]any) string {
	pr := map[string]any{
		"pullRequestId":         id,
		"title":                 "PR " + strconv.Itoa(id),
		"status":                "active",
		"creationDate":          "2026-10-29T08:15:30.123Z",
		"sourceRefName":         source,
		"targetRefName":         "refs/heads/main",
		"createdBy":             map[string]any{"displayName": "Alice", "uniqueName": adoAlice},
		"lastMergeSourceCommit": map[string]any{"commitId": adoHeadSHA},
		"repository":            map[string]any{"id": adoRepoGUID, "project": map[string]any{"id": adoProjectGUID, "name": "p"}},
	}
	for k, v := range extra {
		if v == nil {
			delete(pr, k)
		} else {
			pr[k] = v
		}
	}
	b, _ := json.Marshal(pr)
	return string(b)
}

func adoOpenList(prs ...string) string {
	return `{"value":[` + strings.Join(prs, ",") + `],"count":` + strconv.Itoa(len(prs)) + `}`
}

// TestADOOpenPRsHeads: a record carries the PR's number, title, browser URL,
// creator, creation time and its head without the ref prefix.
func TestADOOpenPRsHeads(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOOpenPRsMock(t, &adoOpenPRsMock{list: adoOpenList(
		adoOpenPR(1, "refs/heads/phase/x", nil),
		adoOpenPR(2, "refs/heads/milestone/v1.8", nil),
	)})
	prs, err := ListOpenPRs(m.opts())
	if err != nil || len(prs) != 2 {
		t.Fatalf("got %+v, %v; want two records", prs, err)
	}
	pr := prs[0]
	if pr.Number != 1 || pr.Title != "PR 1" || pr.HeadRefName != "phase/x" || pr.IsCrossRepository {
		t.Errorf("record = %+v", pr)
	}
	if pr.URL != "https://dev.azure.com/o/p/_git/r/pullrequest/1" {
		t.Errorf("URL = %q, want the browser URL", pr.URL)
	}
	if pr.Author.Login != adoAlice {
		t.Errorf("author = %+v, want the creator's uniqueName", pr.Author)
	}
	if got := pr.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"); got != "2026-10-29T08:15:30.123Z" {
		t.Errorf("CreatedAt = %s", got)
	}
	if prs[1].HeadRefName != "milestone/v1.8" {
		t.Errorf("milestone head = %q", prs[1].HeadRefName)
	}
	// Both are dross's own heads: each had its checks read.
	if n := m.count("/_apis/policy/evaluations"); n != 2 {
		t.Errorf("%d evaluations read(s), want one per shipped PR", n)
	}
	if q := m.calls[0].query; q.Get("searchCriteria.status") != "active" || q.Get("$top") != "100" {
		t.Errorf("list query = %v", q)
	}
}

// TestADOWatchChecksMatchShipCI: the digest's rollup is ship ci's verdict,
// read the same way from the same data — pass/fail/pending/none map onto
// passing/failing/pending/none, and an ask is pending, never passing.
func TestADOWatchChecksMatchShipCI(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	ok := adoBuildRec(1, "completed", "succeeded", adoHeadSHA, adoRepoGUID)
	for _, tc := range []struct {
		name          string
		builds, evals string
		verdict       CIVerdict
		want          CheckRollup
	}{
		{"green", adoBuilds(ok), adoEvals(), CIPass, ChecksPassing},
		{"failed build", adoBuilds(ok, adoBuildRec(2, "completed", "failed", adoHeadSHA, adoRepoGUID)), adoEvals(), CIFail, ChecksFailing},
		{"rejected status policy", adoBuilds(ok), adoEvals(adoEvalCtx(adoTypeStatus, "rejected", 0, false)), CIFail, ChecksFailing},
		{"running", adoBuilds(ok, adoBuildRec(2, "inProgress", "", adoHeadSHA, adoRepoGUID)), adoEvals(), CIPending, ChecksPending},
		{"queued", adoBuilds(adoBuildRec(2, "notStarted", "", adoHeadSHA, adoRepoGUID)), adoEvals(), CIPending, ChecksPending},
		{"nothing ran", adoBuilds(), adoEvals(), CINone, ChecksNone},
		{"reviewers only", adoBuilds(), adoEvals(adoEvalCtx(adoTypeRequired, "rejected", 0, false)), CINone, ChecksNone},
		{"unknown result", adoBuilds(adoBuildRec(2, "completed", "weird", adoHeadSHA, adoRepoGUID)), adoEvals(), CIAsk, ChecksPending},
		{"expired build policy", adoBuilds(ok), adoEvals(adoEvalCtx(adoTypeBuild, "approved", 1, true)), CIAsk, ChecksPending},
	} {
		m := newADOOpenPRsMock(t, &adoOpenPRsMock{
			list:   adoOpenList(adoOpenPR(7, "refs/heads/phase/x", nil)),
			pr7:    adoHeadPR,
			builds: tc.builds,
			evals:  tc.evals,
		})
		opts := m.opts()
		opts.PRNumber = 7
		ci, err := adoCIStatus(opts, adoHeadSHA)
		if err != nil || ci.Verdict != tc.verdict {
			t.Errorf("%s: ship ci got %+v, %v; want %s", tc.name, ci, err, tc.verdict)
			continue
		}
		prs, err := ListOpenPRs(opts)
		if err != nil || len(prs) != 1 {
			t.Errorf("%s: got %+v, %v", tc.name, prs, err)
			continue
		}
		if prs[0].Checks != tc.want {
			t.Errorf("%s: digest reads %s where ship ci reads %s; want %s", tc.name, prs[0].Checks, ci.Verdict, tc.want)
		}
	}
	for _, v := range []CIVerdict{CIAsk, CIPending, "", "unheard-of"} {
		if got := adoWatchChecks(v); got != ChecksPending {
			t.Errorf("verdict %q maps to %s, want pending", v, got)
		}
	}
}

// TestADOOpenPRsOneChecksReadFails: one PR's checks failing to read fails the
// call — never a list where that PR reads none.
func TestADOOpenPRsOneChecksReadFails(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOOpenPRsMock(t, &adoOpenPRsMock{
		list:      adoOpenList(adoOpenPR(1, "refs/heads/phase/a", nil), adoOpenPR(2, "refs/heads/phase/b", nil)),
		evalsCode: map[int]int{2: 500},
	})
	prs, err := ListOpenPRs(m.opts())
	if prs != nil || err == nil {
		t.Errorf("got (%+v, %v), want (nil, err)", prs, err)
	}
	if n := m.count("/_apis/policy/evaluations"); n != 2 {
		t.Errorf("%d evaluations read(s); PR 1's read must have succeeded first", n)
	}

	// A shipped PR with no head commit is an error, not a none.
	m = newADOOpenPRsMock(t, &adoOpenPRsMock{list: adoOpenList(adoOpenPR(1, "refs/heads/phase/a", map[string]any{"lastMergeSourceCommit": nil}))})
	if prs, err := ListOpenPRs(m.opts()); prs != nil || err == nil {
		t.Errorf("no head: got (%+v, %v), want (nil, err)", prs, err)
	}
}

// TestADOOpenPRsRefusesIncomplete: a full page, a missing list, a PR with no
// id or creation date, a non-branch source and a sign-in page each error.
func TestADOOpenPRsRefusesIncomplete(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	full := make([]string, adoPRListTop)
	for i := range full {
		full[i] = adoOpenPR(i+1, "refs/heads/feature/f"+strconv.Itoa(i), nil)
	}
	for name, m := range map[string]*adoOpenPRsMock{
		"a full page":        {list: adoOpenList(full...)},
		"no value array":     {list: `{"count":0}`},
		"null value array":   {list: `{"value":null}`},
		"non-JSON":           {list: `<html>sign in</html>`},
		"pullRequestId 0":    {list: adoOpenList(adoOpenPR(0, "refs/heads/feature/z", nil))},
		"no pullRequestId":   {list: adoOpenList(adoOpenPR(1, "refs/heads/feature/z", map[string]any{"pullRequestId": nil}))},
		"empty creationDate": {list: adoOpenList(adoOpenPR(1, "refs/heads/feature/z", map[string]any{"creationDate": ""}))},
		"no creationDate":    {list: adoOpenList(adoOpenPR(1, "refs/heads/feature/z", map[string]any{"creationDate": nil}))},
		"not a branch":       {list: adoOpenList(adoOpenPR(1, "refs/tags/v1", nil))},
		"203 sign-in page":   {list: `{"value":[]}`, listCode: http.StatusNonAuthoritativeInfo},
		"500":                {list: `{"message":"boom"}`, listCode: 500},
	} {
		m := newADOOpenPRsMock(t, m)
		if prs, err := ListOpenPRs(m.opts()); prs != nil || err == nil {
			t.Errorf("%s: got (%d records, %v), want (nil, err)", name, len(prs), err)
		}
	}

	m := newADOOpenPRsMock(t, &adoOpenPRsMock{list: adoOpenList()})
	prs, err := ListOpenPRs(m.opts())
	if err != nil || prs == nil || len(prs) != 0 {
		t.Errorf("nothing open: got (%#v, %v), want an empty non-nil list", prs, err)
	}
}

// TestADOOpenPRsNoGuessing: ADO has no bot flag, so nothing is a bot — not
// even a Build Service creator; a fork's phase/ PR and a feature/ PR are not
// dross's, so they read ChecksNone with no CI request.
func TestADOOpenPRsNoGuessing(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOOpenPRsMock(t, &adoOpenPRsMock{list: adoOpenList(
		adoOpenPR(1, "refs/heads/feature/z", map[string]any{"createdBy": map[string]any{
			"displayName": "Project Collection Build Service (o)",
			"uniqueName":  "Build\\0c6f9b8a-1d2e-4f3a-9b8c-7d6e5f4a3b2c",
		}}),
		adoOpenPR(2, "refs/heads/phase/y", map[string]any{"forkSource": map[string]any{"name": "refs/heads/phase/y"}}),
		adoOpenPR(3, "refs/heads/phase/z", map[string]any{"forkSource": nil}),
	)})
	prs, err := ListOpenPRs(m.opts())
	if err != nil || len(prs) != 3 {
		t.Fatalf("got %+v, %v", prs, err)
	}
	for _, pr := range prs {
		if pr.Author.IsBot {
			t.Errorf("PR #%d (%s) is flagged a bot", pr.Number, pr.Author.Login)
		}
	}
	if prs[0].Checks != ChecksNone || prs[1].Checks != ChecksNone {
		t.Errorf("feature/z reads %s, fork phase/y reads %s; want none for both", prs[0].Checks, prs[1].Checks)
	}
	if !prs[1].IsCrossRepository || prs[0].IsCrossRepository || prs[2].IsCrossRepository {
		t.Errorf("IsCrossRepository = %v/%v/%v, want false/true/false", prs[0].IsCrossRepository, prs[1].IsCrossRepository, prs[2].IsCrossRepository)
	}
	// Only PR 3 — this repository's phase/z — had its checks read.
	if n := m.count("/_apis/build/builds"); n != 1 {
		t.Errorf("%d builds read(s), want 1", n)
	}
	if n := m.count("/_apis/policy/evaluations"); n != 1 {
		t.Errorf("%d evaluations read(s), want 1", n)
	}
	m.mu.Lock()
	for _, c := range m.calls {
		if strings.Contains(c.path, "/_apis/policy/evaluations") && !strings.HasSuffix(c.query.Get("artifactId"), "/3") {
			t.Errorf("evaluations read for %s", c.query.Get("artifactId"))
		}
	}
	m.mu.Unlock()
}

// TestADOOpenPRsDispatch: widening the lister to Azure DevOps leaves every
// other provider unsupported with nothing sent, and an ADO api_base off the
// host allowlist is refused before any socket.
func TestADOOpenPRsDispatch(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOOpenPRsMock(t, &adoOpenPRsMock{list: adoOpenList()})
	for _, p := range []string{"forgejo", "gitlab", "bitbucket"} {
		opts := m.opts()
		opts.Provider = p
		if prs, err := ListOpenPRs(opts); prs != nil || !errors.Is(err, ErrOpenPRListUnsupported) {
			t.Errorf("provider %q: got (%v, %v), want ErrOpenPRListUnsupported", p, prs, err)
		}
	}
	opts := m.opts()
	opts.Provider = " Azure-DevOps "
	if _, err := ListOpenPRs(opts); err != nil {
		t.Errorf("a padded azure-devops provider: %v", err)
	}
	if n := len(m.calls); n != 1 {
		t.Errorf("%d request(s), want exactly the one azure-devops list", n)
	}

	opts = m.opts()
	opts.Hosts = hostallow.Derive(adoTestURL, nil)
	m.mu.Lock()
	m.calls = nil
	m.mu.Unlock()
	prs, err := ListOpenPRs(opts)
	if prs != nil || !errors.Is(err, hostallow.ErrRefused) {
		t.Errorf("off-allowlist api_base: got (%v, %v), want ErrRefused", prs, err)
	}
	if n := len(m.calls); n != 0 {
		t.Errorf("off-allowlist api_base: %d request(s) reached the server", n)
	}
}
