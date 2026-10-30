package diag

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rivil/dross/internal/project"
)

// silent runs fn with os.Stdout swapped for a pipe and fails the test if a
// single byte reaches it. Every check in this package RETURNS lines; one that
// printed would bypass the caller's decision about what the line costs.
func silent(t *testing.T, fn func()) {
	t.Helper()
	prev := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	os.Stdout = prev
	w.Close()
	if b := <-done; len(b) != 0 {
		t.Errorf("a diag check wrote to stdout: %q", b)
	}
}

// TestLevelsAndIssues: the level vocabulary renders, and only Issue counts.
func TestLevelsAndIssues(t *testing.T) {
	for l, want := range map[Level]string{OK: "ok", Warn: "warn", Issue: "issue", Note: "note", Level(9): "unknown"} {
		if l.String() != want {
			t.Errorf("%d.String() = %q, want %q", int(l), l.String(), want)
		}
	}
	sections := []Section{
		{Heading: "a", Lines: []Line{ok("x"), warn("y"), issue("z"), note("n")}},
		{Heading: "b", Lines: []Line{issue("z"), issue("w")}},
	}
	if got := Issues(sections); got != 3 {
		t.Errorf("Issues = %d, want 3", got)
	}
	if got := CountIssues(sections[0].Lines); got != 1 {
		t.Errorf("CountIssues = %d, want 1", got)
	}
}

// TestDuplicateRoadmapSlugsIgnoresRepeatWithinOneArray: what the check reports
// is two milestones claiming one phase. A slug listed twice inside a single
// array is a malformed array, not a re-scope, and naming it here would report a
// second milestone that does not exist.
func TestDuplicateRoadmapSlugsIgnoresRepeatWithinOneArray(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".dross")
	writeMilestone(t, root, "v1.4", `phases = ["twice", "twice"]

[milestone]
  version = "v1.4"
  status = "active"
`)
	var got []Duplicate
	silent(t, func() { got = RoadmapDuplicates(root) })
	if len(got) != 0 {
		t.Errorf("RoadmapDuplicates = %v, want none — one array is one roadmap", got)
	}
}

// TestRoadmapDuplicatesNamesEveryClaimant: a slug on two roadmaps is reported
// once, with both versions in listing order, first-listed first.
func TestRoadmapDuplicatesNamesEveryClaimant(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".dross")
	writeMilestone(t, root, "v1.0", "phases = [\"alpha\", \"shared\"]\n\n[milestone]\n  version = \"v1.0\"\n")
	writeMilestone(t, root, "v1.1", "phases = [\"shared\", \"beta\"]\n\n[milestone]\n  version = \"v1.1\"\n")
	got := RoadmapDuplicates(root)
	if len(got) != 1 || got[0].Slug != "shared" || len(got[0].Versions) != 2 || got[0].Versions[0] != "v1.0" {
		t.Errorf("RoadmapDuplicates = %+v, want shared on [v1.0 v1.1]", got)
	}
	if RoadmapDuplicates(filepath.Join(t.TempDir(), "nope")) != nil {
		t.Error("a root with no milestones reported duplicates")
	}
}

