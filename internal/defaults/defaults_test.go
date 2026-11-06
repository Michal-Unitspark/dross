package defaults

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Rivil/dross/internal/project"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	d, err := LoadFile(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("missing file should be ok: %v", err)
	}
	if !reflect.DeepEqual(d.Remote, RemoteDefaults{}) {
		t.Errorf("expected zero RemoteDefaults, got %+v", d.Remote)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	original := &Defaults{
		Remote: RemoteDefaults{
			Provider:  "forgejo",
			APIBase:   "https://forge.example/api/v1",
			LogAPI:    true,
			AuthEnv:   "FORGEJO_TOKEN",
			Reviewers: []string{"alice"},
		},
	}
	if err := original.SaveFile(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(original, loaded) {
		t.Fatalf("round-trip mismatch:\norig:   %+v\nloaded: %+v", original, loaded)
	}
}

func TestSaveFileCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c", File)
	d := &Defaults{Remote: RemoteDefaults{Provider: "github"}}
	if err := d.SaveFile(deep); err != nil {
		t.Fatalf("save deep: %v", err)
	}
}

func TestApplyOnlyFillsZeroFields(t *testing.T) {
	d := Defaults{Remote: RemoteDefaults{
		Provider:  "forgejo",
		APIBase:   "https://forge/api/v1",
		LogAPI:    true,
		AuthEnv:   "FORGEJO_TOKEN",
		Reviewers: []string{"alice", "bob"},
	}}

	t.Run("seeds zero remote", func(t *testing.T) {
		got := d.Apply(project.Remote{URL: "https://forge/me/p"})
		if got.Provider != "forgejo" {
			t.Errorf("Provider should be filled: %q", got.Provider)
		}
		if got.APIBase != "https://forge/api/v1" {
			t.Errorf("APIBase: %q", got.APIBase)
		}
		if !got.LogAPI {
			t.Error("LogAPI should be filled true")
		}
		if got.AuthEnv != "FORGEJO_TOKEN" {
			t.Errorf("AuthEnv: %q", got.AuthEnv)
		}
		if !reflect.DeepEqual(got.Reviewers, []string{"alice", "bob"}) {
			t.Errorf("Reviewers: %+v", got.Reviewers)
		}
		// URL must be preserved.
		if got.URL != "https://forge/me/p" {
			t.Errorf("URL clobbered: %q", got.URL)
		}
	})

	t.Run("does not overwrite already-set fields", func(t *testing.T) {
		got := d.Apply(project.Remote{
			URL:       "https://gh/me/p",
			Provider:  "github",
			APIBase:   "https://api.github.com",
			LogAPI:    false,
			AuthEnv:   "GITHUB_TOKEN",
			Reviewers: []string{"carol"},
		})
		if got.Provider != "github" {
			t.Errorf("Provider was overwritten: %q", got.Provider)
		}
		if got.APIBase != "https://api.github.com" {
			t.Errorf("APIBase was overwritten: %q", got.APIBase)
		}
		// LogAPI default=true should NOT promote a false-by-design field —
		// but we can't distinguish unset from explicit-false on bool, so
		// the rule is "if remote already false and default true, take true".
		// Document by way of test: the override happens.
		if !got.LogAPI {
			t.Error("LogAPI: when remote=false and default=true, default should fill (bool zero is indistinguishable from unset)")
		}
		if got.AuthEnv != "GITHUB_TOKEN" {
			t.Errorf("AuthEnv was overwritten: %q", got.AuthEnv)
		}
		if !reflect.DeepEqual(got.Reviewers, []string{"carol"}) {
			t.Errorf("Reviewers: %+v", got.Reviewers)
		}
	})
}

func TestFromRemote(t *testing.T) {
	r := project.Remote{
		URL:       "https://forge/me/p",
		Provider:  "forgejo",
		Public:    false,
		APIBase:   "https://forge/api/v1",
		LogAPI:    true,
		AuthEnv:   "FORGEJO_TOKEN",
		Reviewers: []string{"alice"},
	}
	got := FromRemote(r)
	want := RemoteDefaults{
		Provider:  "forgejo",
		APIBase:   "https://forge/api/v1",
		LogAPI:    true,
		AuthEnv:   "FORGEJO_TOKEN",
		Reviewers: []string{"alice"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FromRemote:\ngot:  %+v\nwant: %+v", got, want)
	}
}

// TestTelemetryEnabledUnsetIsOn pins the "unset = on" default and both explicit
// settings. The nil check is the whole opt-out contract: inverted, a user who
// never answered the prompt would be treated as having opted OUT, and dross
// would silently stop recording — indistinguishable, from the outside, from
// telemetry working.
func TestTelemetryEnabledUnsetIsOn(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{"unset means on", nil, true},
		{"explicitly on", &on, true},
		{"explicitly off", &off, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TelemetryDefaults{Enabled: tc.in}.TelemetryEnabled()
			if got != tc.want {
				t.Errorf("TelemetryEnabled() = %v, want %v", got, tc.want)
			}
		})
	}

	// The explicit-off case is what makes the nil check load-bearing: if the
	// guard were dropped entirely, unset would dereference nil; if it were
	// inverted, unset and explicit-off would agree. Neither can pass the table.
	if (TelemetryDefaults{Enabled: &off}).TelemetryEnabled() == (TelemetryDefaults{}).TelemetryEnabled() {
		t.Error("unset and explicitly-off must not agree — unset is on")
	}
}

// TestApplySkipsForeignProviderCredentials: a credential and reviewers saved
// for one forge never seed onto a remote detected as another — that token
// would be sent to the wrong host. Same-provider defaults, and an unknown host
// that takes the defaults' provider, still seed in full.
func TestApplySkipsForeignProviderCredentials(t *testing.T) {
	github := Defaults{Remote: RemoteDefaults{Provider: "github", AuthEnv: "GITHUB_TOKEN", Reviewers: []string{"alice"}}}
	ado := project.Remote{
		URL: "https://dev.azure.com/o/p/_git/r", Provider: "azure-devops", APIBase: "https://dev.azure.com/o",
	}

	got := github.Apply(ado)
	if got.AuthEnv != "" || got.Reviewers != nil {
		t.Errorf("github defaults seeded an azure-devops remote: auth_env %q, reviewers %v", got.AuthEnv, got.Reviewers)
	}
	if got.Provider != "azure-devops" || got.APIBase != "https://dev.azure.com/o" {
		t.Errorf("the detected remote was clobbered: %+v", got)
	}

	for _, saved := range []string{"azure-devops", " Azure-DevOps "} {
		same := Defaults{Remote: RemoteDefaults{Provider: saved, AuthEnv: "AZURE_DEVOPS_EXT_PAT", Reviewers: []string{"bob"}}}
		got := same.Apply(ado)
		if got.AuthEnv != "AZURE_DEVOPS_EXT_PAT" || !reflect.DeepEqual(got.Reviewers, []string{"bob"}) {
			t.Errorf("defaults saved for %q did not seed their own provider: %+v", saved, got)
		}
	}

	unknown := github.Apply(project.Remote{URL: "https://forge.corp/me/p"})
	if unknown.Provider != "github" || unknown.AuthEnv != "GITHUB_TOKEN" || !reflect.DeepEqual(unknown.Reviewers, []string{"alice"}) {
		t.Errorf("an unknown host should take the defaults whole: %+v", unknown)
	}
}
