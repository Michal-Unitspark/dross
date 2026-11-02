package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Rivil/dross/internal/prtriage"
)

// --- Azure DevOps PR threads end to end (azure-devops-ship-loop c-1..c-3) ---

const (
	// adoSelfID is the PAT's identity as connectionData spells it; the
	// threads endpoint reports the same id in lower case.
	adoSelfID    = "5E2D7C1A-9B3F-4C8E-A1D2-3F4E5A6B7C8D"
	adoSelfLogin = "dross-bot@corp.example"
	adoRevID     = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	adoRevLogin  = "reviewer@corp.example"
)

// adoPRForge is a stateful Azure DevOps Server collection holding PR #7: its
// threads, the PAT's identity, and a POST that appends a thread authored by
// that identity — so a posted reply is read back exactly as ADO lists it.
type adoPRForge struct {
	*httptest.Server
	mu           sync.Mutex
	pr           string
	threads      []map[string]any
	threadsReads int
	posts        int
	lastPost     string
}

func newADOPRForge(t *testing.T, pr string, threads ...map[string]any) *adoPRForge {
	t.Helper()
	f := &adoPRForge{pr: pr, threads: threads}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.EscapedPath()
		switch {
		case r.Method == "GET" && strings.HasSuffix(p, "/pullRequests/7/threads"):
			f.threadsReads++
			b, _ := json.Marshal(map[string]any{"value": f.threads, "count": len(f.threads)})
			_, _ = w.Write(b)
		case r.Method == "POST" && strings.HasSuffix(p, "/pullRequests/7/threads"):
			f.posts++
			var in struct {
				Comments []struct {
					Content string `json:"content"`
				} `json:"comments"`
			}
			raw, _ := io.ReadAll(r.Body)
			if json.Unmarshal(raw, &in) != nil || len(in.Comments) != 1 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.lastPost = in.Comments[0].Content
			id := len(f.threads) + 1
			f.threads = append(f.threads, adoThreadJSON(id, nil, adoCommentJSON(1, "text", strings.ToLower(adoSelfID), adoSelfLogin, f.lastPost, false)))
			_, _ = fmt.Fprintf(w, `{"id":%d}`, id)
		case r.Method == "GET" && strings.HasSuffix(p, "/pullRequests/7"):
			_, _ = w.Write([]byte(f.pr))
		case r.Method == "GET" && strings.HasSuffix(p, "/_apis/connectionData"):
			_, _ = fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"properties":{"Account":{"$type":"System.String","$value":%q}}}}`, adoSelfID, adoSelfLogin)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *adoPRForge) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.threadsReads
}

// adoPRJSON is PR #7 as ADO returns it; extra adds fields.
func adoPRJSON(status, extra string) string {
	return `{"pullRequestId":7,"status":"` + status + `","sourceRefName":"refs/heads/phase/x","targetRefName":"refs/heads/main"` + extra + `}`
}

func adoThreadJSON(id int, ctx map[string]any, comments ...map[string]any) map[string]any {
	th := map[string]any{"id": id, "isDeleted": false, "comments": comments, "status": "active"}
	if ctx != nil {
		th["threadContext"] = ctx
	}
	return th
}

func adoCommentJSON(id int, typ, authorID, login, content string, deleted bool) map[string]any {
	return map[string]any{
		"id": id, "parentCommentId": 0, "content": content, "commentType": typ, "isDeleted": deleted,
		"author": map[string]any{"id": authorID, "uniqueName": login, "displayName": login},
	}
}

func adoReviewer(id int, content string) map[string]any {
	return adoCommentJSON(id, "text", adoRevID, adoRevLogin, content, false)
}

// adoReviewThreads is a reviewer's conversation comment, a reviewer's inline
// comment on /src/a.go line 12, a vote system thread, a deleted comment and
// dross's own earlier reply.
func adoReviewThreads() []map[string]any {
	return []map[string]any{
		adoThreadJSON(1, nil, adoReviewer(1, "why is this unbounded?")),
		adoThreadJSON(2, map[string]any{"filePath": "/src/a.go", "rightFileStart": map[string]any{"line": 12, "offset": 1}},
			adoReviewer(1, "rename this")),
		adoThreadJSON(3, nil, adoCommentJSON(1, "system", adoRevID, adoRevLogin, "Reviewer voted 10", false)),
		adoThreadJSON(4, nil, adoCommentJSON(1, "text", adoRevID, adoRevLogin, "never mind", true)),
		adoThreadJSON(5, nil, adoCommentJSON(1, "text", strings.ToLower(adoSelfID), adoSelfLogin, prtriage.ReplyMarker+"\n\nNot acted on, with the reason for each:\n", false)),
	}
}

// adoPRRepo is prRepo on phase/x with [remote] set through `dross project
// set` to an Azure DevOps Server collection served by f.
func adoPRRepo(t *testing.T, f *adoPRForge) string {
	t.Helper()
	dir := prRepo(t)
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "x", "spec.toml"), "[phase]\nid = \"x\"\ntitle = \"X\"\n\n[[criteria]]\nid = \"c-1\"\ntext = \"does a thing\"\n")
	t.Setenv("MOCK_ADO_PAT", adoShipPAT)
	for _, set := range [][]string{
		{"set", "remote.provider", "azure-devops"},
		{"set", "remote.url", f.URL + "/coll/P/_git/R"},
		{"set", "remote.api_base", f.URL + "/coll"},
		{"set", "remote.api_version", "6.0"},
		{"set", "remote.auth_env", "MOCK_ADO_PAT"},
	} {
		if err := runCmd(t, Project(), set...); err != nil {
			t.Fatalf("project %v: %v", set, err)
		}
	}
	gitCommit(t, dir, "chore(dross): azure devops remote")
	return dir
}

