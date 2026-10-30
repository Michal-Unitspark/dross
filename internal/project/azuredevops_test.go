package project

import (
	"strings"
	"testing"
)

// adoWant is the identity one remote form must parse to.
type adoWant struct {
	url, apiBase, base, project, repo string
	services                          bool
}

func checkADO(t *testing.T, raw string, want adoWant) {
	t.Helper()
	got, ok := ParseAzureDevOpsRemote(raw)
	if !ok {
		t.Fatalf("%s: ok = false, want a parse", raw)
	}
	if got.URL != want.url {
		t.Errorf("%s: URL = %q, want %q", raw, got.URL, want.url)
	}
	if got.APIBase != want.apiBase {
		t.Errorf("%s: APIBase = %q, want %q", raw, got.APIBase, want.apiBase)
	}
	if got.Base != want.base {
		t.Errorf("%s: Base = %q, want %q", raw, got.Base, want.base)
	}
	if got.Project != want.project || got.Repo != want.repo {
		t.Errorf("%s: project/repo = %q/%q, want %q/%q", raw, got.Project, got.Repo, want.project, want.repo)
	}
	if got.Services != want.services {
		t.Errorf("%s: Services = %v, want %v", raw, got.Services, want.services)
	}
}

// TestParseAzureDevOpsRemote: every Services remote form for one repo lands on
// the same canonical url and the org api_base, and no userinfo survives.
func TestParseAzureDevOpsRemote(t *testing.T) {
	want := adoWant{
		url:      "https://dev.azure.com/o/p/_git/r",
		apiBase:  "https://dev.azure.com/o",
		base:     "o",
		project:  "p",
		repo:     "r",
		services: true,
	}
	for _, raw := range []string{
		"https://dev.azure.com/o/p/_git/r",
		"https://dev.azure.com/o/p/_git/r.git",
		"https://o@dev.azure.com/o/p/_git/r",
		"https://o:PAT@dev.azure.com/o/p/_git/r",
		"git@ssh.dev.azure.com:v3/o/p/r",
		"ssh://git@ssh.dev.azure.com/v3/o/p/r",
		"ssh://git@ssh.dev.azure.com:22/v3/o/p/r",
		"  https://DEV.AZURE.COM/o/p/_git/r  ",
	} {
		t.Run(raw, func(t *testing.T) {
			checkADO(t, raw, want)
			got, _ := ParseAzureDevOpsRemote(raw)
			if strings.Contains(got.URL, "PAT") || strings.Contains(got.APIBase, "PAT") {
				t.Errorf("userinfo leaked: URL %q, APIBase %q", got.URL, got.APIBase)
			}
		})
	}
}

// TestParseAzureLegacyHostCarriesOrg: on <org>.visualstudio.com the org is the
// host's label, so the base is counted after it, and the canonical url stays on
// visualstudio.com — never rewritten to dev.azure.com.
func TestParseAzureLegacyHostCarriesOrg(t *testing.T) {
	legacy := adoWant{
		url:      "https://o.visualstudio.com/p/_git/r",
		apiBase:  "https://o.visualstudio.com",
		project:  "p",
		repo:     "r",
		services: true,
	}
	checkADO(t, "https://o.visualstudio.com/p/_git/r", legacy)
	checkADO(t, "https://o@o.visualstudio.com/p/_git/r.git", legacy)
	checkADO(t, "o@vs-ssh.visualstudio.com:v3/o/p/r", legacy)
	checkADO(t, "ssh://o@vs-ssh.visualstudio.com:22/v3/O/p/r", legacy)
	checkADO(t, "https://o.visualstudio.com/DefaultCollection/p/_git/r", adoWant{
		url:      "https://o.visualstudio.com/DefaultCollection/p/_git/r",
		apiBase:  "https://o.visualstudio.com/DefaultCollection",
		base:     "DefaultCollection",
		project:  "p",
		repo:     "r",
		services: true,
	})
	// A lone DefaultCollection is the base, never a project: repo == project.
	checkADO(t, "https://o.visualstudio.com/DefaultCollection/_git/r", adoWant{
		url:      "https://o.visualstudio.com/DefaultCollection/r/_git/r",
		apiBase:  "https://o.visualstudio.com/DefaultCollection",
		base:     "DefaultCollection",
		project:  "r",
		repo:     "r",
		services: true,
	})
	// Zero segments before _git: repo == project, base empty.
	checkADO(t, "https://o.visualstudio.com/_git/r", adoWant{
		url:      "https://o.visualstudio.com/r/_git/r",
		apiBase:  "https://o.visualstudio.com",
		project:  "r",
		repo:     "r",
		services: true,
	})
	if got, _ := ParseAzureDevOpsRemote("o@vs-ssh.visualstudio.com:v3/o/p/r"); strings.Contains(got.URL, "dev.azure.com") {
		t.Errorf("vs-ssh rewritten to dev.azure.com: %q", got.URL)
	}
}

