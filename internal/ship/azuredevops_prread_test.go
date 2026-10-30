package ship

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// adoPRBody is a pullRequests/<n> answer with status, source and an optional
// extra field spliced in.
func adoPRBody(status, source, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"pullRequestId":7,"status":"` + status + `","sourceRefName":"` + source + `","targetRefName":"refs/heads/main"` + extra + `}`
}

func readADOHead(t *testing.T, status int, body string) (PRHead, error) {
	t.Helper()
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, status, body)
	return azureDevOpsPRHead(adoTestOpts(rec.Server), 7)
}

// TestADOPRHeadOpen: open is status active in any case; completed and
// abandoned are closed; a status dross cannot read is an error, never a quiet
// "closed".
func TestADOPRHeadOpen(t *testing.T) {
	for status, open := range map[string]bool{"active": true, "Active": true, "completed": false, "abandoned": false} {
		got, err := readADOHead(t, 200, adoPRBody(status, "refs/heads/phase/x", ""))
		if err != nil || got.Open != open {
			t.Errorf("%s: got %+v, %v; want Open %v", status, got, err, open)
		}
	}
	for name, body := range map[string]string{
		"empty status":   adoPRBody("", "refs/heads/phase/x", ""),
		"missing status": `{"sourceRefName":"refs/heads/phase/x"}`,
		"unknown status": adoPRBody("notSet", "refs/heads/phase/x", ""),
	} {
		if got, err := readADOHead(t, 200, body); err == nil {
			t.Errorf("%s: got %+v with a nil error", name, got)
		}
	}
}

// TestADOPRHeadRef: refs/heads/ comes off once, so a nested branch survives;
// a source that is not a branch is an error.
func TestADOPRHeadRef(t *testing.T) {
	for source, want := range map[string]string{"refs/heads/phase/x": "phase/x", "refs/heads/phase/a/b": "phase/a/b"} {
		got, err := readADOHead(t, 200, adoPRBody("active", source, ""))
		if err != nil || got.Ref != want {
			t.Errorf("%s: got %+v, %v; want Ref %q", source, got, err, want)
		}
	}
	for _, source := range []string{"", "refs/pull/3/merge", "refs/heads/"} {
		if got, err := readADOHead(t, 200, adoPRBody("active", source, "")); err == nil {
			t.Errorf("source %q: got %+v with a nil error", source, got)
		}
	}
	got, err := readADOHead(t, 200, adoPRBody("active", "refs/heads/phase/x", ""))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://dev.azure.com/o/p/_git/r/pullrequest/7"; got.URL != want {
		t.Errorf("URL = %q, want %q", got.URL, want)
	}
}

// TestADOPRHeadFork: a set forkSource is a cross-repo head; null or absent is
// this repo's own branch. No number means no request.
func TestADOPRHeadFork(t *testing.T) {
	for extra, cross := range map[string]bool{
		`"forkSource":{"name":"refs/heads/phase/x","repository":{"id":"f"}}`: true,
		`"forkSource":null`: false,
		"":                  false,
	} {
		got, err := readADOHead(t, 200, adoPRBody("active", "refs/heads/phase/x", extra))
		if err != nil || got.CrossRepo != cross {
			t.Errorf("%q: got %+v, %v; want CrossRepo %v", extra, got, err, cross)
		}
	}

	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 200, adoPRBody("active", "refs/heads/phase/x", ""))
	if _, err := azureDevOpsPRHead(adoTestOpts(rec.Server), 0); err == nil {
		t.Error("PR number 0 was accepted")
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out without a PR number", n)
	}
}

// adoListPR is one record in a pullrequests list.
func adoListPR(id int, status, source, target, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"pullRequestId":` + strconv.Itoa(id) + `,"title":"PR ` + strconv.Itoa(id) + `","status":"` + status +
		`","sourceRefName":"` + source + `","targetRefName":"` + target + `"` + extra + `}`
}

func adoList(prs ...string) string {
	return `{"value":[` + strings.Join(prs, ",") + `],"count":` + strconv.Itoa(len(prs)) + `}`
}

