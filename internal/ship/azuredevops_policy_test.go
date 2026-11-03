package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

// adoPolicyRepoID is the fixture repository's GUID.
const adoPolicyRepoID = "3a1c7f00-0000-4000-8000-00000000abcd"

// adoPolicyMethods records the method of every request a policy fixture
// served, across every test that uses one: the read-only claim is about all of
// them.
var (
	adoPolicyMu      sync.Mutex
	adoPolicyMethods []string
)

// adoPolicyServer serves the fixture repository and answers the policy list
// with status and body.
func adoPolicyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adoPolicyMu.Lock()
		adoPolicyMethods = append(adoPolicyMethods, r.Method+" "+r.URL.Path)
		adoPolicyMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/o/p/_apis/git/repositories/r":
			fmt.Fprintf(w, `{"id":%q,"name":"r","defaultBranch":"refs/heads/main"}`, adoPolicyRepoID)
		case "/o/p/_apis/policy/configurations":
			if got := r.URL.Query().Get("$top"); got != fmt.Sprint(adoPolicyListTop) {
				t.Errorf("$top = %q, want %d", got, adoPolicyListTop)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// adoPolicyOpts addresses srv for the fixture repo, token set.
func adoPolicyOpts(t *testing.T, srv *httptest.Server) OpenOpts {
	t.Helper()
	t.Setenv(adoTestEnv, adoTestTok)
	return adoTestOpts(srv)
}

// policyJSON is one policy configuration in Azure DevOps' wire shape.
type policyJSON struct {
	enabled, blocking, deleted bool
	typeID, typeName           string
	scopes                     []map[string]any
	settings                   map[string]any
}

func (p policyJSON) wire(id int) map[string]any {
	settings := map[string]any{"scope": p.scopes}
	for k, v := range p.settings {
		settings[k] = v
	}
	typeID, typeName := p.typeID, p.typeName
	if typeID == "" {
		typeID, typeName = "fa4e907d-c16b-4a4c-9dfa-4916e5d171aa", "Minimum number of reviewers"
	}
	return map[string]any{
		"id":         id,
		"isEnabled":  p.enabled,
		"isBlocking": p.blocking,
		"isDeleted":  p.deleted,
		"type":       map[string]any{"id": typeID, "displayName": typeName},
		"settings":   settings,
	}
}

// policyList is the configurations answer for ps.
func policyList(ps ...policyJSON) string {
	vals := []any{}
	for i, p := range ps {
		vals = append(vals, p.wire(i+1))
	}
	b, _ := json.Marshal(map[string]any{"count": len(vals), "value": vals})
	return string(b)
}

// scope is one settings.scope entry; a nil refName or repoID is JSON null.
func scope(refName any, matchKind string, repoID any) map[string]any {
	m := map[string]any{"refName": refName, "repositoryId": repoID}
	if matchKind != "" {
		m["matchKind"] = matchKind
	}
	return m
}

// blocking is an enabled, blocking, live policy with the given scopes.
func blocking(scopes ...map[string]any) policyJSON {
	return policyJSON{enabled: true, blocking: true, scopes: scopes}
}

// TestADOBranchProtectionScopes: a branch is protected exactly when an
// enabled, blocking, live policy's scope covers it in this repository.
func TestADOBranchProtectionScopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy policyJSON
		branch string
		want   bool
	}{
		{"exact main, this repo", blocking(scope("refs/heads/main", "Exact", adoPolicyRepoID)), "main", true},
		{"exact main, refs/heads form", blocking(scope("refs/heads/main", "Exact", adoPolicyRepoID)), "refs/heads/main", true},
		{"prefix milestone/, every repo", blocking(scope("refs/heads/milestone/", "Prefix", nil)), "milestone/v1.8", true},
		{"exact refs/heads/Main protects main", blocking(scope("refs/heads/Main", "Exact", adoPolicyRepoID)), "main", true},
		{"repo id compared case-insensitively", blocking(scope("refs/heads/main", "Exact", strings.ToUpper(adoPolicyRepoID))), "main", true},
		{"no matchKind is exact", blocking(scope("refs/heads/main", "", adoPolicyRepoID)), "main", true},
		{"default branch protects main", blocking(scope(nil, "DefaultBranch", adoPolicyRepoID)), "main", true},
		{"default branch, every repo", blocking(scope(nil, "DefaultBranch", nil)), "main", true},
		{"default branch does not protect milestone", blocking(scope(nil, "DefaultBranch", adoPolicyRepoID)), "milestone/v1.8", false},
		{"not blocking", policyJSON{enabled: true, scopes: []map[string]any{scope("refs/heads/main", "Exact", adoPolicyRepoID)}}, "main", false},
		{"not enabled", policyJSON{blocking: true, scopes: []map[string]any{scope("refs/heads/main", "Exact", adoPolicyRepoID)}}, "main", false},
		{"deleted", policyJSON{enabled: true, blocking: true, deleted: true, scopes: []map[string]any{scope("refs/heads/main", "Exact", adoPolicyRepoID)}}, "main", false},
		{"another repository", blocking(scope("refs/heads/main", "Exact", "99999999-0000-4000-8000-000000000000")), "main", false},
		{"repo-wide scope with no refName", blocking(scope(nil, "", adoPolicyRepoID)), "main", false},
		{"exact main against milestone", blocking(scope("refs/heads/main", "Exact", adoPolicyRepoID)), "milestone/v1.8", false},
		{"prefix milestone/ against main", blocking(scope("refs/heads/milestone/", "Prefix", nil)), "main", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := adoPolicyServer(t, http.StatusOK, policyList(tc.policy))
			got := ADOBranchProtection(adoPolicyOpts(t, srv), tc.branch)
			if !got.Known {
				t.Fatalf("unknown: %s", got.Reason)
			}
			if got.Protected != tc.want {
				t.Errorf("Protected = %v, want %v (policies %v)", got.Protected, tc.want, got.Policies)
			}
			if tc.want && len(got.Policies) != 1 {
				t.Errorf("Policies = %v, want the one policy named", got.Policies)
			}
		})
	}

	t.Run("no policies at all", func(t *testing.T) {
		srv := adoPolicyServer(t, http.StatusOK, `{"count":0,"value":[]}`)
		if got := ADOBranchProtection(adoPolicyOpts(t, srv), "main"); !got.Known || got.Protected {
			t.Errorf("an empty list = %+v, want known and unprotected", got)
		}
	})
	t.Run("no branch name", func(t *testing.T) {
		srv := adoPolicyServer(t, http.StatusOK, `{"count":0,"value":[]}`)
		if got := ADOBranchProtection(adoPolicyOpts(t, srv), "  "); got.Known {
			t.Errorf("an empty branch = %+v, want unknown", got)
		}
	})
}

