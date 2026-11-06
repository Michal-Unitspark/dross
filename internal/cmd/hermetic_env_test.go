package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Rivil/dross/internal/defaults"
	"github.com/Rivil/dross/internal/pincheck"
	"github.com/Rivil/dross/internal/protect"
	"github.com/Rivil/dross/internal/ship"
)

// ambientHome is the HOME this test binary inherited, captured before TestMain
// replaces it. Kept so TestHermeticHome_IsIsolated can assert the replacement
// actually happened rather than trust it.
var ambientHome = os.Getenv("HOME")

// ambientClaudeConfigDir is the CLAUDE_CONFIG_DIR this test binary inherited,
// captured before TestMain clears it.
var ambientClaudeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")

// TestMain pins HOME to an empty throwaway directory for the whole package.
//
// Without it, every test that runs `dross init` in a temp repo silently
// inherits the developer's real ~/.claude/dross/defaults.toml: seedRemote
// overlays those defaults onto the git-detected remote (init.go), so a machine
// whose defaults set remote.auth_env = BITBUCKET_TOKEN produced a scaffolded
// repo that doctor called unhealthy unless that token happened to be exported
// in the shell. Five doctor/validate tests were red on every other host. A test
// suite's verdict must not depend on the developer's home directory.
//
// This is the same isolation chdir already applies to DROSS_NO_TELEMETRY and
// CLAUDE_CONFIG_DIR, hoisted to the process because it has to be in place
// before any test body runs. Tests that need a populated home still override
// with t.Setenv("HOME", ...); that restores to this throwaway dir afterwards,
// never to the ambient one.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "dross-test-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hermetic HOME: mkdtemp: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintf(os.Stderr, "hermetic HOME: setenv: %v\n", err)
		os.Exit(1)
	}
	// CLAUDE_CONFIG_DIR outranks HOME for everything Claude Code reads —
	// settings.json and the agents directory install writes the solo reviewer
	// into. Cleared, it resolves under the throwaway HOME above, so a machine
	// that exports it never gets test agents or hooks written into its real
	// config. chdir() still pins a per-test one.
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		fmt.Fprintf(os.Stderr, "hermetic CLAUDE_CONFIG_DIR: unsetenv: %v\n", err)
		os.Exit(1)
	}
	if err := pinGitConfig(home); err != nil {
		fmt.Fprintf(os.Stderr, "hermetic git config: %v\n", err)
		os.Exit(1)
	}
	pinResolver = countingPinResolver{}
	stubBranchProtection()
	code := m.Run()
	if err := pinDialErr(pinDials.Load()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	// The shared source program is loaded once per binary (srcprog_test.go); a
	// second load is a loader that bypassed the sync.Once, doubling the suite's
	// most expensive setup without any test noticing.
	if err := srcLoadCountErr(srcProgLoads.Load()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// pinGitConfig points GIT_CONFIG_GLOBAL at a throwaway config that switches off
// git's background maintenance for every git process this package spawns.
//
// The race it closes: git forks a detached auto-maintenance process after enough
// loose objects pile up, and that process can still be writing inside .git when
// t.TempDir() cleanup runs — RemoveAll then fails with ENOTEMPTY and testing
// reports the test as failed even though every assertion passed. It reddened
// TestShipAutoRequestsZeroReviewers (run 30791024530) and, after the first fix,
// TestShipAutoJSONComposable (run 31310028319).
//
// That first fix set gc.auto=0 per fixture. It was too narrow twice over: only
// three fixture helpers got it, out of ~75 git-init sites in the suite, and
// gc.auto disables the *gc task* while modern git triggers `git maintenance run
// --auto` where it used to trigger `git gc --auto`. Pinning the config for the
// whole process covers every fixture, including ones not written yet.
//
// GIT_CONFIG_SYSTEM is deliberately left alone: neutralising it would drop the
// runner's ownership settings too, and this is a targeted fix for background
// writers, not a full hermetic-git change. safe.directory is carried for the
// same reason — the tests that shell out to git against the real repo checkout
// must keep working under a replaced global config.
func pinGitConfig(home string) error {
	path := filepath.Join(home, "gitconfig")
	body := "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n[safe]\n\tdirectory = *\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", path); err != nil {
		return fmt.Errorf("setenv GIT_CONFIG_GLOBAL: %w", err)
	}
	return nil
}

// errHermeticGH is what every branch-protection write seam answers under
// TestMain: a test that wants a write to succeed installs its own stub.
var errHermeticGH = errors.New("hermetic test stub: no gh, no network")

// stubBranchProtection swaps every ship seam that would run `gh api` or
// `gh pr merge` for one that never dials. Reads answer "known, no rules" —
// an unprotected base — so every flow written before branch protection
// existed keeps its direct push; writes and auto-merge fail loudly. A test
// that needs a protected base, a successful write or an armed PR installs its
// own stub and restores this one. OpenPRFunc is left on OpenPR: the ship
// tests here already drive it through a stub gh on PATH.
func stubBranchProtection() {
	ship.BranchRulesFunc = func(ship.OpenOpts, string) ship.BranchRulesResult {
		return ship.BranchRulesResult{Known: true}
	}
	ship.RepoMergeSettingsFunc = func(ship.OpenOpts) ship.MergeSettings {
		return ship.MergeSettings{Reason: errHermeticGH.Error()}
	}
	ship.ListRulesetsFunc = func(ship.OpenOpts) ([]ship.RulesetSummary, error) { return nil, errHermeticGH }
	ship.CreateRulesetFunc = func(ship.OpenOpts, protect.Ruleset) (int64, error) { return 0, errHermeticGH }
	ship.UpdateRulesetFunc = func(ship.OpenOpts, int64, protect.Ruleset) error { return errHermeticGH }
	ship.SetAllowAutoMergeFunc = func(ship.OpenOpts, bool) error { return errHermeticGH }
	ship.AutoMergePRFunc = func(ship.OpenOpts, int, string) (ship.AutoMergeResult, error) {
		return ship.AutoMergeResult{}, fmt.Errorf("%w: %v", ship.ErrAutoMergeUnavailable, errHermeticGH)
	}
	// The azure-devops branch-policy read answers like BranchRules: known,
	// unprotected, so an ADO fixture keeps its direct push unless a test
	// installs its own stub.
	ship.ADOBranchProtectionFunc = func(ship.OpenOpts, string) ship.ADOProtection {
		return ship.ADOProtection{Known: true}
	}
}

// TestHermeticBranchProtection_NeverDials pins stubBranchProtection, so
// dropping it from TestMain fails here by name rather than as a test that
// shells out to the real gh and reads the developer's live repo settings.
func TestHermeticBranchProtection_NeverDials(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seam, real any
	}{
		{"BranchRulesFunc", ship.BranchRulesFunc, ship.BranchRules},
		{"RepoMergeSettingsFunc", ship.RepoMergeSettingsFunc, ship.RepoMergeSettings},
		{"ListRulesetsFunc", ship.ListRulesetsFunc, ship.ListRulesets},
		{"CreateRulesetFunc", ship.CreateRulesetFunc, ship.CreateRuleset},
		{"UpdateRulesetFunc", ship.UpdateRulesetFunc, ship.UpdateRuleset},
		{"SetAllowAutoMergeFunc", ship.SetAllowAutoMergeFunc, ship.SetAllowAutoMerge},
		{"AutoMergePRFunc", ship.AutoMergePRFunc, ship.AutoMergePR},
		{"ADOBranchProtectionFunc", ship.ADOBranchProtectionFunc, ship.ADOBranchProtection},
	} {
		if reflect.ValueOf(tc.seam).Pointer() == reflect.ValueOf(tc.real).Pointer() {
			t.Errorf("%s is still its gh implementation — TestMain must stub it", tc.name)
		}
	}
	if reflect.ValueOf(ship.OpenPRFunc).Pointer() != reflect.ValueOf(ship.OpenPR).Pointer() {
		t.Error("OpenPRFunc must keep its OpenPR default under TestMain")
	}

	opts := ship.OpenOpts{Provider: "github", URL: "https://github.com/Rivil/dross"}
	if got := ship.BranchRulesFunc(opts, "main"); !got.Known || len(got.Rules) != 0 {
		t.Errorf("stubbed BranchRules = %+v, want known with no rules (unprotected)", got)
	}
	ado := ship.OpenOpts{Provider: "azure-devops", URL: "https://dev.azure.com/o/p/_git/r"}
	if got := ship.ADOBranchProtectionFunc(ado, "main"); !got.Known || got.Protected {
		t.Errorf("stubbed ADOBranchProtection = %+v, want known and unprotected", got)
	}
	if got := ship.RepoMergeSettingsFunc(opts); got.Known {
		t.Errorf("stubbed RepoMergeSettings = %+v, want unknown", got)
	}
	if _, err := ship.AutoMergePRFunc(opts, 1, "merge"); !errors.Is(err, ship.ErrAutoMergeUnavailable) {
		t.Errorf("stubbed AutoMergePR err = %v, want ErrAutoMergeUnavailable", err)
	}
	if err := ship.SetAllowAutoMergeFunc(opts, true); err == nil {
		t.Error("stubbed SetAllowAutoMerge succeeded")
	}
}