// TestADOOpenPRsTargeting: the server filter is sent and re-checked, so a PR
// into another branch or an abandoned one never reads as a dependent; a fork's
// PR into the base still does.
func TestADOOpenPRsTargeting(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for _, base := range []string{"milestone/v1.8", "milestone/a&b+c"} {
		target := "refs/heads/" + base
		rec := newADORecorder(t, 200, adoList(
			adoListPR(3, "active", "refs/heads/phase/x", target, ""),
			adoListPR(4, "active", "refs/heads/phase/y", "refs/heads/main", ""),
			adoListPR(5, "abandoned", "refs/heads/phase/z", target, ""),
			adoListPR(6, "active", "refs/heads/phase/w", target, `"forkSource":{"name":"refs/heads/phase/w"}`),
		))
		got, err := azureDevOpsOpenPRsTargeting(adoTestOpts(rec.Server), base)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if q := rec.query["searchCriteria.targetRefName"]; len(q) != 1 || q[0] != target {
			t.Errorf("%s: targetRefName = %v, want [%s]", base, q, target)
		}
		if q := rec.query["searchCriteria.status"]; len(q) != 1 || q[0] != "active" {
			t.Errorf("%s: status = %v, want [active]", base, q)
		}
		want := []BasePR{
			{Number: 3, Title: "PR 3", URL: "https://dev.azure.com/o/p/_git/r/pullrequest/3", HeadRefName: "phase/x"},
			{Number: 6, Title: "PR 6", URL: "https://dev.azure.com/o/p/_git/r/pullrequest/6", HeadRefName: "phase/w"},
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %+v, want %+v", base, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: [%d] = %+v, want %+v", base, i, got[i], want[i])
			}
		}
	}

	rec := newADORecorder(t, 200, adoList())
	if _, err := azureDevOpsOpenPRsTargeting(adoTestOpts(rec.Server), ""); err == nil {
		t.Error("an empty base was accepted")
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out without a base", n)
	}
}

// TestADOOpenPRsTargetingFailsClosed: a failed read is an error, never the
// empty list that would read as "no dependents"; a genuinely empty list is.
func TestADOOpenPRsTargetingFailsClosed(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	target := "refs/heads/milestone/v1"
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"500":              {500, `{"message":"boom"}`},
		"203 sign-in page": {http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`},
		"non-JSON body":    {200, `<html>nope</html>`},
		"no value array":   {200, `{"count":0}`},
		"pullRequestId 0":  {200, adoList(adoListPR(0, "active", "refs/heads/phase/x", target, ""))},
	} {
		rec := newADORecorder(t, tc.status, tc.body)
		got, err := azureDevOpsOpenPRsTargeting(adoTestOpts(rec.Server), "milestone/v1")
		if err == nil || got != nil {
			t.Errorf("%s: got %+v, %v; want (nil, err)", name, got, err)
		}
	}

	rec := newADORecorder(t, 200, adoList())
	got, err := azureDevOpsOpenPRsTargeting(adoTestOpts(rec.Server), "milestone/v1")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty list: got %#v, %v; want (empty, nil)", got, err)
	}
}

// TestADOOpenPRsTargetingFullPage: a full page with no match may have cut the
// dependents off and is an error; a full page with one still refuses on it.
func TestADOOpenPRsTargetingFullPage(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	target := "refs/heads/milestone/v1"
	full := func(match bool) string {
		var prs []string
		for i := 1; i <= adoPRListTop; i++ {
			into := "refs/heads/main"
			if match && i == 50 {
				into = target
			}
			prs = append(prs, adoListPR(i, "active", "refs/heads/f/"+strconv.Itoa(i), into, ""))
		}
		return adoList(prs...)
	}
	rec := newADORecorder(t, 200, full(false))
	if got, err := azureDevOpsOpenPRsTargeting(adoTestOpts(rec.Server), "milestone/v1"); err == nil {
		t.Errorf("full page without a match: got %+v with a nil error", got)
	}
	rec = newADORecorder(t, 200, full(true))
	got, err := azureDevOpsOpenPRsTargeting(adoTestOpts(rec.Server), "milestone/v1")
	if err != nil || len(got) != 1 || got[0].Number != 50 {
		t.Errorf("full page with a match: got %+v, %v; want PR 50", got, err)
	}
	if q := rec.query["$top"]; len(q) != 1 || q[0] != "100" {
		t.Errorf("$top = %v, want [100]", q)
	}
}
