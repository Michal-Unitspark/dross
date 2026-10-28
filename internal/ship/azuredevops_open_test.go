package ship

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/Rivil/dross/internal/hostallow"
)

const (
	adoGUID1 = "6f1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	adoGUID2 = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	adoAlice = "alice+ado@corp.example"
)

// adoCall is one request the PR mock saw.
type adoCall struct {
	method, path string
	query        url.Values
	body         map[string]any
}

// adoPRMock answers the create, identities and reviewer endpoints. Each
// handler is keyed by method plus a path suffix; an unmatched request is a 404.
type adoPRMock struct {
	*httptest.Server
	mu    sync.Mutex
	calls []adoCall
}

type adoRoute struct {
	method, suffix string
	status         int
	body           string
}

func newADOPRMock(t *testing.T, routes ...adoRoute) *adoPRMock {
	t.Helper()
	m := &adoPRMock{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		m.mu.Lock()
		m.calls = append(m.calls, adoCall{r.Method, r.URL.EscapedPath(), r.URL.Query(), body})
		m.mu.Unlock()
		for _, rt := range routes {
			if r.Method == rt.method && strings.HasSuffix(r.URL.EscapedPath(), rt.suffix) {
				w.WriteHeader(rt.status)
				_, _ = w.Write([]byte(rt.body))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no route"}`))
	}))
	t.Cleanup(m.Close)
	return m
}

// matching returns the calls with method whose path contains part.
func (m *adoPRMock) matching(method, part string) []adoCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []adoCall
	for _, c := range m.calls {
		if c.method == method && strings.Contains(c.path, part) {
			out = append(out, c)
		}
	}
	return out
}

var adoCreated = adoRoute{"POST", "/pullrequests", 201, `{"pullRequestId":42}`}

func adoOpenOpts(srv *httptest.Server) OpenOpts {
	opts := adoTestOpts(srv)
	opts.HeadBranch, opts.BaseBranch = "phase/x", "main"
	opts.Title, opts.Body = "Phase x", "the body"
	return opts
}

// TestOpenAzureDevOpsPRPayload: the create body is Azure Repos' shape — full
// refs, a real isDraft, no title prefix, no reviewers.
func TestOpenAzureDevOpsPRPayload(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for _, tc := range []struct {
		name, head string
		draft      bool
	}{
		{"plain head, ready", "phase/x", false},
		{"prefixed head, draft", "refs/heads/phase/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newADOPRMock(t, adoCreated)
			opts := adoOpenOpts(m.Server)
			opts.HeadBranch, opts.Draft = tc.head, tc.draft
			opts.Reviewers = nil
			if _, err := openAzureDevOpsPR(opts); err != nil {
				t.Fatal(err)
			}
			posts := m.matching("POST", "/pullrequests")
			if len(posts) != 1 {
				t.Fatalf("want one create POST, got %d", len(posts))
			}
			b := posts[0].body
			for key, want := range map[string]string{
				"sourceRefName": "refs/heads/phase/x",
				"targetRefName": "refs/heads/main",
				"title":         "Phase x",
				"description":   "the body",
			} {
				if b[key] != want {
					t.Errorf("%s = %v, want %q", key, b[key], want)
				}
			}
			if d, ok := b["isDraft"]; tc.draft && d != true || !tc.draft && ok {
				t.Errorf("isDraft = %v (present %v), want draft=%v sent only when true", d, ok, tc.draft)
			}
			if _, ok := b["reviewers"]; ok {
				t.Error("the create body carries reviewers")
			}
		})
	}
}

// TestOpenAzureDevOpsPRResultURL: the result is the browser URL built on the
// canonical remote, single-encoded; a missing id or a refused create is no PR.
func TestOpenAzureDevOpsPRResultURL(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)

	m := newADOPRMock(t, adoCreated)
	res, err := openAzureDevOpsPR(adoOpenOpts(m.Server))
	if err != nil || res == nil || res.Number != 42 || res.URL != "https://dev.azure.com/o/p/_git/r/pullrequest/42" {
		t.Errorf("got %+v, %v; want #42 at https://dev.azure.com/o/p/_git/r/pullrequest/42", res, err)
	}

	enc := newADOPRMock(t, adoCreated)
	opts := adoOpenOpts(enc.Server)
	opts.URL = "https://dev.azure.com/o/My%20Project/_git/My%20Repo"
	if res, _ := openAzureDevOpsPR(opts); res == nil || res.URL != "https://dev.azure.com/o/My%20Project/_git/My%20Repo/pullrequest/42" {
		t.Errorf("encoded remote: got %+v", res)
	}

	for name, route := range map[string]adoRoute{
		"missing pullRequestId": {"POST", "/pullrequests", 201, `{"status":"active"}`},
		"409 TF401179":          {"POST", "/pullrequests", 409, `{"message":"TF401179: An active pull request for the source and target branch already exists."}`},
		"create 500":            {"POST", "/pullrequests", 500, `{"message":"boom"}`},
	} {
		m := newADOPRMock(t, route)
		if res, err := openAzureDevOpsPR(adoOpenOpts(m.Server)); res != nil || err == nil {
			t.Errorf("%s: got (%+v, %v), want (nil, err)", name, res, err)
		}
	}
}

