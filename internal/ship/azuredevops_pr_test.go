package ship

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

func adoStatusOpts(apiBase string, n int) OpenOpts {
	return OpenOpts{
		URL:      adoTestURL,
		APIBase:  apiBase,
		AuthEnv:  adoTestEnv,
		Hosts:    hostallow.Derive(apiBase, nil),
		PRNumber: n,
	}
}

// TestAzureDevOpsPRStatusMapping: merged is status == completed, in any case;
// abandoned and active are both not merged.
func TestAzureDevOpsPRStatusMapping(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for status, merged := range map[string]bool{
		"completed": true,
		"Completed": true,
		"abandoned": false,
		"active":    false,
	} {
		rec := newADORecorder(t, 200, `{"status":"`+status+`","targetRefName":"refs/heads/main"}`)
		got, err := azureDevOpsPRStatus(adoStatusOpts(rec.URL, 5))
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if got.Merged != merged || got.BaseRef != "main" {
			t.Errorf("%s: got %+v, want Merged=%v BaseRef=main", status, got, merged)
		}
	}
}

// TestAzureDevOpsPRStatusUnknownIsAnError: a status dross cannot read is an
// error, never a quiet "not merged" — and no number means no request.
func TestAzureDevOpsPRStatusUnknownIsAnError(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"empty status":     {200, `{"status":"","targetRefName":"refs/heads/main"}`},
		"missing status":   {200, `{"targetRefName":"refs/heads/main"}`},
		"unknown status":   {200, `{"status":"notSet","targetRefName":"refs/heads/main"}`},
		"missing target":   {200, `{"status":"active"}`},
		"non-JSON body":    {200, `<html>not json</html>`},
		"404":              {404, `{"message":"TF401180: The requested pull request was not found."}`},
		"203 sign-in page": {http.StatusNonAuthoritativeInfo, `<html>Sign In</html>`},
	} {
		rec := newADORecorder(t, tc.status, tc.body)
		got, err := azureDevOpsPRStatus(adoStatusOpts(rec.URL, 5))
		if err == nil {
			t.Errorf("%s: got %+v with a nil error", name, got)
		}
	}

	rec := newADORecorder(t, 200, `{"status":"completed","targetRefName":"refs/heads/main"}`)
	if _, err := azureDevOpsPRStatus(adoStatusOpts(rec.URL, 0)); err == nil {
		t.Error("PR number 0 was accepted")
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("%d request(s) went out without a PR number", n)
	}
}

// TestAzureDevOpsPRStatusBaseRef: refs/heads/ comes off exactly once, so a
// nested branch survives and a retargeted PR reports its current target.
func TestAzureDevOpsPRStatusBaseRef(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	for target, want := range map[string]string{
		"refs/heads/release/x":      "release/x",
		"refs/heads/v1.8":           "v1.8",
		"refs/heads/refs/heads/odd": "refs/heads/odd",
	} {
		rec := newADORecorder(t, 200, `{"status":"active","targetRefName":"`+target+`"}`)
		got, err := azureDevOpsPRStatus(adoStatusOpts(rec.URL, 5))
		if err != nil || got.BaseRef != want {
			t.Errorf("%s: got %+v, %v; want BaseRef %q", target, got, err, want)
		}
	}
}

// TestAzureDevOpsPRStatusServerAddressing: a /tfs collection is the base the
// status read hangs off, and it carries the api-version.
func TestAzureDevOpsPRStatusServerAddressing(t *testing.T) {
	t.Setenv(adoTestEnv, adoTestTok)
	rec := newADORecorder(t, 200, `{"status":"completed","targetRefName":"refs/heads/main"}`)
	opts := adoStatusOpts(rec.URL+"/tfs/DefaultCollection", 9)
	opts.URL = "https://tfs.corp/tfs/DefaultCollection/P/_git/R"
	opts.APIVersion = "6.0"
	if _, err := azureDevOpsPRStatus(opts); err != nil {
		t.Fatal(err)
	}
	if want := "/tfs/DefaultCollection/P/_apis/git/repositories/R/pullRequests/9"; rec.path != want {
		t.Errorf("path = %q, want %q", rec.path, want)
	}
	if got := rec.query["api-version"]; len(got) != 1 || got[0] != "6.0" {
		t.Errorf("api-version = %v, want [6.0]", got)
	}
	if !strings.HasPrefix(rec.header.Get("Authorization"), "Basic ") {
		t.Errorf("Authorization = %q, want Basic", rec.header.Get("Authorization"))
	}
}
