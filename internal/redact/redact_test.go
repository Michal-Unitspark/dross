package redact_test

import (
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/redact"
)

// canary is the seeded credential. Nothing dross emits may contain it.
const canary = "c4n4ry-t0k3n-do-not-leak-8f2a"

const canaryEnv = "DROSS_REDACT_CANARY"

// --- Scrub unit rows ---

func TestScrub(t *testing.T) {
	got := redact.Scrub("Authorization: token abc", canaryEnv, "abc")
	if strings.Contains(got, "abc") {
		t.Errorf("Scrub left the raw token in place: %q", got)
	}
	if !strings.Contains(got, "Authorization") {
		t.Errorf("Scrub destroyed the surrounding message: %q", got)
	}
}

// TestScrubEmptyToken: an unset credential must not turn every message into a
// redaction marker. Callers pass os.Getenv results straight through, so this is
// the common case on a machine with no token configured, not an edge case.
func TestScrubEmptyToken(t *testing.T) {
	const msg = "GET /issues: HTTP 404: not found"
	if got := redact.Scrub(msg, canaryEnv, ""); got != msg {
		t.Errorf("Scrub with an empty token rewrote the message: %q", got)
	}
	if err := redact.Err(fmt.Errorf("%s", msg), canaryEnv, ""); err.Error() != msg {
		t.Errorf("Err with an empty token rewrote the message: %q", err)
	}
}

// TestScrubShortTokenNotGlobal: a one-character env value is a placeholder or a
// typo, never a secret. Replacing every occurrence of it would shred the message
// into unreadability — losing the diagnostic and gaining no security.
func TestScrubShortTokenNotGlobal(t *testing.T) {
	const msg = "GET /a: HTTP 404: no such repo"
	if got := redact.Scrub(msg, canaryEnv, "a"); got != msg {
		t.Errorf("a one-character token blanked the message: %q", got)
	}
}

// TestScrubBase64: HTTP Basic puts base64(user:token) on the wire, and an
// upstream that mirrors the header back leaks a perfectly usable credential
// that no literal token match would find.
func TestScrubBase64(t *testing.T) {
	cred := base64.StdEncoding.EncodeToString([]byte("me@example.com:" + canary))
	msg := "HTTP 401: upstream echoed Authorization: Basic " + cred
	got := redact.Scrub(msg, canaryEnv, canary)
	if strings.Contains(got, cred) {
		t.Errorf("the base64 credential survived: %q", got)
	}
	if strings.Contains(got, canary) {
		t.Errorf("the raw token survived: %q", got)
	}

	// The raw (unpadded) and URL-safe variants too — which encoding an upstream
	// chose is not knowable here.
	for name, enc := range map[string]*base64.Encoding{
		"raw-std":  base64.RawStdEncoding,
		"url":      base64.URLEncoding,
		"raw-url":  base64.RawURLEncoding,
		"bare-b64": base64.StdEncoding,
	} {
		payload := "user:" + canary
		if name == "bare-b64" {
			payload = canary
		}
		v := enc.EncodeToString([]byte(payload))
		if got := redact.Scrub("body: "+v, canaryEnv, canary); strings.Contains(got, v) {
			t.Errorf("%s encoding survived: %q", name, got)
		}
	}
}

// TestScrubMarkerNamesEnvVar: a message reduced to a bare "[redacted]" is safe
// and useless. Naming the env var keeps the message diagnostic — the user still
// learns which credential the failing request was carrying.
func TestScrubMarkerNamesEnvVar(t *testing.T) {
	got := redact.Scrub("Authorization: Bearer "+canary, "GITHUB_TOKEN", canary)
	if !strings.Contains(got, "[redacted $GITHUB_TOKEN]") {
		t.Errorf("marker does not name the env var: %q", got)
	}
}

// --- forge client rows ---

