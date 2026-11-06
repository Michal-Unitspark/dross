package ship

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/hostallow"
)

// TestCIMergeDispatchRoutes: a padded provider reaches its backend — Azure
// DevOps reads builds and PATCHes the PR, Bitbucket reads the head's statuses
// and POSTs the merge.
func TestCIMergeDispatchRoutes(t *testing.T) {
	t.Run("azure-devops ci", func(t *testing.T) {
		t.Setenv(adoTestEnv, adoTestTok)
		m := adoCIMock(t, adoHeadPR, adoBuilds(adoBuildRec(1, "completed", "succeeded", adoHeadSHA, adoRepoGUID)), adoEvals())
		opts := adoTestOpts(m.Server)
		opts.Provider, opts.PRNumber = " Azure-DevOps ", 7
		got, err := CIStatus(opts, adoHeadSHA)
		if err != nil || got.Verdict != CIPass {
			t.Errorf("got %+v, %v; want pass", got, err)
		}
		if n := len(m.matching("GET", "/_apis/build/builds")); n != 1 {
			t.Errorf("%d builds read(s), want 1", n)
		}
	})
	t.Run("azure-devops merge", func(t *testing.T) {
		t.Setenv(adoTestEnv, adoTestTok)
		fastADOMerge(t, 5)
		m := newADOMergeMock(t, adoMergePR("active", "succeeded", adoHeadSHA, false), adoMergePR("completed", "succeeded", adoHeadSHA, false))
		opts := adoMergeOpts(m)
		opts.Provider = " Azure-DevOps "
		if _, err := MergePR(opts, adoHeadSHA); err != nil {
			t.Errorf("merge: %v", err)
		}
		if n := m.count("PATCH", "/pullrequests/7"); n != 1 {
			t.Errorf("%d PATCH(es) of .../pullrequests/7, want 1", n)
		}
	})
	t.Run("bitbucket ci", func(t *testing.T) {
		b := &bbCI{pull: bbCIPull("0123456789ab"), pages: [][]map[string]any{{bbStatus("SUCCESSFUL", "")}}}
		srv := b.serve(t)
		opts := bbOpts(t, srv.URL)
		opts.Provider, opts.PRNumber = " Bitbucket ", 7
		got, err := CIStatus(opts, bbCISHA)
		if err != nil || got.Verdict != CIPass {
			t.Errorf("got %+v, %v; want pass", got, err)
		}
		if n := b.statusHits.Load(); n != 1 {
			t.Errorf("%d statuses read(s), want 1", n)
		}
	})
	t.Run("bitbucket merge", func(t *testing.T) {
		fastBBMerge(t, 5)
		b := &bbMerge{pull: bbOpenPull, mergeCode: 200, mergeBody: bbMergedBody}
		srv := b.serve(t)
		opts := bbOpts(t, srv.URL)
		opts.Provider, opts.PRNumber = " Bitbucket ", 7
		if _, err := MergePR(opts, bbCISHA); err != nil {
			t.Errorf("merge: %v", err)
		}
		if posts := b.posts(); len(posts) != 1 || posts[0].path != bbMergePath {
			t.Errorf("want one POST to .../merge, got %+v", posts)
		}
	})
}

