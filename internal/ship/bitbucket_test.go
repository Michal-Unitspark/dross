package ship

import (
	"encoding/base64"
	"encoding/json"
	"github.com/Rivil/dross/internal/hostallow"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
)

// wantBasic is the exact header Bitbucket's credential produces. Anything else
// — a PRIVATE-TOKEN, an "Authorization: token", a bare token — 401s.
func wantBasic(user, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

func TestBBRepoRef(t *testing.T) {
	tests := []struct {
		url             string
		workspace, slug string
		wantErr         bool
	}{
		{"https://bitbucket.org/acme/widget", "acme", "widget", false},
		{"https://bitbucket.org/acme/widget.git", "acme", "widget", false},
		{"https://bitbucket.org/acme/widget/", "acme", "widget", false},
		{"https://bitbucket.org/acme", "", "", true},
		{"not-a-url", "", "", true},
	}
	for _, tc := range tests {
		ws, slug, err := bbRepoRef(tc.url)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", tc.url)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.url, err)
		}
		if ws != tc.workspace || slug != tc.slug {
			t.Errorf("%q: got %q/%q want %q/%q", tc.url, ws, slug, tc.workspace, tc.slug)
		}
	}
}

// TestPRStatusBitbucketPopulatesBaseRef proves a MERGED payload reports its
// destination.branch.name as BaseRef, not an empty string — Bitbucket nests
// the branch name where GitHub/Forgejo keep it flat.
func TestPRStatusBitbucketPopulatesBaseRef(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")

	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"MERGED","destination":{"branch":{"name":"main"}}}`))
	}))
	t.Cleanup(server.Close)

	status, err := bitbucketPRStatus(OpenOpts{
		Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv: "MOCK_BB_TOKEN", AuthUser: "wsuser", PRNumber: 42,
	})
	if err != nil {
		t.Fatalf("bitbucketPRStatus: %v", err)
	}
	if !status.Merged || status.BaseRef != "main" {
		t.Errorf("got %+v, want {Merged:true BaseRef:main}", status)
	}
	if want := "/repositories/acme/widget/pullrequests/42"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if want := wantBasic("wsuser", "secret"); gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestOpenBitbucketPRHappyPath(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")

	var (
		created  bool
		gotPath  string
		gotBody  map[string]any
		gotAuth  string
		strayHdr []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if v := r.Header.Get("PRIVATE-TOKEN"); v != "" {
			strayHdr = append(strayHdr, "PRIVATE-TOKEN: "+v)
		}
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/pullrequests") && r.Method == "POST":
			created = true
			gotPath = r.URL.Path
			_ = json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":42,"links":{"html":{"href":"https://bitbucket.org/acme/widget/pull-requests/42"}}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	res, err := OpenPR(OpenOpts{
		Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv: "MOCK_BB_TOKEN", AuthUser: "wsuser",
		HeadBranch: "phase/x", BaseBranch: "main",
		Title: "My Title", Body: "the body",
	})
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if !created {
		t.Fatal("no pullrequests POST was made")
	}
	if want := "/repositories/acme/widget/pullrequests"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != wantBasic("wsuser", "secret") {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantBasic("wsuser", "secret"))
	}
	if len(strayHdr) > 0 {
		t.Errorf("a second auth header was sent alongside Basic: %v", strayHdr)
	}

	// Bitbucket nests the branch names; a flat head/base creates the wrong PR.
	if _, flat := gotBody["head"]; flat {
		t.Error("payload carries a flat head field — that is the GitHub/Forgejo shape")
	}
	src, _ := gotBody["source"].(map[string]any)
	srcBranch, _ := src["branch"].(map[string]any)
	if got := srcBranch["name"]; got != "phase/x" {
		t.Errorf("source.branch.name = %v, want %q", got, "phase/x")
	}
	dst, _ := gotBody["destination"].(map[string]any)
	dstBranch, _ := dst["branch"].(map[string]any)
	if got := dstBranch["name"]; got != "main" {
		t.Errorf("destination.branch.name = %v, want %q", got, "main")
	}
	if got := gotBody["description"]; got != "the body" {
		t.Errorf("description = %v, want %q", got, "the body")
	}
	if _, ok := gotBody["draft"]; ok {
		t.Error("draft sent on a non-draft PR")
	}

	if res.Number != 42 {
		t.Errorf("Number = %d, want 42 (read from id, not number)", res.Number)
	}
	if want := "https://bitbucket.org/acme/widget/pull-requests/42"; res.URL != want {
		t.Errorf("URL = %q, want %q — it comes from links.html.href, not html_url", res.URL, want)
	}
}

func TestOpenBitbucketPRDraftIsABoolean(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"id":1,"links":{"html":{"href":"https://bb/x"}}}`))
	}))
	t.Cleanup(server.Close)

	if _, err := OpenPR(OpenOpts{
		Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv: "MOCK_BB_TOKEN", AuthUser: "wsuser",
		HeadBranch: "h", BaseBranch: "main", Title: "My Title", Draft: true,
	}); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if got := gotBody["draft"]; got != true {
		t.Errorf("draft = %v, want true", got)
	}
	// Bitbucket has a real draft field — the Forgejo/GitLab title-prefix
	// convention would put "Draft: " in front of a title the API never asked
	// to be marked up.
	if got := gotBody["title"]; got != "My Title" {
		t.Errorf("title = %v, want %q — no Draft: prefix on this backend", got, "My Title")
	}
}