// TestHermeticGitConfig_DisablesBackgroundMaintenance pins the TestMain git
// hardening, so deleting it fails here by name instead of resurfacing months
// later as an intermittent ENOTEMPTY in an unrelated test.
//
// The repo is created with a bare `git init` rather than through gitInit on
// purpose: gitInit writes gc.auto=0 into the repo's own config, which would
// satisfy the gc.auto row no matter what the global config said. Reading both
// keys out of a fixture nobody configured locally is what proves the *global*
// mechanism is the one answering.
func TestHermeticGitConfig_DisablesBackgroundMaintenance(t *testing.T) {
	if os.Getenv("GIT_CONFIG_GLOBAL") == "" {
		t.Fatal("GIT_CONFIG_GLOBAL is unset — TestMain should have pinned it to a throwaway config")
	}

	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")

	// --default keeps an absent key readable: without it a missing setting exits
	// 1 and mustGit turns that into an opaque fatal, which also stops the second
	// row from being checked at all.
	for _, tc := range []struct{ key, want string }{
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
	} {
		if got := mustGit(t, dir, "config", "--default", "<unset>", "--get", tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q — background maintenance can still race TempDir cleanup", tc.key, got, tc.want)
		}
	}
}

// TestHermeticHome_IsIsolated pins the TestMain isolation itself, so removing
// it fails here by name instead of surfacing as an unrelated doctor complaint
// on whichever machine has the unlucky defaults.toml.
func TestHermeticHome_IsIsolated(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME is empty — TestMain should have pinned it to a throwaway dir")
	}
	if ambientHome != "" && home == ambientHome {
		t.Fatalf("HOME is still the ambient %q, so this package reads the developer's real ~/.claude", ambientHome)
	}

	dir, err := GlobalDir()
	if err != nil {
		t.Fatalf("GlobalDir: %v", err)
	}
	path := filepath.Join(dir, defaults.File)
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s must not exist under the test HOME (stat err = %v) — init would overlay it", path, err)
	}
}

