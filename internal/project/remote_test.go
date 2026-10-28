package project

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/hostallow"
)

func TestRemoteRoundTrip(t *testing.T) {
	original := &Project{
		Remote: Remote{
			URL:       "https://forge.example/me/proj",
			Provider:  "forgejo",
			Public:    false,
			APIBase:   "https://forge.example/api/v1",
			LogAPI:    true,
			AuthEnv:   "FORGEJO_TOKEN",
			Reviewers: []string{"alice", "bob"},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "project.toml")
	if err := original.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(original.Remote, loaded.Remote) {
		t.Fatalf("Remote round-trip mismatch:\norig:   %+v\nloaded: %+v", original.Remote, loaded.Remote)
	}
}

func TestDetectRemote(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantURL      string
		wantProvider string
		wantPublic   bool
		wantAPIBase  string
	}{
		{
			name:         "github https",
			input:        "https://github.com/Rivil/dross",
			wantURL:      "https://github.com/Rivil/dross",
			wantProvider: "github",
			wantPublic:   true,
			wantAPIBase:  "https://api.github.com",
		},
		{
			name:         "github https with .git suffix",
			input:        "https://github.com/Rivil/dross.git",
			wantURL:      "https://github.com/Rivil/dross",
			wantProvider: "github",
			wantPublic:   true,
			wantAPIBase:  "https://api.github.com",
		},
		{
			name:         "github ssh scp form",
			input:        "git@github.com:Rivil/dross.git",
			wantURL:      "https://github.com/Rivil/dross",
			wantProvider: "github",
			wantPublic:   true,
			wantAPIBase:  "https://api.github.com",
		},
		{
			name:         "github ssh url form",
			input:        "ssh://git@github.com/Rivil/dross.git",
			wantURL:      "https://github.com/Rivil/dross",
			wantProvider: "github",
			wantPublic:   true,
			wantAPIBase:  "https://api.github.com",
		},
		{
			name:         "codeberg → forgejo",
			input:        "https://codeberg.org/me/proj",
			wantURL:      "https://codeberg.org/me/proj",
			wantProvider: "forgejo",
			wantPublic:   true,
			wantAPIBase:  "https://codeberg.org/api/v1",
		},
		{
			name:         "gitlab group named _git keeps its own url",
			input:        "https://gitlab.com/g/_git/r.git",
			wantURL:      "https://gitlab.com/g/_git/r",
			wantProvider: "gitlab",
			wantPublic:   true,
			wantAPIBase:  "https://gitlab.com/api/v4",
		},
		{
			name:         "gitlab.com → gitlab, derives /api/v4",
			input:        "https://gitlab.com/o/r",
			wantURL:      "https://gitlab.com/o/r",
			wantProvider: "gitlab",
			wantPublic:   true,
			wantAPIBase:  "https://gitlab.com/api/v4",
		},
		{
			name:         "encoded segment stays single-encoded",
			input:        "https://gitlab.com/o/My%20Repo",
			wantURL:      "https://gitlab.com/o/My%20Repo",
			wantProvider: "gitlab",
			wantPublic:   true,
			wantAPIBase:  "https://gitlab.com/api/v4",
		},
		{
			name:         "bitbucket",
			input:        "git@bitbucket.org:me/proj.git",
			wantURL:      "https://bitbucket.org/me/proj",
			wantProvider: "bitbucket",
			wantPublic:   true,
			wantAPIBase:  "https://api.bitbucket.org/2.0",
		},
		{
			name:         "azure devops https",
			input:        "https://dev.azure.com/o/p/_git/r",
			wantURL:      "https://dev.azure.com/o/p/_git/r",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://dev.azure.com/o",
		},
		{
			name:         "azure devops https with user@",
			input:        "https://o@dev.azure.com/o/p/_git/r",
			wantURL:      "https://dev.azure.com/o/p/_git/r",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://dev.azure.com/o",
		},
		{
			name:         "azure devops ssh v3",
			input:        "git@ssh.dev.azure.com:v3/o/p/r",
			wantURL:      "https://dev.azure.com/o/p/_git/r",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://dev.azure.com/o",
		},
		{
			name:         "azure devops legacy https",
			input:        "https://o.visualstudio.com/p/_git/r",
			wantURL:      "https://o.visualstudio.com/p/_git/r",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://o.visualstudio.com",
		},
		{
			name:         "azure devops legacy vs-ssh",
			input:        "o@vs-ssh.visualstudio.com:v3/o/p/r",
			wantURL:      "https://o.visualstudio.com/p/_git/r",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://o.visualstudio.com",
		},
		{
			name:         "azure devops legacy DefaultCollection keeps its base",
			input:        "https://o.visualstudio.com/DefaultCollection/p/_git/r",
			wantURL:      "https://o.visualstudio.com/DefaultCollection/p/_git/r",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://o.visualstudio.com/DefaultCollection",
		},
		{
			name:         "azure devops percent-encoded names stay single-encoded",
			input:        "https://dev.azure.com/o/My%20Project/_git/My%20Repo.git",
			wantURL:      "https://dev.azure.com/o/My%20Project/_git/My%20Repo",
			wantProvider: "azure-devops",
			wantPublic:   false,
			wantAPIBase:  "https://dev.azure.com/o",
		},
		{
			name:         "azure devops host but not a repo — not claimed",
			input:        "https://dev.azure.com/o",
			wantURL:      "https://dev.azure.com/o",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "azure devops server https — url only, provider empty",
			input:        "https://tfs.corp/tfs/Coll/P/_git/R",
			wantURL:      "https://tfs.corp/tfs/Coll/P/_git/R",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "azure devops server ssh drops the ssh port",
			input:        "ssh://tfs.corp:22/tfs/Coll/P/_git/R",
			wantURL:      "https://tfs.corp/tfs/Coll/P/_git/R",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "azure devops server https keeps its web port",
			input:        "https://tfs.corp:8443/tfs/Coll/P/_git/R",
			wantURL:      "https://tfs.corp:8443/tfs/Coll/P/_git/R",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "self-hosted unknown host — URL filled, provider empty",
			input:        "https://forge.example.internal/me/proj.git",
			wantURL:      "https://forge.example.internal/me/proj",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "self-hosted ssh — URL filled, provider empty",
			input:        "git@forge.example.internal:me/proj.git",
			wantURL:      "https://forge.example.internal/me/proj",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "empty string returns zero value",
			input:        "",
			wantURL:      "",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
		{
			name:         "garbage returns zero value",
			input:        "not-a-url",
			wantURL:      "",
			wantProvider: "",
			wantPublic:   false,
			wantAPIBase:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectRemote(tc.input)
			if got.URL != tc.wantURL {
				t.Errorf("URL: got %q, want %q", got.URL, tc.wantURL)
			}
			if got.Provider != tc.wantProvider {
				t.Errorf("Provider: got %q, want %q", got.Provider, tc.wantProvider)
			}
			if got.Public != tc.wantPublic {
				t.Errorf("Public: got %v, want %v", got.Public, tc.wantPublic)
			}
			if got.APIBase != tc.wantAPIBase {
				t.Errorf("APIBase: got %q, want %q", got.APIBase, tc.wantAPIBase)
			}
		})
	}
}

