package ship

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

// t-3's Scrub applied at ship's error sites.
//
// Every non-gh backend here echoes an upstream body snippet into its error, and
// every one of them set an Authorization header on the request that produced it.
// An API that mirrors request headers back — several do, for debugging — turns
// dross's own error message into the exfiltration channel.
//
// The scrub sits at the ONE place a body enters the package (each backend's
// shared request helper), not at each Errorf. That is what covers the sites the
// per-backend table below does not name: "response missing iid", the two
// reviewer-assignment paths, and whatever the fifteenth interpolation turns out
// to be.

const shipCanary = "sh1p-c4n4ry-do-not-leak-93bd"

const shipCanaryEnv = "DROSS_SHIP_REDACT_CANARY"

// mirrorAuthServer answers everything with `status` and echoes the credential
// headers it received into the body.
func mirrorAuthServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":"rejected: Authorization=%s PRIVATE-TOKEN=%s"}`,
			r.Header.Get("Authorization"), r.Header.Get("PRIVATE-TOKEN"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertShipRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error from the failing request, got nil")
	}
	msg := err.Error()
	if strings.Contains(msg, shipCanary) {
		t.Errorf("error carries the raw token: %s", msg)
	}
	if !strings.Contains(msg, "[redacted $"+shipCanaryEnv) {
		t.Errorf("error lost the redaction marker — reverting the scrub must fail this test, not pass it silently: %s", msg)
	}
}

// TestBackendErrorsRedacted gives each backend its own row, so removing the
// scrub from one helper fails exactly that row and no other.
func TestBackendErrorsRedacted(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, apiBase string) error
	}{
		{"forgejo OpenPR", func(t *testing.T, apiBase string) error {
			_, err := OpenPR(OpenOpts{
				Provider: "forgejo", URL: "https://forge.example/me/p", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil),
				HeadBranch: "phase/x", BaseBranch: "main", Title: "t", Body: "b",
			})
			return err
		}},
		{"forgejo GetPRStatus", func(t *testing.T, apiBase string) error {
			_, err := GetPRStatus(OpenOpts{
				Provider: "forgejo", URL: "https://forge.example/me/p", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil), PRNumber: 3,
			})
			return err
		}},
		{"gitlab OpenPR", func(t *testing.T, apiBase string) error {
			_, err := OpenPR(OpenOpts{
				Provider: "gitlab", URL: "https://gitlab.example/me/p", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil),
				HeadBranch: "phase/x", BaseBranch: "main", Title: "t", Body: "b",
			})
			return err
		}},
		{"gitlab GetPRStatus", func(t *testing.T, apiBase string) error {
			_, err := GetPRStatus(OpenOpts{
				Provider: "gitlab", URL: "https://gitlab.example/me/p", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil), PRNumber: 3,
			})
			return err
		}},
		{"bitbucket OpenPR", func(t *testing.T, apiBase string) error {
			_, err := OpenPR(OpenOpts{
				Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, AuthUser: "wsuser", Hosts: hostallow.Derive(apiBase, nil),
				HeadBranch: "phase/x", BaseBranch: "main", Title: "t", Body: "b",
			})
			return err
		}},
		{"bitbucket GetPRStatus", func(t *testing.T, apiBase string) error {
			_, err := GetPRStatus(OpenOpts{
				Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, AuthUser: "wsuser", Hosts: hostallow.Derive(apiBase, nil), PRNumber: 3,
			})
			return err
		}},
		{"gitlab comment note", func(t *testing.T, apiBase string) error {
			return PostComment(CommentOpts{
				Provider: "gitlab", URL: "https://gitlab.example/me/p", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil),
				PRNumber: 3, Body: "panel findings",
			})
		}},
		{"bitbucket comment", func(t *testing.T, apiBase string) error {
			return PostComment(CommentOpts{
				Provider: "bitbucket", URL: "https://bitbucket.org/acme/widget", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, AuthUser: "wsuser", Hosts: hostallow.Derive(apiBase, nil),
				PRNumber: 3, Body: "panel findings",
			})
		}},
		{"azure-devops adoRequest", func(t *testing.T, apiBase string) error {
			c, err := adoOpenConn(OpenOpts{
				URL: "https://dev.azure.com/o/p/_git/r", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil),
			})
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			_, _, err = adoRequest(c, "GET", c.repoAPI()+"/pullRequests/3", nil)
			return err
		}},
		{"forgejo comment", func(t *testing.T, apiBase string) error {
			return PostComment(CommentOpts{
				Provider: "forgejo", URL: "https://forge.example/me/p", APIBase: apiBase,
				AuthEnv: shipCanaryEnv, Hosts: hostallow.Derive(apiBase, nil),
				PRNumber: 3, Body: "panel findings",
			})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(shipCanaryEnv, shipCanary)
			srv := mirrorAuthServer(t, 403)
			assertShipRedacted(t, tc.call(t, srv.URL))
		})
	}
}

// TestHTTPSnippetRedacted covers jsonPost's bare `HTTP %d: %s` — the one error
// in ship that says nothing but the status and the body, against a server that
// mirrors back the Authorization header jsonPost set itself a few lines above.
func TestHTTPSnippetRedacted(t *testing.T) {
	t.Setenv(shipCanaryEnv, shipCanary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "bad request; you sent %s", r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)

	_, err := jsonPost(srv.URL+"/x", shipCanaryEnv, shipCanary, map[string]any{"a": 1})
	assertShipRedacted(t, err)
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("the scrub destroyed the diagnostic: %v", err)
	}
}