// TestHermeticHome_HostileGlobalDefaultsDoNotRedden reproduces the verify
// BLOCKING deterministically: a global defaults.toml naming a token that is not
// exported must not make a scaffolded repo fail its own health check. The
// scaffold configures remote.auth_env itself, so the ambient suggestion is
// never adopted; reverting that leaves init inheriting absentToken and doctor
// reporting it unset.
func TestHermeticHome_HostileGlobalDefaultsDoNotRedden(t *testing.T) {
	const absentToken = "DROSS_TEST_ABSENT_TOKEN" // dross:allow-secret
	t.Setenv(absentToken, "")                     // empty reads as unset to doctor's os.Getenv check

	home := t.TempDir()
	t.Setenv("HOME", home)
	mustWrite(t, filepath.Join(home, ".claude", "dross", defaults.File),
		"[remote_defaults]\nauth_env = \""+absentToken+"\"\n")

	// scaffoldDoctorRepo fatals if the repo it builds is not doctor-clean.
	dir := scaffoldDoctorRepo(t)

	if body := mustRead(t, filepath.Join(dir, ".dross", "project.toml")); strings.Contains(body, absentToken) {
		t.Errorf("the scaffold adopted the ambient defaults' auth_env %q:\n%s", absentToken, body)
	}
}