func writeMilestone(t *testing.T, root, version, body string) {
	t.Helper()
	dir := filepath.Join(root, "milestones")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, version+".toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCombinationWarnings is the moved remote/board pairing table.
func TestCombinationWarnings(t *testing.T) {
	cases := []struct {
		provider, scheme, user string
		want                   string // substring of the one expected warning, "" for none
	}{
		{"", "", "", ""},
		{"none", "", "", ""},
		{"github", "", "", ""},
		{"bitbucket", "", "", "auth_user is not set"},
		{"bitbucket", "basic", "me", ""},
		{"github", "basic", "u", "no effect"},
		{"sourcehut", "", "", "cannot open a PR"},
		// azure-devops: Basic :PAT by default, Bearer on bearer; nothing else
		// changes what is sent, and the bitbucket Basic warnings never apply.
		{"azure-devops", "", "", ""},
		{"azure-devops", "bearer", "", ""},
		{"azure-devops", "private-token", "", "no effect"},
		{"azure-devops", "basic", "", "no effect"},
		{" Azure-DevOps ", "basic", "", "no effect"},
		{"azure-devops", "", "me", "auth_user"},
	}
	for _, c := range cases {
		var got []string
		silent(t, func() {
			got = RemoteCombination(project.Remote{Provider: c.provider, AuthScheme: c.scheme, AuthUser: c.user})
		})
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("RemoteCombination(%q,%q,%q) = %v, want none", c.provider, c.scheme, c.user, got)
			}
			continue
		}
		if len(got) != 1 || !contains(got[0], c.want) {
			t.Errorf("RemoteCombination(%q,%q,%q) = %v, want one warning mentioning %q", c.provider, c.scheme, c.user, got, c.want)
		}
	}
	// basic on azure-devops is a no-op, not a missing user: neither bitbucket
	// warning may ride along with the no-effect one.
	for _, w := range RemoteCombination(project.Remote{Provider: "azure-devops", AuthScheme: "basic"}) {
		if contains(w, "auth_user is not set") || contains(w, "sends no Basic credential") {
			t.Errorf("azure-devops basic carries a Basic user:token warning: %q", w)
		}
	}
	if w := BoardCombination("jira", "", ""); len(w) != 1 || !contains(w[0], "auth_user") {
		t.Errorf("jira without auth_user: %v", w)
	}
	if w := BoardCombination("youtrack", "epic", "u"); len(w) != 0 {
		t.Errorf("youtrack epic mode warned: %v", w)
	}
	if got := SortedStateMapKeys(map[string]string{"b": "2", "a": "1", "c": "3"}); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("SortedStateMapKeys = %v", got)
	}
}

// TestGitVersionAtLeast pins the two-component parse and its lenient arms.
func TestGitVersionAtLeast(t *testing.T) {
	cases := []struct {
		raw, floor string
		want       bool
	}{
		{"git version 2.23.0", "2.24", false},
		{"git version 2.24.0", "2.24", true},
		{"git version 2.39.5 (Apple Git-154)", "2.24", true},
		{"git version 3.0.0", "2.24", true},
		{"git version 1.9.1", "2.24", false},
		{"garbage", "2.24", true},           // unreadable version is a warning elsewhere, not a finding
		{"git version 2.0.0", "nope", true}, // an unparseable floor must not fail every repo
	}
	for _, c := range cases {
		if got := GitVersionAtLeast(c.raw, c.floor); got != c.want {
			t.Errorf("GitVersionAtLeast(%q, %q) = %v, want %v", c.raw, c.floor, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestRemoteCombinationAzureBaseMismatch: a Server's api_base must be the
// collection its url names. One level short is flagged, naming the right
// base; spelling differences the server ignores are not.
func TestRemoteCombinationAzureBaseMismatch(t *testing.T) {
	ado := func(apiBase string) []string {
		var got []string
		silent(t, func() {
			got = RemoteCombination(project.Remote{
				Provider: "azure-devops", URL: "https://tfs/tfs/C/P/_git/R", APIBase: apiBase,
			})
		})
		return got
	}
	if got := ado("https://tfs/tfs"); len(got) != 1 || !contains(got[0], "https://tfs/tfs/C") {
		t.Errorf("api_base one level short of the collection: %v, want one warning naming https://tfs/tfs/C", got)
	}
	// http is the host allowlist's refusal to make, not a collection mismatch.
	for _, same := range []string{"https://tfs/tfs/C", "https://tfs/tfs/C/", "https://TFS/tfs/c", "http://tfs/tfs/C", ""} {
		if got := ado(same); len(got) != 0 {
			t.Errorf("api_base %q: %v, want none", same, got)
		}
	}
	// Services derives its own org base; only a Server's is checked here.
	var got []string
	silent(t, func() {
		got = RemoteCombination(project.Remote{
			Provider: "azure-devops", URL: "https://o.visualstudio.com/p/_git/r", APIBase: "https://dev.azure.com/o",
		})
	})
	if len(got) != 0 {
		t.Errorf("a Services url with the dev.azure.com org base: %v, want none", got)
	}
}
