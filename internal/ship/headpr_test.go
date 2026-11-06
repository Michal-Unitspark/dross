package ship

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/hostallow"
)

// TestFindOpenPRByHeadGitHubArgv walks the gh argv by index: `pr list`, the
// head in its own flag's value slot, and `--state open`. A head that escaped
// its slot would be read as a flag; a missing `--state open` would return
// closed PRs as live duplicates.
func TestFindOpenPRByHeadGitHubArgv(t *testing.T) {
	got := captureGh(t, `[{"number":7,"url":"https://github.com/o/r/pull/7"}]`)

	res, err := FindOpenPRByHead(OpenOpts{Provider: "github"}, "phase/x")
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if res == nil || res.Number != 7 {
		t.Fatalf("got %+v, want Number 7", res)
	}
	if res.URL != "https://github.com/o/r/pull/7" {
		t.Errorf("URL = %q", res.URL)
	}

	argv := *got
	if len(argv) < 2 || argv[0] != "pr" || argv[1] != "list" {
		t.Fatalf("argv does not start with `pr list`: %v", argv)
	}
	if i := indexOf(argv, "--head"); i < 0 || i+1 >= len(argv) || argv[i+1] != "phase/x" {
		t.Errorf("--head's value is not phase/x in its slot: %v", argv)
	}
	if i := indexOf(argv, "--state"); i < 0 || i+1 >= len(argv) || argv[i+1] != "open" {
		t.Errorf("--state is not followed by open: %v", argv)
	}
}

func TestFindOpenPRByHeadGitHubNoResults(t *testing.T) {
	captureGh(t, `[]`)
	res, err := FindOpenPRByHead(OpenOpts{Provider: "github"}, "phase/x")
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if res != nil {
		t.Errorf("got %+v, want nil for no open PR", res)
	}
}

// A lookup that swallows failure into (nil, nil) lets ship open a duplicate
// on a flaky network.
func TestFindOpenPRByHeadGitHubFailureIsError(t *testing.T) {
	prev := ghCommand
	ghCommand = func(...string) *exec.Cmd { return exec.Command("sh", "-c", "echo boom >&2; exit 1") }
	t.Cleanup(func() { ghCommand = prev })

	res, err := FindOpenPRByHead(OpenOpts{Provider: "github"}, "phase/x")
	if err == nil {
		t.Fatal("a failed gh must be an error, never nil-nil")
	}
	if res != nil {
		t.Errorf("result must be nil on error, got %+v", res)
	}
}

func forgejoHeadOpts(server *httptest.Server) OpenOpts {
	return OpenOpts{
		Provider: "forgejo", URL: "https://forge.example/me/p", APIBase: server.URL, Hosts: hostallow.Derive(server.URL, nil),
		AuthEnv: "MOCK_FORGEJO_TOKEN",
	}
}

func forgejoItem(number int, head string) map[string]any {
	return map[string]any{
		"number": number, "title": "t", "html_url": "u",
		"head": map[string]any{"ref": head}, "base": map[string]any{"ref": "main"},
	}
}

// TestFindOpenPRByHeadForgejoPaginates: the match sits on page 2, so a lister
// that stops at the first full page never finds it.
func TestFindOpenPRByHeadForgejoPaginates(t *testing.T) {
	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")
	page1 := make([]map[string]any, 50)
	for i := range page1 {
		page1[i] = forgejoItem(i+1, "other")
	}
	page1JSON, _ := json.Marshal(page1)
	page2JSON, _ := json.Marshal([]map[string]any{forgejoItem(12, "phase/x")})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(page1JSON)
			return
		}
		_, _ = w.Write(page2JSON)
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(forgejoHeadOpts(server), "phase/x")
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if res == nil || res.Number != 12 {
		t.Fatalf("got %+v, want Number 12 from page 2", res)
	}
}

