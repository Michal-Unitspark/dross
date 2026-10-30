package ship

import (
	"errors"
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
	"github.com/Rivil/dross/internal/project"
)

const guardEnv = "DROSS_HOSTGUARD_TOKEN"

const guardSentinel = "s3cr3t-ship-sentinel-do-not-leak"

// TestShipRefusesOffAllowlistAPIBase drives every path in this package that
// reads the token: three open backends and two comment backends.
//
// The httptest server is registered as the FAKE host and must record zero
// requests. That assertion is the one that cannot be satisfied by a
// well-worded error: a guard that refuses after the socket is opened produces
// the same message and a request the attacker already has.
func TestShipRefusesOffAllowlistAPIBase(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	// The policy authorizes the repo's own host and nothing else; api_base
	// points at the fake host, exactly as a hostile [remote].api_base would.
	policy := hostallow.Derive("https://forge.example/me/proj", nil)
	base := func(provider string) OpenOpts {
		return OpenOpts{
			Provider: provider,
			URL:      "https://forge.example/me/proj",
			APIBase:  srv.URL,
			AuthEnv:  guardEnv,
			AuthUser: "someone@example.com",
			Hosts:    policy,
			PRNumber: 1,
		}
	}
	comment := func(provider string) CommentOpts {
		return CommentOpts{
			Provider: provider,
			URL:      "https://forge.example/me/proj",
			APIBase:  srv.URL,
			AuthEnv:  guardEnv,
			AuthUser: "someone@example.com",
			Hosts:    policy,
			PRNumber: 1,
			Body:     "hello",
		}
	}

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"forgejo open", func() error { _, err := OpenPR(base("forgejo")); return err }},
		{"gitlab open", func() error { _, err := OpenPR(base("gitlab")); return err }},
		{"bitbucket open", func() error { _, err := OpenPR(base("bitbucket")); return err }},
		{"forgejo comment", func() error { return PostComment(comment("forgejo")) }},
		{"gitlab comment", func() error { return PostComment(comment("gitlab")) }},
		{"bitbucket comment", func() error { return PostComment(comment("bitbucket")) }},
		// Azure DevOps addresses everything from an _git url, so its rows
		// carry one on the authorized host.
		{"azure-devops open", func() error { _, err := OpenPR(adoGuard(base("azure-devops"))); return err }},
		{"azure-devops comment", func() error {
			c := comment("azure-devops")
			c.URL = adoGuardURL
			return PostComment(c)
		}},
		{"azure-devops status", func() error { _, err := GetPRStatus(adoGuard(base("azure-devops"))); return err }},
		{"azure-devops head lookup", func() error {
			_, err := FindOpenPRByHead(adoGuard(base("azure-devops")), "phase/x")
			return err
		}},
		// The identities host is checked on its own, since on Services it is
		// not api_base: a connection whose api_base the policy never saw must
		// still not carry its credential there.
		{"azure-devops identity lookup", func() error {
			repo, _ := project.ParseAzureDevOpsRemote(adoGuardURL)
			c := adoConn{repo: repo, apiBase: srv.URL + "/coll", authEnv: guardEnv, token: guardSentinel, version: adoDefaultAPIVersion}
			_, err := adoResolveIdentity(c, policy, "alice@example.com")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(guardEnv, guardSentinel)
			hits = 0
			err := tc.call()
			if err == nil {
				t.Fatal("an off-allowlist api_base was accepted")
			}
			if !errors.Is(err, hostallow.ErrRefused) {
				t.Errorf("not a policy refusal: %v", err)
			}
			if strings.Contains(err.Error(), guardSentinel) {
				t.Errorf("the refusal echoed the token: %v", err)
			}
			if strings.Contains(err.Error(), guardEnv) {
				t.Errorf("the refusal names the auth env rather than the host: %v", err)
			}
			if hits != 0 {
				t.Errorf("the fake host received %d request(s) — the guard fired after the socket, not before", hits)
			}
		})
	}
}

// adoGuardURL is an Azure Repos remote on the host the guard policy authorizes.
const adoGuardURL = "https://forge.example/coll/proj/_git/proj"