func TestOpenBitbucketPRMissingAuthUser(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	t.Cleanup(server.Close)

	_, err := OpenPR(OpenOpts{
		Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv:    "MOCK_BB_TOKEN", // AuthUser deliberately empty
		HeadBranch: "h", BaseBranch: "main", Title: "x",
	})
	if err == nil {
		t.Fatal("expected an error when auth_user is unset")
	}
	if !strings.Contains(err.Error(), "auth_user") {
		t.Errorf("error should name the missing setting: %v", err)
	}
	if requests != 0 {
		t.Errorf("made %d request(s); a missing auth_user must fail before any HTTP call", requests)
	}
}

func TestOpenBitbucketPRReviewerFailureNonFatal(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")

	var gotReviewers []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			_, _ = w.Write([]byte(`{"id":7,"links":{"html":{"href":"https://bb/acme/widget/pull-requests/7"}}}`))
		case "PUT":
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			gotReviewers, _ = body["reviewers"].([]any)
			http.Error(w, "reviewer not found", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	res, err := OpenPR(OpenOpts{
		Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv: "MOCK_BB_TOKEN", AuthUser: "wsuser",
		HeadBranch: "h", BaseBranch: "main", Title: "x",
		Reviewers: []string{"{504c3b62-8120-4f0c-a7bc-87800b9d6f70}", "557058:abc"},
	})
	if res == nil || res.Number != 7 {
		t.Fatalf("an already-open PR must survive a reviewer 4xx: res=%+v err=%v", res, err)
	}
	if err == nil {
		t.Error("expected a non-fatal error mentioning the reviewer failure")
	}
	if len(gotReviewers) != 2 {
		t.Fatalf("reviewers sent = %v, want 2 entries", gotReviewers)
	}
	// Bitbucket dropped username refs: a "{...}" string is a uuid, anything
	// else an account id.
	first, _ := gotReviewers[0].(map[string]any)
	if _, ok := first["uuid"]; !ok {
		t.Errorf("brace-wrapped reviewer should be sent as uuid, got %v", first)
	}
	second, _ := gotReviewers[1].(map[string]any)
	if _, ok := second["account_id"]; !ok {
		t.Errorf("non-uuid reviewer should be sent as account_id, got %v", second)
	}
}

func TestOpenPRNormalisesProvider(t *testing.T) {
	// A provider doctor accepts must dispatch. With configenum.Normalize the
	// bitbucket arm is reached and fails on its own missing credential; with a
	// bare strings.ToLower it falls through to "unsupported provider".
	for _, p := range []string{" bitbucket", "Bitbucket", "BITBUCKET\t"} {
		_, err := OpenPR(OpenOpts{Provider: p, URL: "https://bitbucket.org/acme/widget"})
		if err == nil {
			t.Fatalf("%q: expected the backend's own credential error", p)
		}
		if strings.Contains(err.Error(), "unsupported provider") {
			t.Errorf("%q dispatched nowhere: %v", p, err)
		}
	}
}

func TestOpenPRMessageListsShipProviders(t *testing.T) {
	_, err := OpenPR(OpenOpts{Provider: "perforce", URL: "x"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), configenum.ShipProviders.List()) {
		t.Errorf("message must be derived from ShipProviders, got: %v", err)
	}
	// The regression this guards: a hand-typed list that forgets the arm that
	// was just added.
	if !strings.Contains(err.Error(), "bitbucket") {
		t.Errorf("message omits an implemented backend: %v", err)
	}
}

func bbHeadOpts(server *httptest.Server, authUser string) OpenOpts {
	return OpenOpts{
		Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv: "MOCK_BB_TOKEN", AuthUser: authUser,
	}
}

// TestBitbucketHeadLookupQueryAndAuth: the q= filter names the source branch
// and the OPEN state, Basic auth carries auth_user, and values[0].id /
// links.html.href map to the result.
func TestBitbucketHeadLookupQueryAndAuth(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")
	var gotPath, gotQ, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQ = r.URL.Query().Get("q")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[{"id":9,"links":{"html":{"href":"https://bitbucket.org/acme/widget/pull-requests/9"}}}]}`))
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(bbHeadOpts(server, "wsuser"), "phase/x")
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if res == nil || res.Number != 9 {
		t.Fatalf("got %+v, want Number 9", res)
	}
	if res.URL != "https://bitbucket.org/acme/widget/pull-requests/9" {
		t.Errorf("URL = %q", res.URL)
	}
	if want := "/repositories/acme/widget/pullrequests"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if !strings.Contains(gotQ, `source.branch.name="phase/x"`) {
		t.Errorf("q missing source.branch.name=\"phase/x\": %q", gotQ)
	}
	if !strings.Contains(gotQ, `state="OPEN"`) {
		t.Errorf("q missing state=\"OPEN\": %q", gotQ)
	}
	if want := wantBasic("wsuser", "secret"); gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestBitbucketHeadLookupNoMatch(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(bbHeadOpts(server, "wsuser"), "phase/x")
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if res != nil {
		t.Errorf("got %+v, want nil for no open PR", res)
	}
}

// Missing auth_user is refused before any request (bbCredentials path).
func TestBitbucketHeadLookupMissingAuthUser(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(bbHeadOpts(server, ""), "phase/x")
	if err == nil || res != nil {
		t.Fatalf("missing auth_user must be (nil, err), got (%+v, %v)", res, err)
	}
	if !strings.Contains(err.Error(), "auth_user") {
		t.Errorf("error should name auth_user: %v", err)
	}
	if called {
		t.Error("no request may be made without credentials")
	}
}

func TestBitbucketHeadLookup401IsError(t *testing.T) {
	t.Setenv("MOCK_BB_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(bbHeadOpts(server, "wsuser"), "phase/x")
	if err == nil || res != nil {
		t.Fatalf("a 401 must be (nil, err), got (%+v, %v)", res, err)
	}
}

// TestBBRequestReturnsHeaders: a caller that passes the header out-param gets
// the response's headers — an async merge's Location — from the one request;
// a caller that does not is served exactly as before, and a body holding a
// secret is still refused before any request.
func TestBBRequestReturnsHeaders(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "http://"+r.Host+"/2.0/repositories/a/b/pullrequests/7/merge/task-status/abc")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	var hdr http.Header
	body, status, err := bbRequest("POST", srv.URL+"/2.0/x", "BB_TOKEN", "u", "t", map[string]any{"a": 1}, &hdr)
	if err != nil || status != http.StatusAccepted || string(body) != `{}` {
		t.Fatalf("got %q, %d, %v", body, status, err)
	}
	if got := hdr.Get("Location"); !strings.HasSuffix(got, "/2.0/repositories/a/b/pullrequests/7/merge/task-status/abc") {
		t.Errorf("Location = %q, want the task-status link", got)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests, want exactly 1", n)
	}

	body, status, err = bbRequest("GET", srv.URL+"/2.0/x", "BB_TOKEN", "u", "t", nil)
	if err != nil || status != http.StatusAccepted || string(body) != `{}` {
		t.Errorf("without the out-param: got %q, %d, %v", body, status, err)
	}

	hits.Store(0)
	_, _, err = bbRequest("POST", srv.URL+"/2.0/x", "BB_TOKEN", "u", "t", map[string]any{"description": "see ghp_" + strings.Repeat("A1b2", 9)}, &hdr)
	if !isErrHit(err) {
		t.Errorf("a ghp_ token in the body: got %v, want ErrHit", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("a screened body still sent %d request(s)", n)
	}
}
