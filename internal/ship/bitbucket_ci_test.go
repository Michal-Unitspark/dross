package ship

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

const (
	bbCISHA       = "0123456789abcdef0123456789abcdef01234567"
	bbCIStatusDir = "/2.0/repositories/acme/widgets/commit/" + bbCISHA + "/statuses"
)

// bbCI is a scripted Bitbucket API: PR #7 and its head's statuses, served in
// pages, every page but the last carrying a `next` that must never be
// requested.
type bbCI struct {
	pull       string
	pages      [][]map[string]any
	pullCode   int
	pageCode   map[int]int
	pageBody   map[int]string
	hits       atomic.Int32
	statusHits atomic.Int32
	nextHits   atomic.Int32
}

func (b *bbCI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.hits.Add(1)
		if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("rivil:bb-secret")) {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case bbPullPath:
			if b.pullCode != 0 {
				w.WriteHeader(b.pullCode)
			}
			_, _ = w.Write([]byte(b.pull))
		case bbCIStatusDir:
			b.statusHits.Add(1)
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if code := b.pageCode[p]; code != 0 {
				w.WriteHeader(code)
			}
			if body, ok := b.pageBody[p]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
			out := map[string]any{"values": []map[string]any{}, "page": p, "pagelen": bbStatusPage}
			if p >= 1 && p <= len(b.pages) {
				out["values"] = b.pages[p-1]
			}
			if p < len(b.pages) {
				out["next"] = srv.URL + "/next-trap"
			}
			body, _ := json.Marshal(out)
			_, _ = w.Write(body)
		case "/next-trap":
			b.nextHits.Add(1)
			_, _ = w.Write([]byte(`{"values":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bbCIPull(hash string) string {
	return `{"id":7,"state":"OPEN","source":{"branch":{"name":"phase/x"},"commit":{"hash":"` + hash + `"}}}`
}

func bbStatus(state, url string) map[string]any {
	return map[string]any{"state": state, "key": "ci-" + state, "url": url, "type": "build"}
}

func readBBCI(t *testing.T, b *bbCI) (CIResult, error) {
	t.Helper()
	srv := b.serve(t)
	opts := bbOpts(t, srv.URL)
	opts.PRNumber = 7
	return bitbucketCIStatus(opts, bbCISHA)
}

// TestBitbucketCIVerdicts: every status SUCCESSFUL is a pass; FAILED or
// STOPPED fails with its link whatever else ran; INPROGRESS is pending; none
// is none; a state dross does not know is an ask, never a pass.
func TestBitbucketCIVerdicts(t *testing.T) {
	ok, bad := bbStatus("SUCCESSFUL", "https://ci/ok"), bbStatus("FAILED", "https://ci/bad")
	for _, tc := range []struct {
		name     string
		statuses []map[string]any
		want     CIResult
	}{
		{"all successful", []map[string]any{ok, ok}, CIResult{Verdict: CIPass}},
		{"one failed", []map[string]any{ok, bad}, CIResult{Verdict: CIFail, Link: "https://ci/bad"}},
		{"stopped", []map[string]any{bbStatus("STOPPED", "https://ci/stop")}, CIResult{Verdict: CIFail, Link: "https://ci/stop"}},
		{"in progress", []map[string]any{ok, bbStatus("INPROGRESS", "")}, CIResult{Verdict: CIPending}},
		{"failed beside in progress", []map[string]any{bbStatus("INPROGRESS", ""), bad}, CIResult{Verdict: CIFail, Link: "https://ci/bad"}},
		{"none", []map[string]any{}, CIResult{Verdict: CINone}},
	} {
		got, err := readBBCI(t, &bbCI{pull: bbCIPull("0123456789ab"), pages: [][]map[string]any{tc.statuses}})
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	got, err := readBBCI(t, &bbCI{pull: bbCIPull("0123456789ab"), pages: [][]map[string]any{{ok, bbStatus("QUEUED", "")}}})
	if err != nil || got.Verdict != CIAsk || got.Reason == "" {
		t.Errorf("QUEUED: got %+v, %v; want ask with a reason", got, err)
	}
}

// TestBitbucketCIPaging: pages are read by number, a failure on page 2 still
// fails, more pages than the bound is an error, and `next` is never followed.
func TestBitbucketCIPaging(t *testing.T) {
	ok := bbStatus("SUCCESSFUL", "https://ci/ok")
	b := &bbCI{pull: bbCIPull("0123456789ab"), pages: [][]map[string]any{{ok}, {bbStatus("FAILED", "https://ci/p2")}}}
	got, err := readBBCI(t, b)
	if err != nil || got.Verdict != CIFail || got.Link != "https://ci/p2" {
		t.Errorf("failure on page 2: got %+v, %v; want fail linking it", got, err)
	}
	if n := b.nextHits.Load(); n != 0 {
		t.Errorf("the body's next URL was requested %d time(s)", n)
	}

	saved := prThreadMaxPages
	prThreadMaxPages = 2
	t.Cleanup(func() { prThreadMaxPages = saved })
	b = &bbCI{pull: bbCIPull("0123456789ab"), pages: [][]map[string]any{{ok}, {ok}, {ok}}}
	if got, err := readBBCI(t, b); err == nil {
		t.Errorf("still more after the bound: got %+v with a nil error", got)
	}
	if n := b.nextHits.Load(); n != 0 {
		t.Errorf("the body's next URL was requested %d time(s)", n)
	}
}

// TestBitbucketCIHeadAndFailures: a head the PR does not carry is pending with
// no status read; a failed read is an error, never none; a missing auth_user
// sends nothing.
func TestBitbucketCIHeadAndFailures(t *testing.T) {
	b := &bbCI{pull: bbCIPull("fedcba987654"), pages: [][]map[string]any{{bbStatus("SUCCESSFUL", "")}}}
	got, err := readBBCI(t, b)
	if err != nil || got.Verdict != CIPending || got.Reason == "" {
		t.Errorf("moved head: got %+v, %v; want pending with a reason", got, err)
	}
	if n := b.statusHits.Load(); n != 0 {
		t.Errorf("moved head: %d status read(s) went out", n)
	}

	for name, b := range map[string]*bbCI{
		"PR 500":            {pull: `{"error":{"message":"boom"}}`, pullCode: 500},
		"PR non-JSON":       {pull: `<html>no</html>`},
		"statuses 500":      {pull: bbCIPull("0123456789ab"), pageCode: map[int]int{1: 500}, pageBody: map[int]string{1: `{"error":{}}`}},
		"statuses non-JSON": {pull: bbCIPull("0123456789ab"), pageBody: map[int]string{1: `<html>no</html>`}},
		"statuses no list":  {pull: bbCIPull("0123456789ab"), pageBody: map[int]string{1: `{"page":1}`}},
	} {
		if got, err := readBBCI(t, b); err == nil || got != (CIResult{}) {
			t.Errorf("%s: got %+v, %v; want (CIResult{}, err)", name, got, err)
		}
	}

	b = &bbCI{pull: bbCIPull("0123456789ab")}
	srv := b.serve(t)
	opts := bbOpts(t, srv.URL)
	opts.PRNumber, opts.AuthUser = 7, ""
	if _, err := bitbucketCIStatus(opts, bbCISHA); err == nil {
		t.Error("a missing auth_user was accepted")
	}
	opts.AuthUser = "rivil"
	if _, err := bitbucketCIStatus(opts, "../../x"); err == nil {
		t.Error("a non-commit sha was spliced into the path")
	}
	if n := b.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out without auth_user or a commit id", n)
	}
}