// --- the enumerating gate ---

// shipBodyIdents are the variable names an HTTP response body is held in across
// this package. A `string(x)` interpolation of any of them is a body snippet.
var shipBodyIdents = map[string]bool{"respBody": true, "rb": true, "body": true, "b": true}

// scrubbingHelpers are the functions where a response body ENTERS the package.
// Each reads the body and scrubs it before returning, so every downstream
// interpolation is safe by construction.
var scrubbingHelpers = map[string]bool{
	"jsonGet":    true,
	"jsonPost":   true,
	"bbRequest":  true,
	"gitlabReq":  true,
	"adoRequest": true,
}

// TestEveryShipBodyInterpolationRedacts enumerates rather than samples.
//
// The per-backend table above proves one site per file. That is not enough: the
// three backends funnel through a shared request helper that returns the raw
// body, so sites like gitlab's "response missing iid" and the two
// reviewer-assignment paths would leak with the table entirely green. This walk
// visits every body interpolation in the package and demands each one's value
// came from a helper that scrubbed it.
func TestEveryShipBodyInterpolationRedacts(t *testing.T) {
	fset := token.NewFileSet()
	files := shipGoFiles(t, fset)

	checked := 0
	for name, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			scrubbed := scrubbedBodyIdents(fn)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isFmtErrorf(call) {
					return true
				}
				for _, arg := range call.Args {
					id, ok := stringConversionOf(arg)
					if !ok || !shipBodyIdents[id] {
						continue
					}
					checked++
					if !scrubbed[id] {
						t.Errorf("%s: %s interpolates string(%s), which does not come from a scrubbing helper (%v) — it may still carry the token",
							fset.Position(arg.Pos()), fn.Name.Name, id, sortedKeys(scrubbingHelpers))
					}
				}
				return true
			})
		}
		_ = name
	}
	// 14 today. A floor rather than an equality so adding a site is allowed —
	// but a walk that silently stopped finding them is not.
	if checked < 14 {
		t.Fatalf("found only %d body interpolations, expected at least 14 — the walk is broken, not the package", checked)
	}
}

// TestShipHelpersScrubAtSource is the other half: the walk above trusts the four
// helpers, so they must actually scrub — and no OTHER function may read a
// response body, or it would bypass them entirely.
func TestShipHelpersScrubAtSource(t *testing.T) {
	fset := token.NewFileSet()
	files := shipGoFiles(t, fset)

	seen := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !readsResponseBody(fn) {
				continue
			}
			if !scrubbingHelpers[fn.Name.Name] {
				t.Errorf("%s: %s reads an HTTP response body outside the scrubbing helpers — route it through one of %v",
					fset.Position(fn.Pos()), fn.Name.Name, sortedKeys(scrubbingHelpers))
				continue
			}
			seen[fn.Name.Name] = true
			if !callsRedactScrub(fn) {
				t.Errorf("%s: %s reads a response body but does not scrub it", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
	for name := range scrubbingHelpers {
		if !seen[name] {
			t.Errorf("scrubbing helper %q was not found reading a body — the list is stale", name)
		}
	}
}

// --- AST helpers ---

func shipGoFiles(t *testing.T, fset *token.FileSet) map[string]*ast.File {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(wd, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		out[name] = f
	}
	if len(out) == 0 {
		t.Fatal("parsed no files in internal/ship — the walk is broken")
	}
	return out
}

// scrubbedBodyIdents returns the identifiers in fn that were assigned from a
// scrubbing helper, plus (inside a helper itself) the ones it scrubs in place.
func scrubbedBodyIdents(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			return true
		}
		switch rhs := as.Rhs[0].(type) {
		case *ast.CallExpr:
			// x, status, err := gitlabReq(...)
			if id, ok := rhs.Fun.(*ast.Ident); ok && scrubbingHelpers[id.Name] {
				if lhs, ok := as.Lhs[0].(*ast.Ident); ok {
					out[lhs.Name] = true
				}
			}
			// x = []byte(redact.Scrub(string(x), ...)) — the in-helper form.
			if usesRedact(rhs) {
				if lhs, ok := as.Lhs[0].(*ast.Ident); ok {
					out[lhs.Name] = true
				}
			}
		}
		return true
	})
	return out
}

func usesRedact(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		sel, ok := x.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "redact" {
			found = true
		}
		return true
	})
	return found
}

func callsRedactScrub(fn *ast.FuncDecl) bool { return usesRedact(fn.Body) }

// readsResponseBody reports whether fn calls io.ReadAll(resp.Body).
func readsResponseBody(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ReadAll" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "io" {
			found = true
		}
		return true
	})
	return found
}

func isFmtErrorf(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "fmt" {
		return false
	}
	return sel.Sel.Name == "Errorf" || sel.Sel.Name == "Sprintf"
}

// stringConversionOf unwraps `string(x)` and returns x's name.
func stringConversionOf(e ast.Expr) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || fn.Name != "string" {
		return "", false
	}
	id, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
