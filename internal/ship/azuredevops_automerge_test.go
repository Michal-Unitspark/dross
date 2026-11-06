package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// adoArmUser is the identity connectionData names.
const adoArmUser = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"

// adoArmMock answers a chore PR's reads in sequence (the last repeats), the
// repository and policy reads, connectionData and the arm PATCH, recording
// every request.
type adoArmMock struct {
	*httptest.Server
	mu          sync.Mutex
	calls       []adoCall
	prs         []string
	reads       int
	policies    string
	policyCode  int
	patchStatus int
	patchBody   string
}

func newADOArmMock(t *testing.T, prs ...string) *adoArmMock {
	t.Helper()
	m := &adoArmMock{prs: prs, policies: policyList(), policyCode: 200, patchStatus: 200, patchBody: `{}`}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.calls = append(m.calls, adoCall{r.Method, r.URL.EscapedPath(), r.URL.Query(), body})
		w.Header().Set("Content-Type", "application/json")
		low := strings.ToLower(r.URL.Path)
		switch {
		case r.Method == "GET" && low == "/o/p/_apis/git/repositories/r/pullrequests/41":
			i := m.reads
			if i >= len(m.prs) {
				i = len(m.prs) - 1
			}
			m.reads++
			_, _ = w.Write([]byte(m.prs[i]))
		case r.Method == "PATCH" && low == "/o/p/_apis/git/repositories/r/pullrequests/41":
			w.WriteHeader(m.patchStatus)
			_, _ = w.Write([]byte(m.patchBody))
		case r.Method == "GET" && low == "/o/p/_apis/git/repositories/r":
			fmt.Fprintf(w, `{"id":%q,"name":"r","defaultBranch":"refs/heads/main"}`, adoPolicyRepoID)
		case r.Method == "GET" && low == "/o/p/_apis/policy/configurations":
			w.WriteHeader(m.policyCode)
			_, _ = w.Write([]byte(m.policies))
		case r.Method == "GET" && low == "/o/_apis/connectiondata":
			_, _ = w.Write([]byte(adoConnectionData(adoArmUser, "dross@example.com")))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.Close)
	// Whatever a case does, no body it sent may bypass a policy.
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, c := range m.calls {
			if v, ok := c.body["completionOptions"].(map[string]any); ok && v["bypassPolicy"] == true {
				t.Errorf("%s %s carries bypassPolicy true", c.method, c.path)
			}
			if findKey(c.body, "bypassReason") {
				t.Errorf("%s %s carries a bypassReason", c.method, c.path)
			}
		}
	})
	return m
}

func (m *adoArmMock) patches() []adoCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []adoCall
	for _, c := range m.calls {
		if c.method == "PATCH" {
			out = append(out, c)
		}
	}
	return out
}

func (m *adoArmMock) total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// choreOpts addresses m for the fixture repo, token set.
func choreOpts(t *testing.T, m *adoArmMock) OpenOpts {
	t.Helper()
	t.Setenv(adoTestEnv, adoTestTok)
	opts := adoTestOpts(m.Server)
	opts.Provider = "azure-devops"
	return opts
}

// chorePR is a PR read: status, target, and the arming identity ("" = none).
func chorePR(status, armedBy string) string {
	pr := map[string]any{
		"pullRequestId": 41,
		"status":        status,
		"sourceRefName": "refs/heads/dross-chores/main",
		"targetRefName": "refs/heads/main",
	}
	if armedBy != "" {
		pr["autoCompleteSetBy"] = map[string]any{"id": armedBy, "displayName": "dross"}
	}
	b, _ := json.Marshal(pr)
	return string(b)
}

// mergeStrategy is a blocking "Require a merge strategy" policy on main with
// the given settings.
func mergeStrategy(settings map[string]any) policyJSON {
	p := blocking(scope("refs/heads/main", "Exact", adoPolicyRepoID))
	p.typeID, p.typeName = adoMergeStrategyPolicyType, "Require a merge strategy"
	p.settings = settings
	return p
}