// TestADOBranchProtectionUnreadableIsUnknown: a policy read that fails, or
// answers with something other than the complete list, is unknown with a
// reason naming the policy read — never a known "unprotected".
func TestADOBranchProtectionUnreadableIsUnknown(t *testing.T) {
	full := make([]policyJSON, adoPolicyListTop)
	for i := range full {
		full[i] = policyJSON{enabled: true, blocking: true}
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"401", http.StatusUnauthorized, `{"message":"no"}`},
		{"500", http.StatusInternalServerError, `oops`},
		{"203 sign-in page", http.StatusNonAuthoritativeInfo, `<html>Sign in</html>`},
		{"non-JSON", http.StatusOK, `<html>not json</html>`},
		{"no value", http.StatusOK, `{"count":0}`},
		{"full $top page", http.StatusOK, policyList(full...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := adoPolicyServer(t, tc.status, tc.body)
			got := ADOBranchProtection(adoPolicyOpts(t, srv), "main")
			if got.Known {
				t.Fatalf("an unreadable list was Known (Protected=%v)", got.Protected)
			}
			if got.Protected {
				t.Error("an unknown read reported Protected")
			}
			if !strings.Contains(got.Reason, "policy configurations") || got.Err == nil {
				t.Errorf("reason does not name the policy read (or Err is nil): %q / %v", got.Reason, got.Err)
			}
		})
	}
}