// cimergeCounter is a server that counts every request and answers none of
// them usefully: reaching it at all is what is measured.
func cimergeCounter(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// cimergeOpts points provider p's opts at srv with a credential set: an
// Azure Repos remote for azure-devops, a Bitbucket one otherwise.
func cimergeOpts(t *testing.T, p string, srv *httptest.Server) OpenOpts {
	t.Helper()
	t.Setenv(adoTestEnv, adoTestTok)
	opts := adoTestOpts(srv)
	opts.Provider, opts.PRNumber, opts.AuthUser = p, 7, "rivil"
	if configenum.Normalize(p) != "azure-devops" {
		opts.URL = "https://bitbucket.org/acme/widgets"
		opts.APIBase = srv.URL + "/2.0"
	}
	return opts
}

// TestCIMergeUnsupported: the providers whose ship.md recipe watches CI and
// merges get the Unsupported sentinels, naming that recipe, with nothing sent
// and no gh spawned; an unknown provider gets an error that is neither.
func TestCIMergeUnsupported(t *testing.T) {
	srv, hits := cimergeCounter(t)
	spy, _ := stubGHOutput(t, "[]", "", 0)
	for _, p := range []string{"github", "gitlab", "forgejo", "gitea", " GitHub "} {
		opts := cimergeOpts(t, p, srv)
		if _, err := CIStatus(opts, bbCISHA); !errors.Is(err, ErrCIUnsupported) || !strings.Contains(err.Error(), "ship.md recipe") {
			t.Errorf("%q ci: got %v, want ErrCIUnsupported naming the ship.md recipe", p, err)
		}
		if _, err := MergePR(opts, bbCISHA); !errors.Is(err, ErrMergeUnsupported) || !strings.Contains(err.Error(), "ship.md recipe") {
			t.Errorf("%q merge: got %v, want ErrMergeUnsupported naming the ship.md recipe", p, err)
		}
	}
	for _, p := range []string{"launchpad", "", "none"} {
		opts := cimergeOpts(t, p, srv)
		if _, err := CIStatus(opts, bbCISHA); err == nil || errors.Is(err, ErrCIUnsupported) || errors.Is(err, ErrMergeUnsupported) {
			t.Errorf("%q ci: got %v, want a plain error", p, err)
		}
		if _, err := MergePR(opts, bbCISHA); err == nil || errors.Is(err, ErrCIUnsupported) || errors.Is(err, ErrMergeUnsupported) {
			t.Errorf("%q merge: got %v, want a plain error", p, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out", n)
	}
	if n := spy.spawns(); n != 0 {
		t.Errorf("gh was spawned %d time(s)", n)
	}
}

// TestCIMergeRefusesOffAllowlistAPIBase: each new entry point checks the host
// before it reads the credential — with the env unset the refusal is still
// the policy's, and with it set the token never reaches the error or the host.
func TestCIMergeRefusesOffAllowlistAPIBase(t *testing.T) {
	srv, hits := cimergeCounter(t)
	for _, tc := range []struct {
		name string
		call func(OpenOpts) error
	}{
		{"azure-devops ci", func(o OpenOpts) error { _, err := CIStatus(o, adoHeadSHA); return err }},
		{"azure-devops merge", func(o OpenOpts) error { _, err := MergePR(o, adoHeadSHA); return err }},
		{"bitbucket ci", func(o OpenOpts) error { _, err := CIStatus(o, bbCISHA); return err }},
		{"bitbucket merge", func(o OpenOpts) error { _, err := MergePR(o, bbCISHA); return err }},
	} {
		provider, _, _ := strings.Cut(tc.name, " ")
		for _, set := range []bool{false, true} {
			opts := cimergeOpts(t, provider, srv)
			opts.Hosts = hostallow.Derive(opts.URL, nil)
			if set {
				t.Setenv(guardEnv, guardSentinel)
			} else {
				t.Setenv(guardEnv, "")
				_ = os.Unsetenv(guardEnv)
			}
			opts.AuthEnv = guardEnv
			err := tc.call(opts)
			if !errors.Is(err, hostallow.ErrRefused) {
				t.Errorf("%s (env set %v): got %v, want ErrRefused", tc.name, set, err)
			}
			if err != nil && strings.Contains(err.Error(), guardSentinel) {
				t.Errorf("%s: the refusal echoed the token: %v", tc.name, err)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the off-allowlist host received %d request(s)", n)
	}
}

// TestCIMergeDispatchSet: by behaviour, over every ShipProviders entry,
// exactly azure-devops and bitbucket reach a forge from CIStatus and MergePR,
// and every other entry gets the Unsupported sentinel.
func TestCIMergeDispatchSet(t *testing.T) {
	fastADOMerge(t, 1)
	fastBBMerge(t, 1)
	spy, _ := stubGHOutput(t, "[]", "", 0)
	reached := map[string]bool{}
	for _, p := range configenum.ShipProviders.Values() {
		srv, hits := cimergeCounter(t)
		opts := cimergeOpts(t, p, srv)
		_, ciErr := CIStatus(opts, bbCISHA)
		ciHits := hits.Swap(0)
		_, mergeErr := MergePR(opts, bbCISHA)
		mergeHits := hits.Load()
		switch {
		case ciHits > 0 && mergeHits > 0:
			reached[p] = true
		case ciHits > 0 || mergeHits > 0:
			t.Errorf("%s: ci sent %d, merge sent %d — the two must route alike", p, ciHits, mergeHits)
		default:
			if !errors.Is(ciErr, ErrCIUnsupported) || !errors.Is(mergeErr, ErrMergeUnsupported) {
				t.Errorf("%s: sent nothing but got %v / %v, want the Unsupported sentinels", p, ciErr, mergeErr)
			}
		}
	}
	if len(reached) != 2 || !reached["azure-devops"] || !reached["bitbucket"] {
		t.Errorf("reached a forge: %v, want exactly azure-devops and bitbucket", reached)
	}
	if n := spy.spawns(); n != 0 {
		t.Errorf("gh was spawned %d time(s)", n)
	}
}
