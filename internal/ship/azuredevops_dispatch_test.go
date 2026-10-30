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

// TestAzureDevOpsReadSurfacesRoute: the PR-thread, head, caller and
// dependents reads each route azure-devops — padded and mixed-case — to its
// Azure Repos call; a PR number below 1 still sends nothing, and Bitbucket's
// dependents lookup is still an announced skip.
func TestAzureDevOpsReadSurfacesRoute(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t,
		adoRoute{"GET", "/pullRequests/7/threads", 200, adoThreads(adoThread(1, "", adoText(1, "hi")))},
		adoRoute{"GET", "/pullRequests/7", 200, adoPRBody("active", "refs/heads/phase/x", "")},
		adoRoute{"GET", "/_apis/connectionData", 200, adoConnectionData(adoGUID1, "me@corp.com")},
		adoRoute{"GET", "/pullrequests", 200, adoList(adoListPR(3, "active", "refs/heads/phase/x", "refs/heads/milestone/v1", ""))},
	)
	opts := adoTestOpts(m.Server)
	opts.Provider = adoPaddedProvider

	comments, err := ListPRComments(opts, 7)
	if err != nil || len(comments) != 1 {
		t.Errorf("ListPRComments = (%+v, %v), want one comment", comments, err)
	}
	if got := m.matching("GET", "/_apis/git/repositories/r/pullRequests/7/threads"); len(got) != 1 {
		t.Errorf("ListPRComments: want one GET .../pullRequests/7/threads, got %+v", m.calls)
	}

	head, err := PRHeadOf(opts, 7)
	if err != nil || head.Ref != "phase/x" || !head.Open {
		t.Errorf("PRHeadOf = (%+v, %v), want phase/x open", head, err)
	}
	if got := m.matching("GET", "/_apis/git/repositories/r/pullRequests/7"); len(got) != 2 {
		t.Errorf("PRHeadOf: want a GET .../pullRequests/7 beside the threads read, got %+v", m.calls)
	}

	acct, err := AuthenticatedUser(opts)
	if err != nil || acct.ID != adoGUID1 {
		t.Errorf("AuthenticatedUser = (%+v, %v), want %s", acct, err, adoGUID1)
	}
	if got := m.matching("GET", "/o/_apis/connectionData"); len(got) != 1 {
		t.Errorf("AuthenticatedUser: want one GET <base>/_apis/connectionData, got %+v", m.calls)
	}

	prs, err := OpenPRsTargeting(opts, "milestone/v1")
	if err != nil || len(prs) != 1 || prs[0].Number != 3 {
		t.Errorf("OpenPRsTargeting = (%+v, %v), want #3", prs, err)
	}
	lists := m.matching("GET", "/_apis/git/repositories/r/pullrequests")
	var targeted int
	for _, c := range lists {
		if c.query.Get("searchCriteria.targetRefName") == "refs/heads/milestone/v1" {
			targeted++
		}
	}
	if targeted != 1 {
		t.Errorf("OpenPRsTargeting: want one GET .../pullrequests with searchCriteria.targetRefName, got %+v", lists)
	}

	before := len(m.calls)
	for _, n := range []int{0, -1} {
		if got, err := ListPRComments(opts, n); err == nil || got != nil {
			t.Errorf("ListPRComments(%d) = (%v, %v), want (nil, err)", n, got, err)
		}
		if got, err := PRHeadOf(opts, n); err == nil || got != (PRHead{}) {
			t.Errorf("PRHeadOf(%d) = (%+v, %v), want (zero, err)", n, got, err)
		}
	}
	if len(m.calls) != before {
		t.Errorf("%d request(s) went out for a PR number below 1", len(m.calls)-before)
	}

	bb := opts
	bb.Provider = "bitbucket"
	if got, err := OpenPRsTargeting(bb, "milestone/v1"); !errors.Is(err, ErrBasePRLookupUnsupported) || got != nil {
		t.Errorf("bitbucket OpenPRsTargeting = (%v, %v), want ErrBasePRLookupUnsupported", got, err)
	}
	if len(m.calls) != before {
		t.Errorf("bitbucket's dependents lookup sent %d request(s)", len(m.calls)-before)
	}
}
