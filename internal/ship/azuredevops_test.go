package ship

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/secretscan"
)

const (
	adoTestEnv = "DROSS_ADO_TEST_PAT"
	adoTestTok = "ado-pat-0f3c9e1d-not-real"
	adoTestURL = "https://dev.azure.com/o/p/_git/r"
)

// adoRecorder is an httptest server that counts requests and keeps the last
// one's headers, query and escaped path.
type adoRecorder struct {
	*httptest.Server
	hits   atomic.Int32
	mu     sync.Mutex
	header http.Header
	query  map[string][]string
	path   string
}

func newADORecorder(t *testing.T, status int, body string) *adoRecorder {
	t.Helper()
	rec := &adoRecorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.header = r.Header.Clone()
		rec.query = r.URL.Query()
		rec.path = r.URL.EscapedPath()
		rec.mu.Unlock()
		rec.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(rec.Close)
	return rec
}

// adoTestOpts addresses the recorder as api_base, with an allowlist derived
// from it so the host check passes.
func adoTestOpts(srv *httptest.Server) OpenOpts {
	return OpenOpts{
		URL:     adoTestURL,
		APIBase: srv.URL + "/o",
		AuthEnv: adoTestEnv,
		Hosts:   hostallow.Derive(srv.URL, nil),
	}
}

// adoGet connects with opts and makes one GET against the repo endpoint.
func adoGet(t *testing.T, opts OpenOpts, suffix string) error {
	t.Helper()
	c, err := adoOpenConn(opts)
	if err != nil {
		return err
	}
	_, _, err = adoRequest(c, "GET", c.repoAPI()+suffix, nil)
	return err
}

func adoBasic(tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+tok))
}

// TestAzureDevOpsAuthHeader: exactly one credential header per call — Basic
// with an empty user for every scheme but bearer — and auth_user never reaches
// it.
func TestAzureDevOpsAuthHeader(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for _, tc := range []struct {
		name, scheme, user, want string
	}{
		{"empty scheme", "", "", adoBasic(adoTestTok)},
		{"private-token is ineffective", "private-token", "", adoBasic(adoTestTok)},
		{"padded BASIC", " BASIC ", "", adoBasic(adoTestTok)},
		{"auth_user ignored", "", "me", adoBasic(adoTestTok)},
		{"bearer", "bearer", "", "Bearer " + adoTestTok},
		{"padded Bearer", " Bearer ", "", "Bearer " + adoTestTok},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newADORecorder(t, 200, `{}`)
			opts := adoTestOpts(rec.Server)
			opts.AuthScheme, opts.AuthUser = tc.scheme, tc.user
			if err := adoGet(t, opts, "/pullRequests/1"); err != nil {
				t.Fatalf("request: %v", err)
			}
			if got := rec.header.Values("Authorization"); len(got) != 1 || got[0] != tc.want {
				t.Errorf("Authorization = %q, want exactly [%q]", got, tc.want)
			}
			if got := rec.header.Get("PRIVATE-TOKEN"); got != "" {
				t.Errorf("a second credential rode along: PRIVATE-TOKEN %q", got)
			}
			if tc.user != "" && rec.header.Get("Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte(tc.user+":"+adoTestTok)) {
				t.Error("auth_user leaked into the Basic credential")
			}
			if got := rec.header.Get("X-TFS-FedAuthRedirect"); got != "Suppress" {
				t.Errorf("X-TFS-FedAuthRedirect = %q, want Suppress", got)
			}
		})
	}
}

// TestAzureDevOpsRefusesBeforeTokenRead: an api_base the derived allowlist
// does not cover is refused before the env var is read — set or not — and the
// refusal carries neither the token nor the env name.
func TestAzureDevOpsRefusesBeforeTokenRead(t *testing.T) {
	rec := newADORecorder(t, 200, `{}`)
	opts := adoTestOpts(rec.Server)
	opts.Hosts = hostallow.Derive(adoTestURL, nil)

	for _, env := range []string{adoTestTok, ""} {
		t.Setenv(adoTestEnv, env)
		err := adoGet(t, opts, "/pullRequests/1")
		if !errors.Is(err, hostallow.ErrRefused) {
			t.Fatalf("env %q: want ErrRefused, got %v", env, err)
		}
		if msg := err.Error(); strings.Contains(msg, adoTestTok) || strings.Contains(msg, adoTestEnv) {
			t.Errorf("refusal names the token or its env var: %s", msg)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) reached the off-allowlist host", n)
	}
}

