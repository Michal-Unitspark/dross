package ship

import (
	"errors"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

// adoPaddedProvider is azure-devops as a hand-edit might leave it: padded and
// mixed-case, which doctor accepts and so every dispatcher must too.
const adoPaddedProvider = " Azure-DevOps "

// TestAzureDevOpsDispatchRoutes: OpenPR, PostComment and GetPRStatus each
// route azure-devops to its Azure Repos call; "none" is not a backend; and an
// api_base off the allowlist is refused before any request leaves.
func TestAzureDevOpsDispatchRoutes(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)

	t.Run("OpenPR", func(t *testing.T) {
		m := newADOPRMock(t, adoCreated)
		opts := adoOpenOpts(m.Server)
		opts.Provider = adoPaddedProvider
		res, err := OpenPR(opts)
		if err != nil || res == nil || res.Number != 42 {
			t.Fatalf("got %+v, %v; want #42", res, err)
		}
		if posts := m.matching("POST", "/_apis/git/repositories/r/pullrequests"); len(posts) != 1 {
			t.Errorf("want one POST .../pullrequests, got %+v", m.calls)
		}
	})

	t.Run("PostComment", func(t *testing.T) {
		m := newADOPRMock(t, adoRoute{"POST", "/threads", 200, `{"id":1}`})
		opts := adoCommentOpts(m.URL+"/o", 7, "panel findings")
		opts.Provider = adoPaddedProvider
		if err := PostComment(opts); err != nil {
			t.Fatal(err)
		}
		if posts := m.matching("POST", "/_apis/git/repositories/r/pullRequests/7/threads"); len(posts) != 1 {
			t.Errorf("want one POST .../pullRequests/7/threads, got %+v", m.calls)
		}
	})

	t.Run("GetPRStatus", func(t *testing.T) {
		m := newADOPRMock(t, adoRoute{"GET", "/pullRequests/7", 200, `{"status":"completed","targetRefName":"refs/heads/release/x"}`})
		opts := adoTestOpts(m.Server)
		opts.Provider, opts.PRNumber = adoPaddedProvider, 7
		got, err := GetPRStatus(opts)
		if err != nil || !got.Merged || got.BaseRef != "release/x" {
			t.Errorf("got %+v, %v; want merged, base release/x", got, err)
		}
	})

	t.Run("none is not a backend", func(t *testing.T) {
		_, openErr := OpenPR(OpenOpts{Provider: "none"})
		commentErr := PostComment(CommentOpts{Provider: "none", PRNumber: 7, Body: "x"})
		_, statusErr := GetPRStatus(OpenOpts{Provider: "none", PRNumber: 7})
		for name, err := range map[string]error{"OpenPR": openErr, "PostComment": commentErr, "GetPRStatus": statusErr} {
			if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
				t.Errorf("%s(none) = %v, want an unsupported-provider error", name, err)
			}
		}
	})

	t.Run("off-allowlist api_base is refused before any request", func(t *testing.T) {
		m := newADOPRMock(t, adoCreated, adoRoute{"POST", "/threads", 200, `{}`},
			adoRoute{"GET", "/pullRequests/7", 200, `{"status":"active","targetRefName":"refs/heads/main"}`})
		// The policy derives from a dev.azure.com remote, so the mock's
		// 127.0.0.1 api_base is not on it.
		offList := hostallow.Derive(adoTestURL, nil)

		open := adoOpenOpts(m.Server)
		open.Provider, open.Hosts = adoPaddedProvider, offList
		_, openErr := OpenPR(open)

		comment := adoCommentOpts(m.URL+"/o", 7, "panel findings")
		comment.Provider, comment.Hosts = adoPaddedProvider, offList
		commentErr := PostComment(comment)

		status := adoTestOpts(m.Server)
		status.Provider, status.Hosts, status.PRNumber = adoPaddedProvider, offList, 7
		_, statusErr := GetPRStatus(status)

		for name, err := range map[string]error{"OpenPR": openErr, "PostComment": commentErr, "GetPRStatus": statusErr} {
			if !errors.Is(err, hostallow.ErrRefused) {
				t.Errorf("%s: want ErrRefused, got %v", name, err)
			}
		}
		if len(m.calls) != 0 {
			t.Errorf("%d request(s) reached an api_base off the allowlist", len(m.calls))
		}
	})
}

// TestAzureDeferredSurfacesFailClosed: the surfaces this phase defers to
// azure-devops-ship-loop answer azure-devops with an error, never an empty
// result — an empty comment list, PR head, account or dependent list would
// each read as a real answer — and none of them sends a request.
func TestAzureDeferredSurfacesFailClosed(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t)
	opts := adoTestOpts(m.Server)
	opts.Provider = "azure-devops"

	comments, err := ListPRComments(opts, 7)
	if err == nil || comments != nil {
		t.Errorf("ListPRComments = (%v, %v), want (nil, err)", comments, err)
	}
	head, err := PRHeadOf(opts, 7)
	if err == nil || head != (PRHead{}) {
		t.Errorf("PRHeadOf = (%+v, %v), want (zero, err)", head, err)
	}
	acct, err := AuthenticatedUser(opts)
	if err == nil || acct != (Account{}) {
		t.Errorf("AuthenticatedUser = (%+v, %v), want (zero, err)", acct, err)
	}
	prs, err := OpenPRsTargeting(opts, "main")
	if err == nil || prs != nil {
		t.Errorf("OpenPRsTargeting = (%v, %v), want (nil, err)", prs, err)
	}
	if len(m.calls) != 0 {
		t.Errorf("%d request(s) went out from a surface azure-devops does not wire yet", len(m.calls))
	}
}