// TestPRCommentsAzureDevOps: through `dross pr comments`, the reviewer's two
// human comments are listed — the inline one at its path and line — while the
// vote, the deleted comment and dross's own reply are not, though the reply's
// author id differs from connectionData's in case. A reviewer copying the
// reply marker is still listed, and dross's own /dross-review comment splits
// into its findings.
func TestPRCommentsAzureDevOps(t *testing.T) {
	f := newADOPRForge(t, adoPRJSON("active", ""), adoReviewThreads()...)
	adoPRRepo(t, f)
	out, err := runPR(t, "comments", "7")
	if err != nil {
		t.Fatalf("pr comments: %v\n%s", err, out)
	}
	for _, want := range []string{
		"c100001 · conversation · @" + adoRevLogin,
		"i200001 · inline · @" + adoRevLogin,
		" · src/a.go:12 · ",
		"PR #7 (phase/x): 2 of 2 comments need a verdict",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"c300001", "c400001", "c500001", "voted", "never mind", prtriage.ReplyMarker} {
		if strings.Contains(out, gone) {
			t.Errorf("output lists %q:\n%s", gone, out)
		}
	}

	review := "## /dross-review — four-lens panel\n\n- **FLAG** — the loop is unbounded `src/a.go:3`\n- **NOTE** — a name could be clearer\n"
	threads := append(adoReviewThreads(),
		adoThreadJSON(6, nil, adoReviewer(1, prtriage.ReplyMarker+"\n\nI copied the marker")),
		adoThreadJSON(7, nil, adoCommentJSON(1, "text", strings.ToLower(adoSelfID), adoSelfLogin, review, false)),
	)
	f = newADOPRForge(t, adoPRJSON("active", ""), threads...)
	adoPRRepo(t, f)
	out, err = runPR(t, "comments", "7")
	if err != nil {
		t.Fatalf("pr comments: %v\n%s", err, out)
	}
	for _, want := range []string{
		"c600001 · conversation · @" + adoRevLogin,
		"c700001#1 · conversation · @" + adoSelfLogin,
		"c700001#2 · conversation · @" + adoSelfLogin,
		" · src/a.go:3 · ",
		"PR #7 (phase/x): 5 of 5 comments need a verdict",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "c700001 · ") || strings.Contains(out, "c500001") {
		t.Errorf("dross's own review was listed whole, or its reply listed:\n%s", out)
	}
}