// adoGuard re-points a guard row's opts at adoGuardURL.
func adoGuard(o OpenOpts) OpenOpts {
	o.URL = adoGuardURL
	return o
}

// TestShipAllowsDerivedSelfHostedHost is the over-refusal gate. An honest
// self-hosted forgejo or gitlab install — remote and API on one host — must keep
// working with no machine-local additions at all; if it does not, the guard is
// something users turn off rather than something that protects them.
func TestShipAllowsDerivedSelfHostedHost(t *testing.T) {
	t.Setenv(guardEnv, guardSentinel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":1,"iid":1,"id":1,"pullRequestId":1,"html_url":"https://x/1","links":{"html":{"href":"https://x/1"}}}`))
	}))
	t.Cleanup(srv.Close)

	// remote and api_base share the server's host, so the derivation reaches it.
	for _, provider := range []string{"forgejo", "gitlab"} {
		if _, err := OpenPR(OpenOpts{
			Provider:   provider,
			URL:        srv.URL + "/me/proj",
			APIBase:    srv.URL,
			AuthEnv:    guardEnv,
			Hosts:      hostallow.Derive(srv.URL, nil),
			HeadBranch: "phase/x",
			BaseBranch: "main",
			Title:      "t",
		}); err != nil && errors.Is(err, hostallow.ErrRefused) {
			t.Errorf("%s: an honest self-hosted install was refused: %v", provider, err)
		}
	}

	// An Azure DevOps Server: the collection is api_base, the remote hangs
	// off it, and both sit on the one derived host.
	res, err := OpenPR(OpenOpts{
		Provider:   "azure-devops",
		URL:        srv.URL + "/coll/P/_git/R",
		APIBase:    srv.URL + "/coll",
		AuthEnv:    guardEnv,
		Hosts:      hostallow.Derive(srv.URL+"/coll/P/_git/R", nil),
		HeadBranch: "phase/x",
		BaseBranch: "main",
		Title:      "t",
	})
	if err != nil || res == nil || res.Number != 1 {
		t.Errorf("azure-devops Server: an honest self-hosted collection did not open its PR: %+v, %v", res, err)
	}
}

// TestNoGetenvOutsideHostguard is the structural half of this task.
//
// Five separate call sites across four backends used to read the token inline,
// and a guard added to four of them is a guard with a hole. This walks the
// package's non-test sources and fails, by file:line, on any `os.Getenv(`
// outside hostguard.go — so "did the new backend check the host?" is answered
// by the compiler-adjacent check rather than by whoever reviews the PR.
//
// It matches the BARE call, not `opts.AuthEnv`: bitbucket.go reads through a
// local variable, and a narrower pattern would have missed it — which is how
// the next one gets missed too.
func TestNoGetenvOutsideHostguard(t *testing.T) {
	const exempt = "hostguard.go" // where the one guarded read lives

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	scanned := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == exempt {
			continue
		}
		scanned++
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Getenv" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "os" {
				return true
			}
			offenders = append(offenders, fset.Position(call.Pos()).String())
			return true
		})
	}

	if scanned == 0 {
		t.Fatal("scanned no files — the audit would pass vacuously")
	}
	if len(offenders) > 0 {
		t.Errorf("os.Getenv outside %s — route the read through resolveToken so the host is checked first:\n  %s",
			exempt, strings.Join(offenders, "\n  "))
	}
}

// TestHostguardChecksBeforeReadingEnv pins resolveToken's internal order the
// same way the forge package does: with the host refused AND the env var unset,
// the error must be the refusal. "$X is not set" would prove the Getenv ran.
func TestHostguardChecksBeforeReadingEnv(t *testing.T) {
	const unset = "DROSS_DEFINITELY_UNSET_SHIPGUARD"
	if _, ok := os.LookupEnv(unset); ok {
		t.Skipf("%s is set in this environment", unset)
	}
	_, err := resolveToken("https://attacker.example", unset,
		hostallow.Derive("https://forge.example/me/proj", nil))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "is not set") {
		t.Errorf("the token was read before the host was checked: %v", err)
	}
	if !errors.Is(err, hostallow.ErrRefused) {
		t.Errorf("not the host refusal: %v", err)
	}
}
