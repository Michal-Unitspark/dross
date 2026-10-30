package ship

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	bbMergePath = bbPullPath + "/merge"
	bbTaskPath  = bbMergePath + "/task-status/abc"
)

// bbMerge is a scripted Bitbucket merge: the PR pre-read, the merge POST (a
// 202 carries location), and its task status in sequence (the last repeats).
type bbMerge struct {
	pull      string
	mergeCode int
	mergeBody string
	location  string
	tasks     []string
	mu        sync.Mutex
	calls     []adoCall
	taskHits  atomic.Int32
}

func (b *bbMerge) serve(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		b.mu.Lock()
		b.calls = append(b.calls, adoCall{method: r.Method, path: r.URL.Path, body: body})
		b.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == bbPullPath:
			_, _ = w.Write([]byte(b.pull))
		case r.Method == "POST" && r.URL.Path == bbMergePath:
			if b.mergeCode == http.StatusAccepted && b.location != "-" {
				loc := b.location
				if loc == "" {
					loc = srv.URL + bbTaskPath
				}
				w.Header().Set("Location", loc)
			}
			w.WriteHeader(b.mergeCode)
			_, _ = w.Write([]byte(b.mergeBody))
		case r.Method == "GET" && strings.HasPrefix(strings.ToLower(r.URL.Path), bbMergePath+"/task-status/"):
			i := int(b.taskHits.Add(1)) - 1
			if i >= len(b.tasks) {
				i = len(b.tasks) - 1
			}
			_, _ = w.Write([]byte(b.tasks[i]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	// No merge path may tear anything down provider-side.
	t.Cleanup(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, c := range b.calls {
			if c.method == "DELETE" || strings.Contains(c.path, "/refs/branches") {
				t.Errorf("provider-side teardown: %s %s", c.method, c.path)
			}
		}
	})
	return srv
}

func (b *bbMerge) posts() []adoCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []adoCall
	for _, c := range b.calls {
		if c.method == "POST" {
			out = append(out, c)
		}
	}
	return out
}

func bbMergePull(state, hash string) string {
	return `{"id":7,"state":"` + state + `","links":{"html":{"href":"https://bitbucket.org/acme/widgets/pull-requests/7"}},` +
		`"source":{"branch":{"name":"phase/x"},"commit":{"hash":"` + hash + `"}}}`
}

const (
	bbMergedBody = `{"id":7,"state":"MERGED"}`
	bbPending    = `{"task_status":"PENDING"}`
	bbSucceeded  = `{"task_status":"SUCCESS","merge_result":{"id":7,"state":"MERGED"}}`
)

// bbOpenPull is an open PR whose head prefixes bbCISHA.
var bbOpenPull = bbMergePull("OPEN", "0123456789ab")

// fastBBMerge makes the async wait instant, bounded at polls polls.
func fastBBMerge(t *testing.T, polls int) {
	t.Helper()
	interval, wait, sleep := bbMergeInterval, bbMergeWait, bbMergeSleep
	bbMergeInterval, bbMergeWait = time.Millisecond, time.Duration(polls)*time.Millisecond
	bbMergeSleep = func(time.Duration) {}
	t.Cleanup(func() { bbMergeInterval, bbMergeWait, bbMergeSleep = interval, wait, sleep })
}

func mergeBB(t *testing.T, b *bbMerge) (MergeResult, error) {
	t.Helper()
	srv := b.serve(t)
	opts := bbOpts(t, srv.URL)
	opts.PRNumber = 7
	return bitbucketMergePR(opts, bbCISHA)
}

// TestBitbucketMergePayload: one POST squash-merges and keeps the source
// branch.
func TestBitbucketMergePayload(t *testing.T) {
	fastBBMerge(t, 5)
	b := &bbMerge{pull: bbOpenPull, mergeCode: 200, mergeBody: bbMergedBody}
	got, err := mergeBB(t, b)
	if err != nil || got.URL != "https://bitbucket.org/acme/widgets/pull-requests/7" {
		t.Fatalf("got %+v, %v; want merged", got, err)
	}
	posts := b.posts()
	if len(posts) != 1 || posts[0].path != bbMergePath {
		t.Fatalf("want exactly one POST to .../pullrequests/7/merge, got %+v", posts)
	}
	body := posts[0].body
	if body["merge_strategy"] != "squash" || body["type"] != "pullrequest" {
		t.Errorf("merge body = %v", body)
	}
	if v, ok := body["close_source_branch"]; !ok || v != false {
		t.Errorf("close_source_branch = %v (present %v), want present and false", v, ok)
	}
}