// TestDetectRemoteDropsUserinfo: a remote can carry a token in its userinfo,
// and the detected remote is what init writes to project.toml. No form of the
// URL may bring the token along.
func TestDetectRemoteDropsUserinfo(t *testing.T) {
	for _, raw := range []string{
		"https://u:CANARY-TOK@github.com/o/r.git",
		"https://CANARY-TOK@github.com/o/r",
		"ssh://git:CANARY-TOK@github.com/o/r.git",
		"https://u:CANARY@TOK@github.com/o/r",
		"CANARY-TOK@github.com:o/r.git",
	} {
		r := DetectRemote(raw)
		if r.URL != "https://github.com/o/r" {
			t.Errorf("%s: URL = %q, want https://github.com/o/r", raw, r.URL)
		}
		if s := fmt.Sprintf("%+v", r); strings.Contains(s, "CANARY") || strings.Contains(s, "TOK") {
			t.Errorf("%s: the userinfo reached the detected remote: %s", raw, s)
		}
	}

	// An Azure DevOps PAT is the password half of an https remote as often as
	// not; the ADO route re-parses the raw remote and must cut it too.
	for _, raw := range []string{
		"https://CANARY-USER:CANARY-TOK@dev.azure.com/o/p/_git/r",
		"https://CANARY-TOK@dev.azure.com/o/p/_git/r",
		"https://CANARY-USER:CANARY-TOK@o.visualstudio.com/DefaultCollection/p/_git/r",
		"https://CANARY-USER:CANARY-TOK@tfs.corp/tfs/Coll/p/_git/r",
	} {
		r := DetectRemote(raw)
		if !strings.HasSuffix(r.URL, "/p/_git/r") {
			t.Errorf("%s: URL = %q, want the canonical .../p/_git/r", raw, r.URL)
		}
		if s := fmt.Sprintf("%+v", r); strings.Contains(s, "CANARY") || strings.Contains(s, "TOK") {
			t.Errorf("%s: the userinfo reached the detected remote: %s", raw, s)
		}
	}
}

// TestDetectRemoteServerSSHAllowsItsAPIBase: a Server cloned over ssh with an
// explicit port must still write the web host, port-free — the host allowlist
// derives from that url, so tfs.corp:22 would refuse the https collection
// api_base the user sets by hand.
func TestDetectRemoteServerSSHAllowsItsAPIBase(t *testing.T) {
	r := DetectRemote("ssh://tfs.corp:22/tfs/Coll/P/_git/R")
	if r.Provider != "" {
		t.Errorf("Provider = %q; a Server host is never claimed outright", r.Provider)
	}
	if err := hostallow.Derive(r.URL, nil).Check("[remote].api_base", "https://tfs.corp/tfs/Coll"); err != nil {
		t.Errorf("url %q does not allow its own collection api_base: %v", r.URL, err)
	}
	// The discriminator: the url as origin spelled it would refuse that base.
	if err := hostallow.Derive("https://tfs.corp:22/tfs/Coll/P/_git/R", nil).Check("[remote].api_base", "https://tfs.corp/tfs/Coll"); err == nil {
		t.Error("a :22 url allows https://tfs.corp — the check above proves nothing")
	}
}