// TestAzureDevOpsAPIVersionOnEveryCall: every call carries exactly one
// api-version — 7.1 by default, the configured one otherwise — and a value that
// could splice into the query is refused before the env var is read.
func TestAzureDevOpsAPIVersionOnEveryCall(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for _, tc := range []struct{ set, want string }{
		{"", "7.1"},
		{"6.0", "6.0"},
		{"7.1-preview.1", "7.1-preview.1"},
	} {
		rec := newADORecorder(t, 200, `{}`)
		opts := adoTestOpts(rec.Server)
		opts.APIVersion = tc.set
		if err := adoGet(t, opts, "/pullrequests?searchCriteria.status=active"); err != nil {
			t.Fatalf("api_version %q: %v", tc.set, err)
		}
		if got := rec.query["api-version"]; len(got) != 1 || got[0] != tc.want {
			t.Errorf("api_version %q: api-version = %q, want exactly [%q]", tc.set, got, tc.want)
		}
		if got := rec.query["searchCriteria.status"]; len(got) != 1 || got[0] != "active" {
			t.Errorf("api_version %q: the endpoint's own query was lost: %v", tc.set, rec.query)
		}
	}

	t.Setenv(adoTestEnv, "")
	rec := newADORecorder(t, 200, `{}`)
	for _, bad := range []string{"7.1&x=y", "7.1#", " ", "../", "7", "7.1-beta"} {
		opts := adoTestOpts(rec.Server)
		opts.APIVersion = bad
		err := adoGet(t, opts, "/pullRequests/1")
		if err == nil || !strings.Contains(err.Error(), "[remote].api_version") {
			t.Errorf("api_version %q: want an [remote].api_version error ahead of the env read, got %v", bad, err)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out with a bad api_version", n)
	}
}

// TestADOSignInIsAuthFailure: Azure DevOps answers a rejected credential with
// a 203 sign-in page or a 302 to one. Both are auth errors, never a success,
// and the redirect is not followed — its target sees nothing.
func TestADOSignInIsAuthFailure(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)

	page := newADORecorder(t, http.StatusNonAuthoritativeInfo, `<html><body>Sign In</body></html>`)
	if err := adoGet(t, adoTestOpts(page.Server), "/pullRequests/1"); !errors.Is(err, errADOSignIn) {
		t.Errorf("203 sign-in page: want errADOSignIn, got %v", err)
	}

	target := newADORecorder(t, 200, `{}`)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/_signin", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	opts := adoTestOpts(redirect)
	opts.Hosts = hostallow.Derive(redirect.URL, []string{target.URL})
	if err := adoGet(t, opts, "/pullRequests/1"); !errors.Is(err, errADOSignIn) {
		t.Errorf("302: want errADOSignIn, got %v", err)
	}
	if n := target.hits.Load(); n != 0 {
		t.Errorf("the redirect was followed: its target saw %d request(s)", n)
	}
}

// TestADOErrorsScrubTheCredential: a 401/403 that mirrors the credential back
// leaks neither the raw token nor its base64(":"+token) Basic form, for either
// scheme.
func TestADOErrorsScrubTheCredential(t *testing.T) {
	t.Setenv(shipCanaryEnv, shipCanary)
	encoded := base64.StdEncoding.EncodeToString([]byte(":" + shipCanary))
	for _, status := range []int{401, 403} {
		for _, scheme := range []string{"", "bearer"} {
			srv := mirrorAuthServer(t, status)
			err := adoGet(t, OpenOpts{
				URL: adoTestURL, APIBase: srv.URL, AuthEnv: shipCanaryEnv,
				AuthScheme: scheme, Hosts: hostallow.Derive(srv.URL, nil),
			}, "/pullRequests/1")
			assertShipRedacted(t, err)
			if strings.Contains(err.Error(), encoded) {
				t.Errorf("%d %q: the Basic credential survives: %s", status, scheme, err)
			}
		}
	}
}

// TestADOBodyIsScreened: a credential in a request body is refused before the
// request is built.
func TestADOBodyIsScreened(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 201, `{}`)
	c, err := adoOpenConn(adoTestOpts(rec.Server))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"description": "see ghp_" + strings.Repeat("A1b2", 9)}
	if _, _, err := adoRequest(c, "POST", c.repoAPI()+"/pullrequests", body); !isErrHit(err) {
		t.Errorf("want a secretscan refusal, got %v", err)
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) carried the credential out", n)
	}
}

func isErrHit(err error) bool {
	_, ok := secretscan.AsErrHit(err)
	return ok
}

// TestADORepoEndpointEscapesOnce: decoded names are escaped exactly once —
// in the endpoint and on the wire.
func TestADORepoEndpointEscapesOnce(t *testing.T) {
	const want = "https://dev.azure.com/o/My%20Project/_apis/git/repositories/My%20Repo"
	if got := adoRepoEndpoint("https://dev.azure.com/o/", "My Project", "My Repo"); got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
	if got := adoRepoEndpoint("https://dev.azure.com/o", "a#b", "c?d"); !strings.HasSuffix(got, "/a%23b/_apis/git/repositories/c%3Fd") {
		t.Errorf("# and ? must be escaped: %q", got)
	}

	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 200, `{}`)
	opts := adoTestOpts(rec.Server)
	opts.URL = "https://dev.azure.com/o/My%20Project/_git/My%20Repo"
	if err := adoGet(t, opts, "/pullRequests/1"); err != nil {
		t.Fatal(err)
	}
	if want := "/o/My%20Project/_apis/git/repositories/My%20Repo/pullRequests/1"; rec.path != want {
		t.Errorf("wire path = %q, want %q", rec.path, want)
	}
}

// TestADOConfigErrorsBeforeAnyRequest: a missing api_base or auth_env names
// the key to set, and nothing is sent.
func TestADOConfigErrorsBeforeAnyRequest(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 200, `{}`)
	for key, mutate := range map[string]func(*OpenOpts){
		"[remote].api_base": func(o *OpenOpts) { o.APIBase = "" },
		"[remote].auth_env": func(o *OpenOpts) { o.AuthEnv = "" },
		"[remote].url":      func(o *OpenOpts) { o.URL = "https://dev.azure.com/o/p/r" },
	} {
		opts := adoTestOpts(rec.Server)
		mutate(&opts)
		if err := adoGet(t, opts, "/pullRequests/1"); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("want an error naming %s, got %v", key, err)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out on incomplete config", n)
	}
}

// TestADONon2xxIsAnError: a non-2xx status is an error carrying the status
// and the (scrubbed) body, so no caller can mistake it for a result.
func TestADONon2xxIsAnError(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 409, `{"message":"TF401179: An active pull request already exists"}`)
	err := adoGet(t, adoTestOpts(rec.Server), "/pullrequests")
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "TF401179") {
		t.Errorf("want HTTP 409 with the body, got %v", err)
	}
}