// TestParseAzureDevOpsPercentEncoding: an encoded name is decoded exactly once
// into Project/Repo and escaped exactly once into the url — no raw space, no
// %2520 — and the ssh v3 twin lands on the same url.
func TestParseAzureDevOpsPercentEncoding(t *testing.T) {
	const canonical = "https://dev.azure.com/o/My%20Project/_git/My%20Repo"
	want := adoWant{
		url:      canonical,
		apiBase:  "https://dev.azure.com/o",
		base:     "o",
		project:  "My Project",
		repo:     "My Repo",
		services: true,
	}
	for _, raw := range []string{
		canonical,
		canonical + ".git",
		"git@ssh.dev.azure.com:v3/o/My%20Project/My%20Repo",
	} {
		checkADO(t, raw, want)
		got, _ := ParseAzureDevOpsRemote(raw)
		if strings.Contains(got.URL, " ") || strings.Contains(got.URL, "%25") {
			t.Errorf("%s: URL %q is not single-encoded", raw, got.URL)
		}
	}
	// The canonical url parses back to itself.
	got, _ := ParseAzureDevOpsRemote(canonical)
	if again, _ := ParseAzureDevOpsRemote(got.URL); again != got {
		t.Errorf("round-trip drifted: %+v -> %+v", got, again)
	}
}

// TestParseAzureDevOpsServer: a Server collection at any depth — under /tfs or
// at the host root — is the base; an ssh port is dropped, an https port kept.
func TestParseAzureDevOpsServer(t *testing.T) {
	tfs := adoWant{
		url:     "https://tfs.corp/tfs/DefaultCollection/P/_git/R",
		apiBase: "https://tfs.corp/tfs/DefaultCollection",
		base:    "tfs/DefaultCollection",
		project: "P",
		repo:    "R",
	}
	checkADO(t, "https://tfs.corp/tfs/DefaultCollection/P/_git/R", tfs)
	checkADO(t, "ssh://tfs.corp:22/tfs/DefaultCollection/P/_git/R", tfs)
	checkADO(t, "https://tfs.corp/Coll/P/_git/R", adoWant{
		url:     "https://tfs.corp/Coll/P/_git/R",
		apiBase: "https://tfs.corp/Coll",
		base:    "Coll",
		project: "P",
		repo:    "R",
	})
	checkADO(t, "https://tfs.corp:8443/tfs/Coll/P/_git/R", adoWant{
		url:     "https://tfs.corp:8443/tfs/Coll/P/_git/R",
		apiBase: "https://tfs.corp:8443/tfs/Coll",
		base:    "tfs/Coll",
		project: "P",
		repo:    "R",
	})
	// One segment before _git is the collection alone: repo == project.
	checkADO(t, "https://tfs.corp/Coll/_git/R", adoWant{
		url:     "https://tfs.corp/Coll/R/_git/R",
		apiBase: "https://tfs.corp/Coll",
		base:    "Coll",
		project: "R",
		repo:    "R",
	})
	// Under /tfs the collection is the segment after it, never a project, so
	// the short form is repo == project too — and its canonical url parses back
	// to itself.
	short := adoWant{
		url:     "https://tfs.corp/tfs/Coll/R/_git/R",
		apiBase: "https://tfs.corp/tfs/Coll",
		base:    "tfs/Coll",
		project: "R",
		repo:    "R",
	}
	checkADO(t, "https://tfs.corp/tfs/Coll/_git/R", short)
	checkADO(t, "ssh://tfs.corp:22/tfs/Coll/_git/R", short)
	checkADO(t, short.url, short)
	checkADO(t, "https://tfs.corp/TFS/Coll/_git/R", adoWant{
		url:     "https://tfs.corp/TFS/Coll/R/_git/R",
		apiBase: "https://tfs.corp/TFS/Coll",
		base:    "TFS/Coll",
		project: "R",
		repo:    "R",
	})
	// The /tfs marker is a Server's alone: a dev.azure.com org named tfs keeps
	// its project.
	checkADO(t, "https://dev.azure.com/tfs/p/_git/r", adoWant{
		url:      "https://dev.azure.com/tfs/p/_git/r",
		apiBase:  "https://dev.azure.com/tfs",
		base:     "tfs",
		project:  "p",
		repo:     "r",
		services: true,
	})
}