// TestOpenAzureDevOpsPRServerAddressing: a Server collection under /tfs is the
// base every call hangs off.
func TestOpenAzureDevOpsPRServerAddressing(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t, adoCreated)
	opts := adoOpenOpts(m.Server)
	opts.URL = "https://tfs.corp/tfs/DefaultCollection/P/_git/R"
	opts.APIBase = m.URL + "/tfs/DefaultCollection"
	if _, err := openAzureDevOpsPR(opts); err != nil {
		t.Fatal(err)
	}
	if got := m.matching("POST", "/tfs/DefaultCollection/P/_apis/git/repositories/R/pullrequests"); len(got) != 1 {
		t.Errorf("want one POST to /tfs/DefaultCollection/P/_apis/git/repositories/R/pullrequests, saw %+v", m.calls)
	}
}

// TestOpenAzureDevOpsPRReviewers: a GUID is used as-is, an email is resolved
// with exactly one identities lookup, and both are added as optional.
func TestOpenAzureDevOpsPRReviewers(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t, adoCreated,
		adoRoute{"GET", "/_apis/identities", 200, `{"count":1,"value":[{"id":"` + adoGUID2 + `"}]}`},
		adoRoute{"PUT", "/reviewers/" + adoGUID1, 200, `{}`},
		adoRoute{"PUT", "/reviewers/" + adoGUID2, 200, `{}`},
	)
	opts := adoOpenOpts(m.Server)
	opts.Reviewers = []string{adoGUID1, adoAlice}
	if _, err := openAzureDevOpsPR(opts); err != nil {
		t.Fatalf("open: %v", err)
	}
	lookups := m.matching("GET", "/_apis/identities")
	if len(lookups) != 1 {
		t.Fatalf("want exactly one identities GET, got %d", len(lookups))
	}
	// adoAlice carries a +, which an unescaped query would decode to a space.
	if q := lookups[0].query; q.Get("searchFilter") != "General" || q.Get("filterValue") != adoAlice {
		t.Errorf("identities query = %v, want searchFilter=General&filterValue=%s", q, adoAlice)
	}
	if posts := m.matching("POST", "/pullrequests"); len(posts) != 1 {
		t.Fatalf("want one create POST, got %d", len(posts))
	} else if _, ok := posts[0].body["reviewers"]; ok {
		t.Error("configured reviewers rode on the create body instead of their own PUTs")
	}
	puts := m.matching("PUT", "/pullRequests/42/reviewers/")
	if len(puts) != 2 {
		t.Fatalf("want two reviewer PUTs, got %d", len(puts))
	}
	for _, p := range puts {
		if req, ok := p.body["isRequired"]; !ok || req != false {
			t.Errorf("%s: isRequired = %v, want false", p.path, req)
		}
	}
	if !strings.HasSuffix(puts[0].path, adoGUID1) || !strings.HasSuffix(puts[1].path, adoGUID2) {
		t.Errorf("reviewers added in the wrong order or to the wrong ids: %s, %s", puts[0].path, puts[1].path)
	}
}

// TestOpenAzureDevOpsPRReviewerFailureNonFatal: every way a reviewer can fail
// still returns the opened PR, names the reviewer (never the token), and adds
// the reviewers after it.
func TestOpenAzureDevOpsPRReviewerFailureNonFatal(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	okPut := adoRoute{"PUT", "/reviewers/" + adoGUID2, 200, `{}`}
	for name, tc := range map[string]struct {
		failing string
		route   adoRoute
	}{
		"identities 404":   {adoAlice, adoRoute{"GET", "/_apis/identities", 404, `{"message":"` + adoTestTok + `"}`}},
		"identities 500":   {adoAlice, adoRoute{"GET", "/_apis/identities", 500, `{}`}},
		"0 matches":        {adoAlice, adoRoute{"GET", "/_apis/identities", 200, `{"count":0,"value":[]}`}},
		"2 matches":        {adoAlice, adoRoute{"GET", "/_apis/identities", 200, `{"count":2,"value":[{"id":"a"},{"id":"b"}]}`}},
		"reviewer PUT 500": {adoGUID1, adoRoute{"PUT", "/reviewers/" + adoGUID1, 500, `{"message":"nope"}`}},
	} {
		t.Run(name, func(t *testing.T) {
			m := newADOPRMock(t, adoCreated, tc.route, okPut)
			opts := adoOpenOpts(m.Server)
			opts.Reviewers = []string{tc.failing, adoGUID2}
			res, err := openAzureDevOpsPR(opts)
			if res == nil || res.Number != 42 {
				t.Fatalf("the opened PR was lost: res=%+v err=%v", res, err)
			}
			if err == nil || !strings.Contains(err.Error(), tc.failing) {
				t.Errorf("want an error naming %s, got %v", tc.failing, err)
			}
			if err != nil && strings.Contains(err.Error(), adoTestTok) {
				t.Errorf("the token leaked into the error: %v", err)
			}
			if got := m.matching("PUT", "/reviewers/"+adoGUID2); len(got) != 1 {
				t.Errorf("the reviewer after the failure was not added (%d PUTs)", len(got))
			}
			if name == "2 matches" && len(m.matching("PUT", "/reviewers/a")) != 0 {
				t.Error("an ambiguous identity was guessed")
			}
		})
	}
}