// TestADOArmChoreAutoComplete: one PATCH arms auto-complete as dross's own
// identity with a merge commit and nothing that bypasses a policy, and the
// outcome is read back.
func TestADOArmChoreAutoComplete(t *testing.T) {
	for name, policies := range map[string]string{
		"no merge-strategy policy":           policyList(),
		"a policy that allows merge commits": policyList(mergeStrategy(map[string]any{"allowNoFastForward": true, "allowSquash": true})),
		"a forbidding policy on another branch": policyList(func() policyJSON {
			p := mergeStrategy(map[string]any{"allowSquash": true})
			p.scopes = []map[string]any{scope("refs/heads/release", "Exact", adoPolicyRepoID)}
			return p
		}()),
		"a forbidding policy that is not blocking": policyList(func() policyJSON {
			p := mergeStrategy(map[string]any{"allowSquash": true})
			p.blocking = false
			return p
		}()),
	} {
		t.Run(name, func(t *testing.T) {
			m := newADOArmMock(t, chorePR("active", ""), chorePR("active", adoArmUser))
			m.policies = policies
			got, err := AutoMergePR(choreOpts(t, m), 41, "merge")
			if err != nil || !got.AutoEnabled || got.Merged {
				t.Fatalf("AutoMergePR = %+v, %v; want AutoEnabled", got, err)
			}
			ps := m.patches()
			if len(ps) != 1 || !strings.HasSuffix(strings.ToLower(ps[0].path), "/pullrequests/41") {
				t.Fatalf("want exactly one PATCH to .../pullrequests/41, got %+v", ps)
			}
			b := ps[0].body
			if set, _ := b["autoCompleteSetBy"].(map[string]any); set["id"] != adoArmUser {
				t.Errorf("autoCompleteSetBy = %v, want id %s (connectionData's authenticatedUser)", b["autoCompleteSetBy"], adoArmUser)
			}
			co, _ := b["completionOptions"].(map[string]any)
			if co["mergeStrategy"] != "noFastForward" {
				t.Errorf("mergeStrategy = %v, want noFastForward", co["mergeStrategy"])
			}
			if bp, ok := co["bypassPolicy"]; ok && bp != false {
				t.Errorf("bypassPolicy = %v, want absent or false", bp)
			}
			if findKey(b, "status") {
				t.Error("the arm sets status: that completes the PR now instead of arming it")
			}
		})
	}
}

// TestADOArmRefusesForbiddenMergeCommit: a merge commit the target's policy
// forbids, or a policy list that cannot be read, is ErrAutoMergeUnavailable
// before anything is written.
func TestADOArmRefusesForbiddenMergeCommit(t *testing.T) {
	for name, set := range map[string]func(*adoArmMock){
		"allowNoFastForward false": func(m *adoArmMock) {
			m.policies = policyList(mergeStrategy(map[string]any{"allowNoFastForward": false, "allowSquash": true}))
		},
		"squash only, modern form": func(m *adoArmMock) {
			m.policies = policyList(mergeStrategy(map[string]any{"allowSquash": true}))
		},
		"squash only, legacy form": func(m *adoArmMock) {
			m.policies = policyList(mergeStrategy(map[string]any{"useSquashMerge": true}))
		},
		"policy list unreadable": func(m *adoArmMock) { m.policyCode, m.policies = 500, `oops` },
		"policy list not a list": func(m *adoArmMock) { m.policies = `{"count":0}` },
	} {
		t.Run(name, func(t *testing.T) {
			m := newADOArmMock(t, chorePR("active", ""))
			set(m)
			got, err := AutoMergePR(choreOpts(t, m), 41, "merge")
			if !errors.Is(err, ErrAutoMergeUnavailable) || got.AutoEnabled || got.Merged {
				t.Fatalf("AutoMergePR = %+v, %v; want ErrAutoMergeUnavailable", got, err)
			}
			if n := len(m.patches()); n != 0 {
				t.Errorf("%d PATCH(es) sent, want none", n)
			}
		})
	}
}

// TestADOArmRefusalStatuses: Azure DevOps refusing the arm (400, 403) is
// ErrAutoMergeUnavailable; a server failure is a plain error.
func TestADOArmRefusalStatuses(t *testing.T) {
	for status, wantUnavailable := range map[int]bool{400: true, 403: true, 500: false} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			m := newADOArmMock(t, chorePR("active", ""))
			m.patchStatus, m.patchBody = status, `{"message":"refused"}`
			_, err := AutoMergePR(choreOpts(t, m), 41, "merge")
			if err == nil {
				t.Fatal("a failed arm reported success")
			}
			if errors.Is(err, ErrAutoMergeUnavailable) != wantUnavailable {
				t.Errorf("HTTP %d: errors.Is(ErrAutoMergeUnavailable) = %v, want %v (%v)", status, !wantUnavailable, wantUnavailable, err)
			}
		})
	}
}