// TestBitbucketMergeAsync: a 200 must say MERGED and polls nothing; a 202 is
// polled at its task-status link until it says the PR merged.
func TestBitbucketMergeAsync(t *testing.T) {
	fastBBMerge(t, 5)
	b := &bbMerge{pull: bbOpenPull, mergeCode: 200, mergeBody: bbMergedBody}
	if _, err := mergeBB(t, b); err != nil || b.taskHits.Load() != 0 {
		t.Errorf("200 MERGED: got %v with %d poll(s); want merged with none", err, b.taskHits.Load())
	}
	b = &bbMerge{pull: bbOpenPull, mergeCode: 200, mergeBody: `{"id":7,"state":"OPEN"}`}
	if got, err := mergeBB(t, b); err == nil || got != (MergeResult{}) {
		t.Errorf("200 not MERGED: got %+v, %v; want an error", got, err)
	}

	b = &bbMerge{pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{bbPending, bbPending, bbSucceeded}}
	got, err := mergeBB(t, b)
	if err != nil || got.URL == "" || b.taskHits.Load() != 3 {
		t.Errorf("202: got %+v, %v after %d poll(s); want merged after 3", got, err, b.taskHits.Load())
	}

	b = &bbMerge{pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{`{"task_status":"SUCCESS","merge_result":{"state":"OPEN"}}`}}
	if got, err := mergeBB(t, b); err == nil || got != (MergeResult{}) {
		t.Errorf("SUCCESS without MERGED: got %+v, %v; want an error", got, err)
	}
	b = &bbMerge{pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{bbPending}}
	if got, err := mergeBB(t, b); err == nil || got != (MergeResult{}) || !strings.Contains(err.Error(), "merge not confirmed") {
		t.Errorf("still PENDING at the bound: got %+v, %v; want 'merge not confirmed'", got, err)
	}
}

// TestBitbucketMergeLocationVetted: the credential follows a 202's Location
// only on api_base's scheme and host and under this merge's task-status path.
// Each refusal is the vetting's own — never a transport error or a 404 from a
// request that went out — and the server sees nothing past the pre-read and
// the merge POST.
func TestBitbucketMergeLocationVetted(t *testing.T) {
	fastBBMerge(t, 5)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(bbSucceeded))
	}))
	t.Cleanup(other.Close)

	for name, loc := range map[string]func(srv string) string{
		"another host":   func(string) string { return other.URL + bbTaskPath },
		"another scheme": func(srv string) string { return strings.Replace(srv, "http://", "https://", 1) + bbTaskPath },
		"another PR": func(srv string) string {
			return srv + "/2.0/repositories/acme/widgets/pullrequests/8/merge/task-status/abc"
		},
		"outside task-status": func(srv string) string { return srv + bbMergePath + "/other/abc" },
		"traversal":           func(srv string) string { return srv + bbMergePath + "/task-status/../../../../../user" },
		"bare task-status":    func(srv string) string { return srv + bbMergePath + "/task-status/" },
		"token in the link":   func(string) string { return other.URL + "/bb-secret" },
	} {
		b := &bbMerge{pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{bbSucceeded}}
		srv := b.serve(t)
		b.location = loc(srv.URL)
		opts := bbOpts(t, srv.URL)
		opts.PRNumber = 7
		got, err := bitbucketMergePR(opts, bbCISHA)
		if err == nil || got != (MergeResult{}) {
			t.Errorf("%s: got %+v, %v; want a refusal", name, got, err)
			continue
		}
		if !strings.Contains(err.Error(), "not following it") {
			t.Errorf("%s: not the Location vetting's refusal: %v", name, err)
		}
		if strings.Contains(err.Error(), "bb-secret") {
			t.Errorf("%s: the token reached the error: %v", name, err)
		}
		b.mu.Lock()
		if len(b.calls) != 2 || b.calls[0].method != "GET" || b.calls[1].method != "POST" {
			t.Errorf("%s: want only the pre-read and the merge POST, got %+v", name, b.calls)
		}
		b.mu.Unlock()
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the credential reached another server %d time(s)", n)
	}

	// The same task under a workspace and slug spelled in another case is
	// still this merge's.
	b := &bbMerge{pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{bbSucceeded}}
	srv := b.serve(t)
	b.location = srv.URL + "/2.0/repositories/ACME/Widgets/pullrequests/7/merge/task-status/abc"
	opts := bbOpts(t, srv.URL)
	opts.PRNumber = 7
	if got, err := bitbucketMergePR(opts, bbCISHA); err != nil || got.URL == "" || b.taskHits.Load() != 1 {
		t.Errorf("case-varied task link: got %+v, %v after %d poll(s); want merged", got, err, b.taskHits.Load())
	}
}