func TestFindOpenPRByHeadForgejoNoMatch(t *testing.T) {
	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"number":1,"title":"t","html_url":"u","head":{"ref":"other"},"base":{"ref":"main"}}]`))
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(forgejoHeadOpts(server), "phase/x")
	if err != nil {
		t.Fatalf("FindOpenPRByHead: %v", err)
	}
	if res != nil {
		t.Errorf("got %+v, want nil when no head matches", res)
	}
}

func TestFindOpenPRByHeadForgejo500IsError(t *testing.T) {
	t.Setenv("MOCK_FORGEJO_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	res, err := FindOpenPRByHead(forgejoHeadOpts(server), "phase/x")
	if err == nil {
		t.Fatalf("a 500 must be an error, got %+v", res)
	}
	if res != nil {
		t.Errorf("result must be nil on error, got %+v", res)
	}
}

// An unknown provider is a plain error naming the supported list — never the
// ErrHeadPRLookupUnsupported sentinel, which is reserved for a known provider
// whose lookup isn't wired (none today).
func TestFindOpenPRByHeadUnknownProviderIsPlainError(t *testing.T) {
	_, err := FindOpenPRByHead(OpenOpts{Provider: "sourcehut"}, "phase/x")
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	if errors.Is(err, ErrHeadPRLookupUnsupported) {
		t.Error("an unknown provider should not report as merely unsupported")
	}
	if !strings.Contains(err.Error(), configenum.ShipProviders.List()) {
		t.Errorf("message must be derived from ShipProviders, got: %v", err)
	}
}

// Mirror of TestOpenPRsTargetingFuncDefaultsToOpenPRsTargeting: the seam
// delegates to FindOpenPRByHead, proven by the dispatch's own unknown-provider
// error coming back through it.
func TestFindOpenPRByHeadFuncDefaultsToFindOpenPRByHead(t *testing.T) {
	if FindOpenPRByHeadFunc == nil {
		t.Fatal("FindOpenPRByHeadFunc must be a non-nil overridable var")
	}
	_, err := FindOpenPRByHeadFunc(OpenOpts{Provider: "sourcehut"}, "phase/x")
	if err == nil || !strings.Contains(err.Error(), configenum.ShipProviders.List()) {
		t.Errorf("FindOpenPRByHeadFunc should delegate to FindOpenPRByHead, got: %v", err)
	}
}

// adoHeadServer answers the active-PR list. It asserts the server-side
// filter, then returns body whatever was asked — so a test can also play a
// server that ignored the filter.
func adoHeadServer(t *testing.T, wantRef string, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if wantRef != "" {
			if got := q.Get("searchCriteria.sourceRefName"); got != wantRef {
				t.Errorf("searchCriteria.sourceRefName = %q, want %q", got, wantRef)
			}
			if got := q.Get("searchCriteria.status"); got != "active" {
				t.Errorf("searchCriteria.status = %q, want active", got)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func adoHeadOpts(srv *httptest.Server) OpenOpts {
	return OpenOpts{
		Provider: " Azure-DevOps ", URL: "https://dev.azure.com/o/p/_git/r", APIBase: srv.URL + "/o",
		AuthEnv: "MOCK_ADO_PAT", Hosts: hostallow.Derive(srv.URL, nil),
	}
}

// TestFindOpenPRByHeadAzureDevOps: the azure-devops arm filters active PRs by
// full source ref, re-checks every hit, and never reads a failed lookup as
// "no PR" or as "unsupported".
func TestFindOpenPRByHeadAzureDevOps(t *testing.T) {
	t.Setenv("MOCK_ADO_PAT", "pat")

	t.Run("hit", func(t *testing.T) {
		srv := adoHeadServer(t, "refs/heads/phase/x", 200,
			`{"count":1,"value":[{"pullRequestId":17,"status":"active","sourceRefName":"refs/heads/phase/x"}]}`)
		res, err := FindOpenPRByHead(adoHeadOpts(srv), "phase/x")
		if err != nil || res == nil || res.Number != 17 || res.URL != "https://dev.azure.com/o/p/_git/r/pullrequest/17" {
			t.Errorf("got %+v, %v; want #17 at https://dev.azure.com/o/p/_git/r/pullrequest/17", res, err)
		}
	})

	t.Run("head with & and + round-trips", func(t *testing.T) {
		srv := adoHeadServer(t, "refs/heads/phase/a&b+c", 200,
			`{"value":[{"pullRequestId":3,"status":"active","sourceRefName":"refs/heads/phase/a&b+c"}]}`)
		if res, err := FindOpenPRByHead(adoHeadOpts(srv), "phase/a&b+c"); err != nil || res == nil || res.Number != 3 {
			t.Errorf("got %+v, %v; want #3", res, err)
		}
	})

	t.Run("none", func(t *testing.T) {
		srv := adoHeadServer(t, "refs/heads/phase/x", 200, `{"count":0,"value":[]}`)
		if res, err := FindOpenPRByHead(adoHeadOpts(srv), "phase/x"); res != nil || err != nil {
			t.Errorf("got (%+v, %v), want (nil, nil)", res, err)
		}
	})

	t.Run("server ignored the filter", func(t *testing.T) {
		srv := adoHeadServer(t, "", 200, `{"value":[
			{"pullRequestId":4,"status":"active","sourceRefName":"refs/heads/other"},
			{"pullRequestId":5,"status":"abandoned","sourceRefName":"refs/heads/phase/x"},
			{"pullRequestId":6,"status":"active","sourceRefName":"refs/heads/phase/x","forkSource":{"repository":{"id":"f"}}}
		]}`)
		if res, err := FindOpenPRByHead(adoHeadOpts(srv), "phase/x"); res != nil || err != nil {
			t.Errorf("got (%+v, %v), want (nil, nil) — none of those is this phase's PR", res, err)
		}
	})

	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"500":              {500, `{"message":"boom"}`},
		"203 sign-in page": {http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`},
		"unparseable body": {200, `not json`},
		"no value list":    {200, `{"count":0}`},
		"match with no id": {200, `{"value":[{"status":"active","sourceRefName":"refs/heads/phase/x"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := adoHeadServer(t, "", tc.status, tc.body)
			res, err := FindOpenPRByHead(adoHeadOpts(srv), "phase/x")
			if err == nil || res != nil {
				t.Fatalf("got (%+v, %v), want (nil, err)", res, err)
			}
			if errors.Is(err, ErrHeadPRLookupUnsupported) {
				t.Errorf("a failed lookup reads as unsupported: %v", err)
			}
		})
	}
}