// TestADOArmOutcomeReadBack: what the arm achieved is read from the PR, never
// assumed from the PATCH's 200.
func TestADOArmOutcomeReadBack(t *testing.T) {
	for _, tc := range []struct {
		name      string
		patchBody string
		readBack  string
		want      AutoMergeResult
		// wantManual: the arm did not take, so the PR is left for a human —
		// ErrAutoMergeUnavailable, which chore PRs narrate as "by hand".
		wantManual bool
		wantErr    bool
	}{
		{"completed at once", `{}`, chorePR("completed", ""), AutoMergeResult{Merged: true}, false, false},
		{"armed", `{}`, chorePR("active", adoArmUser), AutoMergeResult{AutoEnabled: true}, false, false},
		{"active and not armed", `{}`, chorePR("active", ""), AutoMergeResult{}, true, true},
		{"a 200 PATCH answer without autoCompleteSetBy", chorePR("active", ""), chorePR("active", ""), AutoMergeResult{}, true, true},
		{"a 200 PATCH answer claiming armed, read back unarmed", chorePR("active", adoArmUser), chorePR("active", ""), AutoMergeResult{}, true, true},
		{"abandoned under the arm", `{}`, chorePR("abandoned", ""), AutoMergeResult{}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newADOArmMock(t, chorePR("active", ""), tc.readBack)
			m.patchBody = tc.patchBody
			got, err := AutoMergePR(choreOpts(t, m), 41, "merge")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("AutoMergePR = %+v, %v; want %+v (error %v)", got, err, tc.want, tc.wantErr)
			}
			if errors.Is(err, ErrAutoMergeUnavailable) != tc.wantManual {
				t.Errorf("errors.Is(ErrAutoMergeUnavailable) = %v, want %v (%v)", !tc.wantManual, tc.wantManual, err)
			}
		})
	}

	t.Run("already completed: no write", func(t *testing.T) {
		m := newADOArmMock(t, chorePR("completed", ""))
		got, err := AutoMergePR(choreOpts(t, m), 41, "merge")
		if err != nil || !got.Merged {
			t.Fatalf("AutoMergePR = %+v, %v; want Merged", got, err)
		}
		if n := len(m.patches()); n != 0 {
			t.Errorf("%d PATCH(es) on a completed PR, want none", n)
		}
	})
	t.Run("abandoned: no write", func(t *testing.T) {
		m := newADOArmMock(t, chorePR("abandoned", ""))
		if _, err := AutoMergePR(choreOpts(t, m), 41, "merge"); err == nil {
			t.Fatal("an abandoned PR was armed")
		}
		if n := len(m.patches()); n != 0 {
			t.Errorf("%d PATCH(es) on an abandoned PR, want none", n)
		}
	})
}

// TestADOArmDispatch: azure-devops arms only a merge commit, and a GitHub
// remote still reaches gh without touching an ADO host.
func TestADOArmDispatch(t *testing.T) {
	m := newADOArmMock(t, chorePR("active", ""))
	for _, method := range []string{"squash", "rebase", ""} {
		if _, err := AutoMergePR(choreOpts(t, m), 41, method); err == nil {
			t.Errorf("method %q was armed on azure-devops", method)
		}
	}
	if n := m.total(); n != 0 {
		t.Errorf("refused methods made %d request(s), want none", n)
	}

	calls := scriptGh(t, ghReply{}, ghReply{out: viewArmed})
	gh := OpenOpts{Provider: "github", APIBase: m.URL + "/o", AuthEnv: adoTestEnv}
	got, err := AutoMergePR(gh, 12, "merge")
	if err != nil || !got.AutoEnabled {
		t.Fatalf("github AutoMergePR = %+v, %v; want AutoEnabled via gh", got, err)
	}
	if len(*calls) != 2 || (*calls)[0][0] != "pr" || (*calls)[0][1] != "merge" {
		t.Errorf("gh calls = %v, want gh pr merge then gh pr view", *calls)
	}
	if n := m.total(); n != 0 {
		t.Errorf("a github auto-merge sent %d request(s) to the ADO host, want none", n)
	}

	if _, err := AutoMergePR(OpenOpts{Provider: "gitlab"}, 3, "merge"); err == nil {
		t.Error("gitlab was armed")
	}
}