// TestPRResolveAzureDevOpsID: an ADO id as listed is a triage id — resolving
// it is accepted and the saved record validates.
func TestPRResolveAzureDevOpsID(t *testing.T) {
	f := newADOPRForge(t, adoPRJSON("active", ""), adoReviewThreads()...)
	dir := adoPRRepo(t, f)
	out, err := runPR(t, "resolve", "7", "i200001", "--seen", prtriage.Digest("rename this"),
		"--reject", "--reason", "the name matches the package's", "--at", "x.go:1")
	if err != nil {
		t.Fatalf("pr resolve: %v\n%s", err, out)
	}
	rec := loadTriage(t, dir)
	if len(rec.Resolution) != 1 || rec.Resolution[0].ID != "i200001" || rec.Resolution[0].PR != 7 {
		t.Fatalf("saved record = %+v", rec.Resolution)
	}
	if errs := prtriage.Validate(rec, prtriage.Refs{}); len(errs) > 0 {
		t.Errorf("the saved %s does not validate: %v", prtriage.File, errs)
	}
}

// TestPRCommentsAzureDevOpsNotAPhasePR: a fork's phase/ branch is no phase PR
// — refused before any thread is read. A completed or abandoned PR can still
// be listed (listing is read-only, as on every provider), but a verdict on it
// is refused and nothing is saved.
func TestPRCommentsAzureDevOpsNotAPhasePR(t *testing.T) {
	f := newADOPRForge(t, adoPRJSON("active", `,"forkSource":{"name":"refs/heads/phase/x"}`), adoReviewThreads()...)
	adoPRRepo(t, f)
	if out, err := runPR(t, "comments", "7"); err == nil || !strings.Contains(err.Error(), "another repository") {
		t.Errorf("a fork's phase/x: got %v\n%s", err, out)
	}
	if n := f.reads(); n != 0 {
		t.Errorf("a fork's PR had its threads read %d time(s)", n)
	}

	// Completed and abandoned are both closed (c-2).
	for _, status := range []string{"completed", "abandoned"} {
		f = newADOPRForge(t, adoPRJSON(status, ""), adoReviewThreads()...)
		dir := adoPRRepo(t, f)
		before := readOrEmpty(t, phaseFile(dir, prtriage.File))
		_, err := runPR(t, "resolve", "7", "i200001", "--seen", prtriage.Digest("rename this"),
			"--reject", "--reason", "r", "--at", "x.go:1")
		if err == nil || !strings.Contains(err.Error(), "closed") {
			t.Errorf("a verdict on a %s PR: got %v, want a refusal naming it closed", status, err)
		}
		if after := readOrEmpty(t, phaseFile(dir, prtriage.File)); after != before {
			t.Errorf("a refused verdict on a %s PR wrote %s:\n%s", status, prtriage.File, after)
		}
	}
}

// TestPRReplyAzureDevOps: with the draft approved, `dross pr reply --post`
// posts one thread opening with ReplyMarker; ADO lists it under the PAT's
// own id, so the next `dross pr comments` treats it as dross's own reply.
func TestPRReplyAzureDevOps(t *testing.T) {
	f := newADOPRForge(t, adoPRJSON("active", ""), adoReviewThreads()...)
	dir := adoPRRepo(t, f)
	if out, err := runPR(t, "resolve", "7", "c100001", "--seen", prtriage.Digest("why is this unbounded?"),
		"--reject", "--reason", "it is bounded by maxPages", "--at", "x.go:1"); err != nil {
		t.Fatalf("pr resolve: %v\n%s", err, out)
	}
	body, ok := prtriage.ReplyBody(loadTriage(t, dir), 7)
	if !ok {
		t.Fatal("no reply drafted")
	}
	approve(t, dir, 7, prtriage.ReplyDigest(body))
	if out, err := runPR(t, "reply", "7", "--post"); err != nil {
		t.Fatalf("pr reply --post: %v\n%s", err, out)
	}
	f.mu.Lock()
	posts, first := f.posts, strings.SplitN(f.lastPost, "\n", 2)[0]
	f.mu.Unlock()
	if posts != 1 || first != prtriage.ReplyMarker {
		t.Fatalf("%d post(s), first line %q; want one thread opening with the marker", posts, first)
	}

	out, err := runPR(t, "comments", "7", "--all")
	if err != nil {
		t.Fatalf("pr comments: %v\n%s", err, out)
	}
	// The posted reply is thread 6: listed, it would be c600001 and a third item.
	if strings.Contains(out, "c600001") {
		t.Errorf("dross's posted reply is listed as a comment:\n%s", out)
	}
	if !strings.Contains(out, "PR #7 (phase/x): 1 of 2 comments need a verdict") {
		t.Errorf("want only i200001 left to triage:\n%s", out)
	}
}