// TestADOMergeCommitAllowed: a blocking merge-strategy policy on the target
// decides whether a merge commit (noFastForward) may complete a PR there.
func TestADOMergeCommitAllowed(t *testing.T) {
	mergeStrategy := func(settings map[string]any, scopes ...map[string]any) policyJSON {
		return policyJSON{enabled: true, blocking: true, typeID: adoMergeStrategyPolicyType,
			typeName: "Require a merge strategy", scopes: scopes, settings: settings}
	}
	onMain := scope("refs/heads/main", "Exact", adoPolicyRepoID)
	for _, tc := range []struct {
		name string
		list string
		want bool
	}{
		{"allowNoFastForward false", policyList(mergeStrategy(map[string]any{"allowSquash": true, "allowNoFastForward": false}, onMain)), false},
		{"allowNoFastForward true", policyList(mergeStrategy(map[string]any{"allowSquash": true, "allowNoFastForward": true}, onMain)), true},
		{"modern form listing squash alone", policyList(mergeStrategy(map[string]any{"allowSquash": true}, onMain)), false},
		{"modern form listing rebase alone", policyList(mergeStrategy(map[string]any{"allowRebase": true, "useSquashMerge": false}, onMain)), false},
		{"legacy squash only", policyList(mergeStrategy(map[string]any{"useSquashMerge": true}, onMain)), false},
		{"legacy no squash", policyList(mergeStrategy(map[string]any{"useSquashMerge": false}, onMain)), true},
		{"no such policy", policyList(blocking(onMain)), true},
		{"scoped to another branch", policyList(mergeStrategy(map[string]any{"allowNoFastForward": false},
			scope("refs/heads/develop", "Exact", adoPolicyRepoID))), true},
		{"not blocking", policyList(policyJSON{enabled: true, typeID: adoMergeStrategyPolicyType,
			scopes: []map[string]any{onMain}, settings: map[string]any{"allowNoFastForward": false}}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := adoPolicyServer(t, http.StatusOK, tc.list)
			c, err := adoOpenConn(adoPolicyOpts(t, srv))
			if err != nil {
				t.Fatal(err)
			}
			allowed, known, reason := adoMergeCommitAllowed(c, "main")
			if !known {
				t.Fatalf("unknown: %s", reason)
			}
			if allowed != tc.want {
				t.Errorf("allowed = %v, want %v", allowed, tc.want)
			}
		})
	}
	t.Run("unreadable list", func(t *testing.T) {
		srv := adoPolicyServer(t, http.StatusInternalServerError, `oops`)
		c, err := adoOpenConn(adoPolicyOpts(t, srv))
		if err != nil {
			t.Fatal(err)
		}
		if allowed, known, reason := adoMergeCommitAllowed(c, "main"); known || allowed || reason == "" {
			t.Errorf("an unreadable list = allowed %v known %v reason %q, want unknown with a reason", allowed, known, reason)
		}
	})
}

// TestADOBranchProtectionReadOnly: dross only reads policy configurations
// (locked ado_branch_protection). Every request any policy fixture served —
// across the scope, failure and merge-strategy tests, run here again so the
// claim holds whatever -run selects — is a GET.
func TestADOBranchProtectionReadOnly(t *testing.T) {
	TestADOBranchProtectionScopes(t)
	TestADOBranchProtectionUnreadableIsUnknown(t)
	TestADOMergeCommitAllowed(t)
	adoPolicyMu.Lock()
	defer adoPolicyMu.Unlock()
	if len(adoPolicyMethods) == 0 {
		t.Fatal("no request was recorded — the read-only claim is vacuous")
	}
	for _, m := range adoPolicyMethods {
		if !strings.HasPrefix(m, "GET ") {
			t.Errorf("a policy read sent %s — dross never writes a policy", m)
		}
	}
}

// TestADOBranchProtectionRefusesHostBeforeRead: a refused api_base is unknown,
// its Err the policy refusal, before any request.
func TestADOBranchProtectionRefusesHostBeforeRead(t *testing.T) {
	srv := adoPolicyServer(t, http.StatusOK, `{"count":0,"value":[]}`)
	opts := adoPolicyOpts(t, srv)
	opts.Hosts = hostallow.Derive("https://forge.example/o/p/_git/r", nil)
	adoPolicyMu.Lock()
	before := len(adoPolicyMethods)
	adoPolicyMu.Unlock()
	got := ADOBranchProtection(opts, "main")
	if got.Known || !errors.Is(got.Err, hostallow.ErrRefused) {
		t.Fatalf("a refused host = %+v, want unknown with the policy refusal as Err", got)
	}
	adoPolicyMu.Lock()
	after := len(adoPolicyMethods)
	adoPolicyMu.Unlock()
	if after != before {
		t.Errorf("the refused host saw %d request(s)", after-before)
	}
}