// echoAuthServer returns a server that answers every request with `status` and
// mirrors the credential headers it received into the body — the real upstream
// behaviour that turns dross's own error message into the exfiltration channel.
func echoAuthServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"message":"rejected request with Authorization: %s / PRIVATE-TOKEN: %s"}`,
			r.Header.Get("Authorization"), r.Header.Get("PRIVATE-TOKEN"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// assertNoCredential is the shared assertion: the error is non-nil, mentions
// neither the raw token nor any encoded form of it, and — the half that keeps
// the test from passing by silence — still carries the redaction marker naming
// the env var.
func assertNoCredential(t *testing.T, err error, wantMarker bool) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error from the failing request, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, canary) {
		t.Errorf("error carries the raw token: %s", msg)
	}
	for _, user := range []string{"me@example.com", "me", ""} {
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + canary))
		if strings.Contains(msg, cred) {
			t.Errorf("error carries base64(%s:token): %s", user, msg)
		}
	}
	if wantMarker && !strings.Contains(msg, "[redacted $"+canaryEnv) {
		t.Errorf("error lost the redaction marker — reverting the scrub must fail this test, not pass it silently: %s", msg)
	}
}

// TestForgeErrorsCarryNoToken drives every status path plus a hard transport
// failure. Scrubbing applied to only one of them would leave the others open,
// so each is its own row.
func TestForgeErrorsCarryNoToken(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			t.Setenv(canaryEnv, canary)
			srv := echoAuthServer(t, status)
			c, err := forge.New(forge.Config{
				Provider: "forgejo",
				URL:      "https://forge.example/me/proj",
				APIBase:  srv.URL,
				AuthEnv:  canaryEnv,
				Hosts:    hostallow.Derive(srv.URL, nil),
			})
			if err != nil {
				t.Fatalf("forge.New: %v", err)
			}
			_, gerr := c.GetIssue("1")
			assertNoCredential(t, gerr, true)
		})
	}

	// A hard transport failure takes a different return path out of do. It
	// carries no body today, which is exactly why it is easy to leave unwrapped
	// — and why it is pinned here rather than assumed.
	t.Run("transport failure", func(t *testing.T) {
		t.Setenv(canaryEnv, canary)
		srv := echoAuthServer(t, 500)
		base := srv.URL
		c, err := forge.New(forge.Config{
			Provider: "forgejo",
			URL:      "https://forge.example/me/proj",
			APIBase:  base,
			AuthEnv:  canaryEnv,
			Hosts:    hostallow.Derive(base, nil),
		})
		if err != nil {
			t.Fatalf("forge.New: %v", err)
		}
		srv.Close()
		_, gerr := c.GetIssue("1")
		assertNoCredential(t, gerr, false)
	})
}

// TestEveryForgeClientRedacts gives each of the `do` implementations its
// own row, so removing the scrub from one fails exactly that row and no other.
// A single combined assertion would let three clients hide behind the fourth.
func TestEveryForgeClientRedacts(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, apiBase string) error
	}{
		{"forge.Client token scheme", func(t *testing.T, apiBase string) error {
			c, err := forge.New(forge.Config{
				Provider: "forgejo", URL: "https://forge.example/me/proj",
				APIBase: apiBase, AuthEnv: canaryEnv, Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("forge.New: %v", err)
			}
			_, e := c.GetIssue("1")
			return e
		}},
		{"forge.Client bearer scheme", func(t *testing.T, apiBase string) error {
			c, err := forge.New(forge.Config{
				Provider: "gitlab", URL: "https://forge.example/me/proj",
				APIBase: apiBase, AuthEnv: canaryEnv, AuthScheme: "bearer",
				Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("forge.New: %v", err)
			}
			_, e := c.GetIssue("1")
			return e
		}},
		{"forge.Client basic scheme", func(t *testing.T, apiBase string) error {
			c, err := forge.New(forge.Config{
				Provider: "forgejo", URL: "https://forge.example/me/proj",
				APIBase: apiBase, AuthEnv: canaryEnv, AuthScheme: "basic", AuthUser: "me",
				Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("forge.New: %v", err)
			}
			_, e := c.GetIssue("1")
			return e
		}},
		{"GitHubClient", func(t *testing.T, apiBase string) error {
			c, err := forge.NewGitHubProjects(forge.Config{
				APIBase: apiBase, AuthEnv: canaryEnv, Project: "octo/repo",
				Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("NewGitHubProjects: %v", err)
			}
			_, e := c.GetIssue("1")
			return e
		}},
		{"JiraClient", func(t *testing.T, apiBase string) error {
			c, err := forge.NewJira(forge.Config{
				APIBase: apiBase, AuthEnv: canaryEnv, Project: "PROJ", AuthUser: "me@example.com",
				Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("NewJira: %v", err)
			}
			_, e := c.GetIssue("PROJ-1")
			return e
		}},
		{"AzureBoardsClient", func(t *testing.T, apiBase string) error {
			// An Azure DevOps base_url needs its collection, so the echo
			// server is addressed as one.
			c, err := forge.NewAzureBoards(forge.Config{
				Provider: "azure-devops", APIBase: apiBase + "/acme", AuthEnv: canaryEnv, Project: "Fabrikam",
				Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("NewAzureBoards: %v", err)
			}
			_, e := c.GetIssue("1")
			return e
		}},
		{"YouTrackClient", func(t *testing.T, apiBase string) error {
			c, err := forge.NewYouTrack(forge.Config{
				APIBase: apiBase, AuthEnv: canaryEnv, Project: "PROJ",
				Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("NewYouTrack: %v", err)
			}
			_, e := c.GetIssue("PROJ-1")
			return e
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(canaryEnv, canary)
			srv := echoAuthServer(t, 401)
			assertNoCredential(t, tc.call(t, srv.URL), true)
		})
	}
}

// TestEveryForgeDoRedacts enumerates rather than samples: it finds every method
// named `do` in internal/forge and asserts each one routes through redact. A
// fifth client added a year from now cannot join the package unscrubbed, even
// though no per-client row above knows it exists.
func TestEveryForgeDoRedacts(t *testing.T) {
	dir := forgePackageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	found := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "do" || fn.Recv == nil || fn.Body == nil {
				continue
			}
			key := name + ":" + receiverName(fn)
			found[key] = true
			if !callsRedact(fn.Body) {
				t.Errorf("%s: %s.do does not route its error through redact — it sets an Authorization header and echoes an upstream body",
					name, receiverName(fn))
			}
		}
	}
	if len(found) < 4 {
		var names []string
		for k := range found {
			names = append(names, k)
		}
		sort.Strings(names)
		t.Fatalf("found %d do methods (%v), want at least 4 — the walk is broken, not the package", len(found), names)
	}
}

func receiverName(fn *ast.FuncDecl) string {
	if len(fn.Recv.List) == 0 {
		return "?"
	}
	switch e := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return e.Name
	}
	return "?"
}

func callsRedact(body *ast.BlockStmt) bool {
	seen := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "redact" {
			seen = true
		}
		return true
	})
	return seen
}

// forgePackageDir resolves internal/forge from this test's own location, so the
// walk does not depend on the working directory the suite was started from.
func forgePackageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // .../internal/redact
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(wd), "forge")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("cannot find internal/forge from %s: %v", wd, err)
	}
	return dir
}