// TestHermeticHome_DoctorScaffoldOwnsItsToken keeps the scaffold's two halves
// in step: the env var it exports and the one it writes to project.toml must be
// the same name, or doctor reports an unset token for a var nothing set.
func TestHermeticHome_DoctorScaffoldOwnsItsToken(t *testing.T) {
	dir := scaffoldDoctorRepo(t)

	body := mustRead(t, filepath.Join(dir, ".dross", "project.toml"))
	if want := `auth_env = "` + doctorTokenEnv + `"`; !strings.Contains(body, want) {
		t.Errorf("project.toml does not carry %s:\n%s", want, body)
	}
	if os.Getenv(doctorTokenEnv) == "" {
		t.Errorf("the scaffold must export $%s, or doctor flags it as unset", doctorTokenEnv)
	}
}

// pinDials counts upstream lookups that reached doctor's default pin resolver.
var pinDials atomic.Int64

// countingPinResolver is the pin resolver every test in this binary gets
// unless it installs a stub. It never dials: it counts the lookup and answers
// with an error, and TestMain fails the binary if the count is non-zero after
// the run — a doctor test that would have reached proxy.golang.org or
// nodejs.org from a developer's laptop or CI is a test whose verdict depends on
// the network (locked decision doctor_net keeps doctor usable offline; its
// tests must be too).
type countingPinResolver struct{}

func (countingPinResolver) Releases(context.Context, pincheck.Site) ([]pincheck.Release, error) {
	pinDials.Add(1)
	return nil, errors.New("hermetic test binary: doctor's pin resolver would have reached the network — install a stub")
}

// pinDialErr is TestMain's verdict on the dial count.
func pinDialErr(n int64) error {
	if n == 0 {
		return nil
	}
	return fmt.Errorf("hermetic pin resolver: %d upstream lookup(s) reached doctor's default resolver — a test ran the Pin currency section without installing a stub (withPinResolver)", n)
}

// withPinResolver installs r as doctor's pin resolver for one test.
func withPinResolver(t *testing.T, r pincheck.Resolver) {
	t.Helper()
	prev := pinResolver
	pinResolver = r
	t.Cleanup(func() { pinResolver = prev })
}

// TestDialCounterTrips proves the guard can fire: a lookup through the default
// resolver increments the count TestMain gates on, and a non-zero count is an
// error. The one lookup made here is taken back so the binary stays green.
func TestDialCounterTrips(t *testing.T) {
	if _, ok := pinResolver.(countingPinResolver); !ok {
		t.Fatalf("pinResolver is %T — TestMain should have installed the counting resolver", pinResolver)
	}
	before := pinDials.Load()
	_, err := pinResolver.Releases(context.Background(), pincheck.Site{Kind: pincheck.KindNode, Name: "node", Version: "24.19.0", Pinned: true})
	if err == nil {
		t.Error("the counting resolver answered — it must fail every lookup")
	}
	if got := pinDials.Load(); got != before+1 {
		t.Errorf("dial count %d → %d, want one increment", before, got)
	}
	pinDials.Add(-1)
	if pinDialErr(1) == nil || pinDialErr(0) != nil {
		t.Error("pinDialErr must fail on a non-zero count and pass on zero")
	}
}