// TestParseAzureDevOpsRepoIsProject: dev.azure.com/<org>/_git/<repo> names a
// repo whose project shares its name.
func TestParseAzureDevOpsRepoIsProject(t *testing.T) {
	checkADO(t, "https://dev.azure.com/o/_git/r", adoWant{
		url:      "https://dev.azure.com/o/r/_git/r",
		apiBase:  "https://dev.azure.com/o",
		base:     "o",
		project:  "r",
		repo:     "r",
		services: true,
	})
}

// TestParseAzureRejects: anything short of the full shape refuses whole, so no
// caller ever addresses a half-parsed repo.
func TestParseAzureRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"not-a-url",
		"https://dev.azure.com/o/p/r",           // no _git
		"https://dev.azure.com/o/p/_git",        // _git last
		"https://dev.azure.com/o/p/_git/r/x",    // _git not second-to-last
		"https://dev.azure.com/o//p/_git/r",     // empty segment
		"https://dev.azure.com/o/p/_git/r/",     // trailing empty segment
		"https://dev.azure.com/o/./_git/r",      // dot segment
		"https://dev.azure.com/o/../_git/r",     // dot-dot segment
		"https://dev.azure.com/o/%2E%2E/_git/r", // escaped dot-dot
		"https://dev.azure.com/_git/r",          // no org
		"https://dev.azure.com/a/b/p/_git/r",    // two-segment org
		"https://dev.azure.com/o/_git/p/_git/r", // a second _git
		"https://o.visualstudio.com/a/b/p/_git/r",
		"https://tfs.corp/_git/R",              // Server without a collection
		"https://tfs.corp/tfs/_git/R",          // /tfs without its collection
		"git@ssh.dev.azure.com:v3/o/p",         // v3 missing the repo
		"git@ssh.dev.azure.com:v2/o/p/r",       // not v3
		"git@ssh.dev.azure.com:o/p/_git/r",     // ssh host, not v3
		"o@vs-ssh.visualstudio.com:v3/o_x/p/r", // org not a DNS label
		"https://dev.azure.com/o/p%zz/_git/r",  // bad escape
		"github.com/x/y",
		"https://github.com/x/y",
	} {
		if got, ok := ParseAzureDevOpsRemote(raw); ok {
			t.Errorf("%q: parsed to %+v, want ok=false", raw, got)
		}
	}
}

// TestAzureServicesHost: the Services predicate matches whole hosts, never a
// substring or suffix that an attacker's domain could carry.
func TestAzureServicesHost(t *testing.T) {
	for host, want := range map[string]bool{
		"dev.azure.com":                 true,
		"o.visualstudio.com":            true,
		"my-org.visualstudio.com":       true,
		"evil-visualstudio.com":         false,
		"visualstudio.com":              false,
		"visualstudio.com.evil.example": false,
		"dev.azure.com.evil.example":    false,
		"a.b.visualstudio.com":          false,
		"-o.visualstudio.com":           false,
		"tfs.corp":                      false,
	} {
		if got := azureServicesHost(host); got != want {
			t.Errorf("azureServicesHost(%q) = %v, want %v", host, got, want)
		}
	}
}