// TestAzureDevOpsIdentityBase: Services resolves identities on the org's
// vssps host; a Server resolves them on its own collection.
func TestAzureDevOpsIdentityBase(t *testing.T) {
	for in, want := range map[string]string{
		"https://dev.azure.com/o":        "https://vssps.dev.azure.com/o",
		"https://dev.azure.com/o/":       "https://vssps.dev.azure.com/o",
		"https://o.visualstudio.com":     "https://vssps.dev.azure.com/o",
		"https://o.visualstudio.com/":    "https://vssps.dev.azure.com/o",
		"https://tfs.corp/tfs/Coll":      "https://tfs.corp/tfs/Coll",
		"https://tfs.corp/Coll/":         "https://tfs.corp/Coll",
		"https://evil-visualstudio.com":  "https://evil-visualstudio.com",
		"https://a.b.visualstudio.com/x": "https://a.b.visualstudio.com/x",
	} {
		if got := adoIdentityBase(in); got != want {
			t.Errorf("adoIdentityBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOpenAzureDevOpsIdentityHostIsChecked: the vssps identities host is a
// different host from api_base, so it is checked on its own — an allowlist
// that lacks it refuses the lookup, and no request is made.
func TestOpenAzureDevOpsIdentityHostIsChecked(t *testing.T) {
	c := adoConn{apiBase: "https://elsewhere.example/o", authEnv: adoTestEnv, token: adoTestTok, version: "7.1"}
	_, err := adoResolveIdentity(c, hostallow.Derive("https://dev.azure.com/o/p/_git/r", nil), adoAlice)
	if err == nil || !strings.Contains(err.Error(), "refusing to contact host") {
		t.Errorf("an off-allowlist identities host was not refused: %v", err)
	}
}

// TestOpenAzureDevOpsPRLongDescription: a body over the limit is cut to it,
// ends in the note, and stays valid UTF-8.
func TestOpenAzureDevOpsPRLongDescription(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	m := newADOPRMock(t, adoCreated)
	opts := adoOpenOpts(m.Server)
	opts.Body = strings.Repeat("ż", 3990) + strings.Repeat("é", 100)
	if _, err := openAzureDevOpsPR(opts); err != nil {
		t.Fatal(err)
	}
	sent, _ := m.matching("POST", "/pullrequests")[0].body["description"].(string)
	if n := utf8.RuneCountInString(sent); n > adoDescriptionLimit {
		t.Errorf("description is %d runes, want at most %d", n, adoDescriptionLimit)
	}
	if !strings.HasSuffix(sent, adoTruncationNote) {
		t.Errorf("description does not end in the truncation note: …%q", sent[max(0, len(sent)-80):])
	}
	if !utf8.ValidString(sent) {
		t.Error("the cut split a multi-byte rune")
	}
	// A 3-byte rune after a 1-byte one: no byte budget a naive slice could pick
	// lands on a rune boundary by luck, so a byte cut would split one.
	if d := adoDescription("a" + strings.Repeat("€", 4100)); !utf8.ValidString(d) || utf16Len(d) > adoDescriptionLimit || !strings.HasSuffix(d, adoTruncationNote) {
		t.Errorf("3-byte body: valid=%v, %d UTF-16 units", utf8.ValidString(d), utf16Len(d))
	}
	if short := adoDescription("short"); short != "short" {
		t.Errorf("a body within the limit was changed: %q", short)
	}
	// Astral runes are two UTF-16 units each: 2100 of them is over the limit.
	if d := adoDescription(strings.Repeat("😀", 2100)); utf16Len(d) > adoDescriptionLimit || !utf8.ValidString(d) {
		t.Errorf("astral body: %d UTF-16 units, valid=%v", utf16Len(d), utf8.ValidString(d))
	}
}
