package ship

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

func adoCommentOpts(apiBase string, n int, body string) CommentOpts {
	return CommentOpts{
		URL:      adoTestURL,
		APIBase:  apiBase,
		AuthEnv:  adoTestEnv,
		Hosts:    hostallow.Derive(apiBase, nil),
		PRNumber: n,
		Body:     body,
	}
}

// TestPostAzureDevOpsThread: a comment is a new thread on the PR holding one
// root text comment, with no status — so it never gates the merge.
func TestPostAzureDevOpsThread(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t, adoRoute{"POST", "/threads", 200, `{"id":1}`})
	if err := postAzureDevOpsComment(adoCommentOpts(m.URL+"/o", 7, "panel findings")); err != nil {
		t.Fatal(err)
	}
	posts := m.matching("POST", "/threads")
	if len(posts) != 1 {
		t.Fatalf("want one thread POST, got %+v", m.calls)
	}
	p := posts[0]
	if want := "/o/p/_apis/git/repositories/r/pullRequests/7/threads"; p.path != want {
		t.Errorf("path = %q, want %q", p.path, want)
	}
	if got := p.query.Get("api-version"); got != "7.1" {
		t.Errorf("api-version = %q, want 7.1", got)
	}
	if _, ok := p.body["status"]; ok {
		t.Errorf("the thread carries a status (%v) — it would count under a comment-resolution policy", p.body["status"])
	}
	comments, _ := p.body["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("want one root comment, got %v", p.body["comments"])
	}
	root, _ := comments[0].(map[string]any)
	if root["content"] != "panel findings" || root["parentCommentId"] != float64(0) || root["commentType"] != float64(1) {
		t.Errorf("root comment = %v, want content, parentCommentId 0, commentType 1", root)
	}
}

// TestPostAzureDevOpsCommentScreened: a credential in the body is refused
// before the request is built.
func TestPostAzureDevOpsCommentScreened(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for _, tok := range []string{"glpat-" + strings.Repeat("xY9-", 5), "ghp_" + strings.Repeat("A1b2", 9)} {
		m := newADOPRMock(t, adoRoute{"POST", "/threads", 200, `{}`})
		err := postAzureDevOpsComment(adoCommentOpts(m.URL, 7, "see\n"+tok))
		if !isErrHit(err) {
			t.Errorf("want a secretscan refusal, got %v", err)
		}
		if len(m.calls) != 0 {
			t.Errorf("%d request(s) carried the credential out", len(m.calls))
		}
	}
}

// TestPostAzureDevOpsCommentErrorRedacted: a 403 that mirrors the credential
// back leaks neither the PAT nor its Basic form, and a sign-in page is an
// error, never "posted".
func TestPostAzureDevOpsCommentErrorRedacted(t *testing.T) {
	t.Setenv(shipCanaryEnv, shipCanary)
	srv := mirrorAuthServer(t, 403)
	opts := adoCommentOpts(srv.URL, 7, "panel findings")
	opts.AuthEnv = shipCanaryEnv
	err := postAzureDevOpsComment(opts)
	assertShipRedacted(t, err)
	if strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(":"+shipCanary))) {
		t.Errorf("the Basic credential survives: %s", err)
	}

	t.Setenv(adoTestEnv, adoTestTok)
	page := newADORecorder(t, http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`)
	if err := postAzureDevOpsComment(adoCommentOpts(page.URL, 7, "panel findings")); !errors.Is(err, errADOSignIn) {
		t.Errorf("203 sign-in page: want errADOSignIn, got %v", err)
	}
}

// TestPostAzureDevOpsCommentNeedsANumber: no PR number, no request.
func TestPostAzureDevOpsCommentNeedsANumber(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t)
	if err := postAzureDevOpsComment(adoCommentOpts(m.URL, 0, "x")); err == nil {
		t.Error("PR number 0 was accepted")
	}
	if len(m.calls) != 0 {
		t.Errorf("%d request(s) went out without a PR number", len(m.calls))
	}
}