// TestBitbucketMergeBlocked: a 400 for unmet merge checks or approvals is
// ErrMergeBlocked, scrubbed; a conflict or another 400 is a plain error.
func TestBitbucketMergeBlocked(t *testing.T) {
	fastBBMerge(t, 5)
	for name, body := range map[string]string{
		"merge checks": `{"type":"error","error":{"message":"You can't merge until you resolve all merge checks. bb-secret"}}`,
		"approvals":    `{"type":"error","error":{"message":"This pull request needs 2 approvals before it can be merged. bb-secret"}}`,
	} {
		_, err := mergeBB(t, &bbMerge{pull: bbOpenPull, mergeCode: 400, mergeBody: body})
		if !errors.Is(err, ErrMergeBlocked) {
			t.Errorf("%s: got %v, want ErrMergeBlocked", name, err)
			continue
		}
		if strings.Contains(err.Error(), "bb-secret") {
			t.Errorf("%s: the token reached the error: %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		code int
		body string
	}{
		"409 conflict":     {409, `{"type":"error","error":{"message":"Merge conflict"}}`},
		"400 not a policy": {400, `{"type":"error","error":{"message":"Invalid merge strategy"}}`},
	} {
		_, err := mergeBB(t, &bbMerge{pull: bbOpenPull, mergeCode: tc.code, mergeBody: tc.body})
		if err == nil || errors.Is(err, ErrMergeBlocked) {
			t.Errorf("%s: got %v; want a plain error", name, err)
		}
	}
}

// TestBitbucketMergeRefusals: nothing short of a confirmed merge returns a
// MergeResult; a merged PR is done with no POST; a declined or superseded PR,
// or a head other than the watched one, is refused with none.
func TestBitbucketMergeRefusals(t *testing.T) {
	fastBBMerge(t, 5)
	for name, b := range map[string]*bbMerge{
		"409":                  {pull: bbOpenPull, mergeCode: 409, mergeBody: `{}`},
		"555":                  {pull: bbOpenPull, mergeCode: 555, mergeBody: `{}`},
		"202 without Location": {pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, location: "-"},
		"task error":           {pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{`{"task_status":"ERROR"}`}},
		"task unrecognised":    {pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{`{"task_status":"WEIRD"}`}},
		"task non-JSON":        {pull: bbOpenPull, mergeCode: 202, mergeBody: `{}`, tasks: []string{`<html>no</html>`}},
	} {
		if got, err := mergeBB(t, b); err == nil || got != (MergeResult{}) {
			t.Errorf("%s: got %+v, %v; want an error and no MergeResult", name, got, err)
		}
	}

	b := &bbMerge{pull: bbMergePull("MERGED", "0123456789ab")}
	if got, err := mergeBB(t, b); err != nil || got.URL == "" {
		t.Errorf("already merged: got %+v, %v; want merged", got, err)
	}
	if n := len(b.posts()); n != 0 {
		t.Errorf("already merged: %d POST(s)", n)
	}
	for name, pull := range map[string]string{
		"declined":   bbMergePull("DECLINED", "0123456789ab"),
		"superseded": bbMergePull("SUPERSEDED", "0123456789ab"),
		"moved head": bbMergePull("OPEN", "fedcba987654"),
		"no head":    bbMergePull("OPEN", ""),
	} {
		b := &bbMerge{pull: pull, mergeCode: 200, mergeBody: bbMergedBody}
		if got, err := mergeBB(t, b); err == nil || got != (MergeResult{}) {
			t.Errorf("%s: got %+v, %v; want a refusal", name, got, err)
		}
		if n := len(b.posts()); n != 0 {
			t.Errorf("%s: %d POST(s)", name, n)
		}
	}
}
