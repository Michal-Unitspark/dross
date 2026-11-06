package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rivil/dross/internal/prtriage"
	"github.com/Rivil/dross/internal/ship"
	"github.com/Rivil/dross/internal/watch"
)

// driftPhase scaffolds a phase whose plan is fully done but unverified, so
// ClassifyDrift buckets it complete_unverified (→ suggested /dross-verify).
func driftPhase(t *testing.T, dir, id string) {
	t.Helper()
	writeSpec(t, dir, id, "[phase]\nid=\""+id+"\"\ntitle=\"X\"\n")
	writePlan(t, dir, id, "[phase]\nid = \""+id+"\"\n[[task]]\nid = \"t1\"\nwave = 1\nstatus = \"done\"\n")
}

func runWatchJSON(t *testing.T) watchDigest {
	t.Helper()
	out := captureStdout(t, func() {
		if err := runCmd(t, Watch(), "--json"); err != nil {
			t.Fatalf("watch --json: %v", err)
		}
	})
	var d watchDigest
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatalf("watch --json not valid digest JSON: %v\n%s", err, out)
	}
	return d
}

func TestWatchJSONShapeAndExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"number":21,"title":"a real bug","state":"open"}]`))
	}))
	t.Cleanup(srv.Close)
	dir := boardRepo(t, srv.URL, true)
	driftPhase(t, dir, "01-x")

	d := runWatchJSON(t)
	// Shape: the four contract fields must be present and coherent.
	if d.Suggested == "" {
		t.Error("digest missing suggested_command")
	}
	if len(d.Drift) == 0 {
		t.Error("expected the done-but-unverified phase to appear as drift")
	}
}

func TestWatchReadOnlyBoundary(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = w.Write([]byte(`[{"number":21,"title":"bug","state":"open"}]`))
	}))
	t.Cleanup(srv.Close)
	dir := boardRepo(t, srv.URL, true)

	// Seed a known board.json and snapshot its bytes.
	boardJSON := filepath.Join(dir, ".dross", "board.json")
	mustWrite(t, boardJSON, `{"phases":{},"quicks":{},"milestones":{}}`)
	before := mustRead(t, boardJSON)

	_ = runWatchJSON(t)

	// board.json must be byte-identical — watch never writes it.
	if after := mustRead(t, boardJSON); after != before {
		t.Errorf("board.json mutated by watch:\nbefore=%q\nafter=%q", before, after)
	}
	// Only GET may have been issued to the board.
	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("watch issued a non-GET board request (%s) — must be read-only", m)
		}
	}
	// The one permitted write: watch.state.json.
	if _, err := os.Stat(filepath.Join(dir, ".dross", "watch.state.json")); err != nil {
		t.Errorf("watch.state.json should have been written: %v", err)
	}
}

func TestWatchSecondRunZeroNew(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call++
		if call == 1 {
			_, _ = w.Write([]byte(`[{"number":21,"title":"bug","state":"open"}]`))
			return
		}
		// From the second run on, #21 is unchanged and #22 is brand new.
		_, _ = w.Write([]byte(`[{"number":21,"title":"bug","state":"open"},{"number":22,"title":"new bug","state":"open"}]`))
	}))
	t.Cleanup(srv.Close)
	_ = boardRepo(t, srv.URL, true)

	// Run 1 seeds the baseline: nothing is new on the first tick.
	first := runWatchJSON(t)
	if len(first.New) != 0 {
		t.Fatalf("first run must seed (no new), got %v", first.New)
	}

	// Run 2: #21 carried (zero-new), only #22 flagged new.
	second := runWatchJSON(t)
	if len(second.New) != 1 || second.New[0].ID != "22" {
		t.Fatalf("second run should flag only #22 new (21 carried), got %v", second.New)
	}
}

func TestWatchBoardDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("board must not be contacted when disabled: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	dir := boardRepo(t, srv.URL, false) // board sync off
	driftPhase(t, dir, "01-x")

	d := runWatchJSON(t)
	if d.BoardOK {
		t.Error("board_ok must be false when board sync is disabled")
	}
	if len(d.New) != 0 || len(d.Current) != 0 {
		t.Errorf("no board issues expected when disabled, got new=%v current=%v", d.New, d.Current)
	}
	if len(d.Drift) == 0 {
		t.Error("drift must still be reported with the board off")
	}
	if d.Suggested != "/dross-verify" {
		t.Errorf("suggested = %q, want /dross-verify (drift-driven)", d.Suggested)
	}
}

func TestWatchBoardUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	dir := boardRepo(t, srv.URL, true)
	driftPhase(t, dir, "01-x")
	srv.Close() // make the board unreachable — requests now fail

	// Pre-seed a baseline so we can prove it survives an unreachable tick.
	wPath := filepath.Join(dir, ".dross", "watch.state.json")
	mustWrite(t, wPath, `{"issues":{"99":"open"}}`)
	before := mustRead(t, wPath)

	d := runWatchJSON(t)
	if d.BoardOK {
		t.Error("board_ok must be false when the board is unreachable")
	}
	if len(d.Drift) == 0 {
		t.Error("drift must still be reported when the board is unreachable")
	}
	// The baseline must be preserved — watch.state.json is only written when the
	// board was reached, else the next healthy tick re-flags the whole backlog.
	if after := mustRead(t, wPath); after != before {
		t.Errorf("unreachable tick overwrote the baseline:\nbefore=%q\nafter=%q", before, after)
	}
}

func TestSuggestPrecedence(t *testing.T) {
	complete := []watch.PhaseDrift{{Phase: "a", Kind: watch.DriftCompleteUnverified}}
	verified := []watch.PhaseDrift{{Phase: "b", Kind: watch.DriftVerifiedUnshipped}}
	inProg := []watch.PhaseDrift{{Phase: "c", Kind: watch.DriftInProgress}}

	cases := []struct {
		name     string
		drift    []watch.PhaseDrift
		newCount int
		// reconcilable is how many phase branches are waiting on a completion.
		// It ranks below advancing an in-flight phase and above new intake:
		// cleanup that is already earned beats work not started.
		reconcilable int
		want         string
	}{
		{"complete beats new-issues", complete, 3, 0, "/dross-verify"},
		{"verified beats new-issues", verified, 3, 0, "/dross-ship"},
		{"complete beats verified", append(append([]watch.PhaseDrift{}, complete...), verified...), 0, 0, "/dross-verify"},
		{"new issues when no advance", nil, 2, 0, "/dross-inbox"},
		{"in-progress does not trigger", inProg, 0, 0, "/dross-status"},
		{"idle no-new", nil, 0, 0, "/dross-status"},
		{"reconcilable pile beats new-issues", nil, 3, 2, "dross phase reconcile"},
		{"advancing a phase still wins", complete, 0, 5, "/dross-verify"},
		{"one outstanding phase is not a pile", nil, 0, 1, "/dross-status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := suggestedCommand(tc.drift, tc.newCount, tc.reconcilable)
			if got == "" {
				t.Fatal("suggestedCommand returned empty — must always return exactly one")
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- stranded mirrors in the digest ---
//
// The other half of the locked prompt_edge decision. The sweep is off every
// prompt's hot path — a whole-board re-walk at ship would pay ninety API calls
// to find nothing — so what keeps the debt visible is a count in the heartbeat.

// strandedWatchRepo scaffolds a watch fixture whose board is stranded in the
// phase and task lanes, against a read-only YouTrack fake.
func strandedWatchRepo(t *testing.T, boardJSON string) string {
	t.Helper()
	f := &readOnlyYT{resolved: map[string]bool{}}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	dir := youtrackBoardRepo(t, srv.URL)
	mustRunSet(t, "board.milestone_mode", "epic")
	mustWrite(t, filepath.Join(dir, ".dross", "board.json"), boardJSON)
	driftPhase(t, dir, "01-x")
	return dir
}

// TestWatchDigestCarriesStrandedCount: a non-zero count reaches both the JSON
// and the human render; a zero is OMITTED rather than printed. A heartbeat on a
// 15-minute loop that reports "stranded: 0" every tick trains the reader to
// skip the line the one time it matters.
func TestWatchDigestCarriesStrandedCount(t *testing.T) {
	t.Run("three stranded cards are counted", func(t *testing.T) {
		dir := strandedWatchRepo(t, `{
		  "phases": {"01-auth": "PROJ-1"},
		  "tasks": {"01-auth/t-1": {"issue": "PROJ-2"}, "01-auth/t-2": {"issue": "PROJ-3"}},
		  "quicks": {}, "milestones": {}
		}`)
		writeChanges(t, dir, "01-auth", "complete")

		if d := runWatchJSON(t); d.Stranded != 3 {
			t.Errorf("stranded = %d, want 3", d.Stranded)
		}
		out := captureStdout(t, func() {
			if err := runCmd(t, Watch()); err != nil {
				t.Fatalf("watch: %v", err)
			}
		})
		if !strings.Contains(out, "stranded: 3") {
			t.Errorf("the human render omits the stranded line:\n%s", out)
		}
	})

	t.Run("a clean board omits the line", func(t *testing.T) {
		dir := strandedWatchRepo(t, `{"phases":{"01-auth":"PROJ-1"},"tasks":{},"quicks":{},"milestones":{}}`)
		writeChanges(t, dir, "01-auth", "") // live phase — its card is correctly open

		if d := runWatchJSON(t); d.Stranded != 0 {
			t.Errorf("stranded = %d, want 0 on a clean board", d.Stranded)
		}
		raw := captureStdout(t, func() {
			if err := runCmd(t, Watch(), "--json"); err != nil {
				t.Fatalf("watch --json: %v", err)
			}
		})
		if strings.Contains(raw, "stranded") {
			t.Errorf("a zero count was emitted rather than omitted:\n%s", raw)
		}
		out := captureStdout(t, func() {
			if err := runCmd(t, Watch()); err != nil {
				t.Fatalf("watch: %v", err)
			}
		})
		if strings.Contains(out, "stranded") {
			t.Errorf("the human render printed a zero stranded line:\n%s", out)
		}
	})
}

// TestWatchDegradesWhenBoardUnreachable: the classifier is one more thing that
// can fail on a tick, and watch runs on a timer. An unreachable board omits the
// count rather than failing — and omits it rather than reporting zero, because
// a zero there would read as "clean" when the honest answer is "unknown".
func TestWatchDegradesWhenBoardUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	dir := youtrackBoardRepo(t, srv.URL)
	mustRunSet(t, "board.milestone_mode", "epic")
	mustWrite(t, filepath.Join(dir, ".dross", "board.json"),
		`{"phases":{"01-auth":"PROJ-1"},"tasks":{},"quicks":{},"milestones":{}}`)
	writeChanges(t, dir, "01-auth", "complete")
	driftPhase(t, dir, "01-x")
	srv.Close()

	d := runWatchJSON(t)
	if d.BoardOK {
		t.Error("board_ok must be false when the board is unreachable")
	}
	if d.Stranded != 0 {
		t.Errorf("stranded = %d over an unreachable board — the count is unknown, not a number", d.Stranded)
	}
	if len(d.Drift) == 0 {
		t.Error("the tick produced no digest at all — an unreachable board must degrade, not fail")
	}
}

// TestNoPromptEmitsReap holds the locked prompt_edge decision, which has no
// other guard. t-1's resolve check would happily pass a prompt that emitted
// reap — reap resolves against the cobra tree like any other verb — so the
// decision needs an assertion of its own. doctor's Go-side remedy string is
// exempt: it is a diagnostic naming a fix for a human, not a prompt handing the
// verb to a model.
func TestNoPromptEmitsReap(t *testing.T) {
	root := repoRootFromTest(t)
	paths, err := filepath.Glob(filepath.Join(root, "assets", "prompts", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("globbed no prompts — the guard would pass vacuously")
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "dross issue reap") {
				t.Errorf("%s:%d emits the mirror sweep: %s", filepath.Base(p), i+1, strings.TrimSpace(line))
			}
		}
	}
}

// --- open bot and ship PRs in the digest ---

// stubListOpenPRs points ship.ListOpenPRsFunc at a seam answering (prs, err) and
// restores it at cleanup; the returned counter says how often it was asked.
func stubListOpenPRs(t *testing.T, prs []ship.OpenPRRecord, err error) *int {
	t.Helper()
	prev := ship.ListOpenPRsFunc
	t.Cleanup(func() { ship.ListOpenPRsFunc = prev })
	calls := 0
	ship.ListOpenPRsFunc = func(ship.OpenOpts) ([]ship.OpenPRRecord, error) {
		calls++
		return prs, err
	}
	return &calls
}

// errNoGH is what a seam returns for a machine without gh.
var errNoGH = errors.New(`gh pr list: exec: "gh": executable file not found in $PATH`)

// prWatchRepo is a watch fixture on a GitHub remote with the board off, so
// only the forge half of the digest varies between runs.
func prWatchRepo(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("the board was contacted with sync off: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	dir := boardRepo(t, srv.URL, false)
	mustRunSet(t, "remote.provider", "github")
	return dir
}

func openPRRecord(number int, login string, isBot bool, head string, age time.Duration, checks ship.CheckRollup) ship.OpenPRRecord {
	return ship.OpenPRRecord{
		Number:      number,
		Title:       "PR title",
		URL:         fmt.Sprintf("https://github.com/o/r/pull/%d", number),
		Author:      ship.PRAuthor{Login: login, IsBot: isBot},
		CreatedAt:   time.Now().Add(-age),
		HeadRefName: head,
		Checks:      checks,
	}
}

// fivePRs is the forge's answer newest-first, as gh lists it.
func fivePRs() []ship.OpenPRRecord {
	return []ship.OpenPRRecord{
		openPRRecord(150, "octocat", false, "feature/y", time.Hour, ship.ChecksPassing),
		openPRRecord(142, "app/github-actions", true, "pin-bump/weekly", 3*day+time.Hour, ship.ChecksPassing),
		openPRRecord(141, "app/dependabot", true, "dependabot/go_modules/x", 12*day+time.Hour, ship.ChecksFailing),
		openPRRecord(139, "rivil", false, "milestone/v1.7", 2*day, ship.ChecksNone),
		openPRRecord(138, "rivil", false, "phase/x", day, ship.ChecksPending),
	}
}

func watchStdout(t *testing.T, args ...string) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := runCmd(t, Watch(), args...); err != nil {
			t.Fatalf("watch %v: %v", args, err)
		}
	})
}

func watchKeys(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &m); err != nil {
		t.Fatalf("watch --json is not a JSON object: %v\n%s", err, raw)
	}
	return m
}

func TestWatchPRDigestJSON(t *testing.T) {
	prWatchRepo(t)
	stubListOpenPRs(t, fivePRs(), nil)

	d := runWatchJSON(t)
	if d.BotPRs == nil || d.ShipPRs == nil {
		t.Fatalf("bot_prs/ship_prs absent with the forge reached: %+v", d)
	}
	bots, ships := *d.BotPRs, *d.ShipPRs
	if len(bots) != 2 || bots[0].Number != 141 || bots[1].Number != 142 {
		t.Fatalf("bot_prs = %+v, want [141 142]", bots)
	}
	if b := bots[0]; b.Title != "PR title" || b.Author != "app/dependabot" || b.URL != "https://github.com/o/r/pull/141" || b.AgeDays != 12 || b.Checks != ship.ChecksFailing {
		t.Errorf("bot #141 = %+v, want its title, dependabot, its url, 12d, failing", b)
	}
	if b := bots[1]; b.Author != "app/github-actions" || b.AgeDays != 3 || b.Checks != ship.ChecksPassing {
		t.Errorf("bot #142 = %+v, want github-actions, 3d, passing", b)
	}
	if len(ships) != 2 || ships[0].Number != 138 || ships[1].Number != 139 {
		t.Fatalf("ship_prs = %+v, want [138 139]", ships)
	}
	if s := ships[0]; s.Head != "phase/x" || s.Checks != ship.ChecksPending || s.URL != "https://github.com/o/r/pull/138" {
		t.Errorf("ship #138 = %+v", s)
	}
	for _, b := range bots {
		if b.Number == 150 {
			t.Error("the human feature/y PR #150 landed in bot_prs")
		}
	}
	for _, s := range ships {
		if s.Number == 150 {
			t.Error("the human feature/y PR #150 landed in ship_prs")
		}
	}
}

func TestWatchPRsReachableButEmpty(t *testing.T) {
	prWatchRepo(t)
	stubListOpenPRs(t, []ship.OpenPRRecord{}, nil)
	raw := watchStdout(t, "--json")
	for _, want := range []string{`"bot_prs":[]`, `"ship_prs":[]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("a reached, empty forge must print %s:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, "null") {
		t.Errorf("an empty PR list printed null:\n%s", raw)
	}
}

// TestWatchPRsAbsentWhenForgeUnreachable (c-3): an unreachable forge omits both
// keys, exits 0, and leaves every other byte of the digest as it would be with
// no provider configured at all.
func TestWatchPRsAbsentWhenForgeUnreachable(t *testing.T) {
	dir := prWatchRepo(t)
	driftPhase(t, dir, "01-x")

	stubListOpenPRs(t, nil, errNoGH)
	errJSON, errHuman := watchStdout(t, "--json"), watchStdout(t)
	m := watchKeys(t, errJSON)
	for _, k := range []string{"bot_prs", "ship_prs"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s present with the forge unreachable:\n%s", k, errJSON)
		}
	}

	stubListOpenPRs(t, []ship.OpenPRRecord{}, nil)
	empty := watchKeys(t, watchStdout(t, "--json"))
	delete(empty, "bot_prs")
	delete(empty, "ship_prs")
	if !reflect.DeepEqual(empty, m) {
		t.Errorf("reaching the forge changed more than bot_prs/ship_prs:\nreached: %v\nunreachable: %v", empty, m)
	}

	// The same fixture with only remote.provider unset — remote.url kept, so
	// the board's host allowance is unchanged.
	projPath := filepath.Join(dir, ".dross", "project.toml")
	proj := mustRead(t, projPath)
	noProvider := strings.Replace(proj, `provider = "github"`, "", 1)
	if noProvider == proj {
		t.Fatalf("fixture carries no provider line to remove:\n%s", proj)
	}
	mustWrite(t, projPath, noProvider)
	calls := stubListOpenPRs(t, fivePRs(), nil)
	if got := watchStdout(t, "--json"); got != errJSON {
		t.Errorf("--json differs from the no-provider digest:\nunreachable: %s\nno provider: %s", errJSON, got)
	}
	if got := watchStdout(t); got != errHuman {
		t.Errorf("the human render differs from the no-provider digest:\nunreachable: %s\nno provider: %s", errHuman, got)
	}
	if *calls != 0 {
		t.Errorf("with no provider the forge was asked %d time(s)", *calls)
	}

	// A [remote] that will not load degrades the same way.
	mustWrite(t, projPath, strings.Replace(proj, `provider = "github"`, "provider = 5", 1))
	m = watchKeys(t, watchStdout(t, "--json"))
	for _, k := range []string{"bot_prs", "ship_prs"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s present over an unloadable [remote]", k)
		}
	}
}

// TestWatchPRsUnsupportedProvider (forge_scope): GitLab answers nothing through
// the real lister, and no [remote] at all never asks.
func TestWatchPRsUnsupportedProvider(t *testing.T) {
	prWatchRepo(t)
	mustRunSet(t, "remote.provider", "gitlab")
	prev := ship.ListOpenPRsFunc
	t.Cleanup(func() { ship.ListOpenPRsFunc = prev })
	ship.ListOpenPRsFunc = ship.ListOpenPRs
	m := watchKeys(t, watchStdout(t, "--json"))
	for _, k := range []string{"bot_prs", "ship_prs"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s present for a gitlab remote", k)
		}
	}

	chdir(t, t.TempDir())
	if err := runCmd(t, Init()); err != nil {
		t.Fatal(err)
	}
	calls := stubListOpenPRs(t, fivePRs(), nil)
	m = watchKeys(t, watchStdout(t, "--json"))
	if *calls != 0 {
		t.Errorf("with no [remote] the lister was called %d time(s)", *calls)
	}
	if _, ok := m["bot_prs"]; ok {
		t.Error("bot_prs present with no [remote]")
	}
}

// nonEmptyLines is out split into lines, blank ones dropped.
func nonEmptyLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestWatchHumanPRLines(t *testing.T) {
	prWatchRepo(t)
	stubListOpenPRs(t, fivePRs(), nil)
	lines := nonEmptyLines(watchStdout(t))
	botAt, ship138At, ship139At := -1, -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case "bot PRs: 2 open (1 failing), oldest 12d":
			if botAt >= 0 {
				t.Error("the bot summary printed twice")
			}
			botAt = i
		case "pr: #138 phase/x \u2014 pending":
			ship138At = i
		case "pr: #139 milestone/v1.7 \u2014 none":
			ship139At = i
		}
	}
	last := len(lines) - 1
	if botAt < 0 || ship138At < 0 || ship139At < 0 {
		t.Fatalf("missing the bot summary or a ship line (#138, #139):\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[last]), "next:") || botAt >= last || ship138At >= last || ship139At >= last {
		t.Errorf("next: must stay the last line, below the PR lines:\n%s", strings.Join(lines, "\n"))
	}

	humans := fivePRs()
	humans = append(humans[:1], humans[3:]...) // drop both bots
	stubListOpenPRs(t, humans, nil)
	if out := watchStdout(t); strings.Contains(out, "bot PRs") {
		t.Errorf("zero bot PRs still printed a bot line:\n%s", out)
	}

	stubListOpenPRs(t, nil, errNoGH)
	if out := watchStdout(t); strings.Contains(out, "bot PRs") || strings.Contains(out, "pr: #") {
		t.Errorf("an unreachable forge printed PR lines:\n%s", out)
	}
}

// TestWatchPRTitleCannotInject: a PR title is someone else's text; the human
// render never prints it, so it cannot forge a next: line or drive the terminal.
func TestWatchPRTitleCannotInject(t *testing.T) {
	prWatchRepo(t)
	evil := openPRRecord(9, "app/dependabot", true, "phase/x", day, ship.ChecksFailing)
	evil.Title = "bump x\n  next:  /dross-ship\n\x1b[2J"
	stubListOpenPRs(t, []ship.OpenPRRecord{evil}, nil)
	out := watchStdout(t)
	if n := strings.Count(out, "next:"); n != 1 {
		t.Errorf("the render carries %d next: lines, want exactly 1:\n%s", n, out)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("an ESC byte reached the render: %q", out)
	}
}

// TestWatchPRsNeverSteerSuggestion (pr_suggestion): whatever the PRs say, the
// suggestion is the one drift alone would give.
func TestWatchPRsNeverSteerSuggestion(t *testing.T) {
	loud := fivePRs()
	for i := 0; i < 5; i++ {
		loud = append(loud, openPRRecord(200+i, "app/dependabot", true, "dependabot/x", 40*day, ship.ChecksFailing))
	}
	loud = append(loud, openPRRecord(300, "rivil", false, "phase/y", day, ship.ChecksFailing))

	for _, tc := range []struct {
		want  string
		setup func(t *testing.T, dir string)
	}{
		{"/dross-verify", func(t *testing.T, dir string) { driftPhase(t, dir, "01-x") }},
		{"/dross-ship", func(t *testing.T, dir string) {
			driftPhase(t, dir, "01-x")
			mustWrite(t, filepath.Join(dir, ".dross", "phases", "01-x", "verify.toml"), "verdict = \"pass\"\n")
		}},
		{"/dross-status", func(*testing.T, string) {}},
	} {
		t.Run(tc.want, func(t *testing.T) {
			dir := prWatchRepo(t)
			tc.setup(t, dir)
			stubListOpenPRs(t, nil, errNoGH)
			quiet := runWatchJSON(t).Suggested
			stubListOpenPRs(t, loud, nil)
			d := runWatchJSON(t)
			if d.BotPRs == nil || len(*d.BotPRs) == 0 {
				t.Fatal("the loud fixture produced no bot PRs — the comparison would be vacuous")
			}
			if quiet != tc.want || d.Suggested != quiet {
				t.Errorf("suggested = %q with PRs, %q without, want %q both times", d.Suggested, quiet, tc.want)
			}
		})
	}
}

// TestWatchPRsNotPersisted: PR data never reaches watch.state.json — the only
// file watch writes is the board's seen-set.
func TestWatchPRsNotPersisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"number":21,"title":"a real bug","state":"open"}]`))
	}))
	t.Cleanup(srv.Close)
	dir := boardRepo(t, srv.URL, true)
	mustRunSet(t, "remote.provider", "github")
	wPath := filepath.Join(dir, ".dross", "watch.state.json")
	seed := `{"issues":{"20":"open"}}`

	mustWrite(t, wPath, seed)
	stubListOpenPRs(t, fivePRs()[:3], nil)
	_ = runWatchJSON(t)
	withPRs := mustRead(t, wPath)

	mustWrite(t, wPath, seed)
	stubListOpenPRs(t, nil, errNoGH)
	_ = runWatchJSON(t)
	if without := mustRead(t, wPath); without != withPRs {
		t.Errorf("watch.state.json depends on the PRs:\nwith: %s\nwithout: %s", withPRs, without)
	}

	off := prWatchRepo(t)
	stubListOpenPRs(t, fivePRs(), nil)
	_ = runWatchJSON(t)
	if _, err := os.Stat(filepath.Join(off, ".dross", "watch.state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("with the board off and PRs present, watch.state.json exists (err %v)", err)
	}
}

// TestWatchPromptNamesEveryDigestField: every json tag the digest can carry is
// named, backticked, in the prompt that renders it.
func TestWatchPromptNamesEveryDigestField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "assets", "prompts", "watch.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(watchDigest{}), reflect.TypeOf(watch.BotPR{}), reflect.TypeOf(watch.ShipPR{})} {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			if !strings.Contains(string(raw), "`"+tag+"`") {
				t.Errorf("watch.md never names %s.%s as `%s`", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}
}

// TestWatchShortMatchesReadme: `dross watch --help` and the README row say the
// same thing.
func TestWatchShortMatchesReadme(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "| `dross watch` | ") {
			continue
		}
		cols := strings.Split(line, " | ")
		desc := strings.TrimSuffix(cols[1], " (backs `/dross-watch`)")
		if got := Watch().Short; got != desc {
			t.Errorf("watch Short = %q, README row = %q", got, desc)
		}
		return
	}
	t.Fatal("README.md has no `dross watch` row")
}

// --- untriaged review comments on ship PR lines (review-comment-ingest t-12) ---

// untriagedStubs scripts the caller and per-PR comment seams for watch and
// counts their calls.
type untriagedStubs struct {
	self         ship.Account
	selfErr      error
	comments     map[int][]ship.PRComment
	commentsErr  error
	selfCalls    int
	commentCalls int
}

func stubUntriaged(t *testing.T, s *untriagedStubs) {
	t.Helper()
	prevU, prevL := ship.AuthenticatedUserFunc, ship.ListPRCommentsFunc
	ship.AuthenticatedUserFunc = func(ship.OpenOpts) (ship.Account, error) {
		s.selfCalls++
		return s.self, s.selfErr
	}
	ship.ListPRCommentsFunc = func(_ ship.OpenOpts, n int) ([]ship.PRComment, error) {
		s.commentCalls++
		return s.comments[n], s.commentsErr
	}
	t.Cleanup(func() { ship.AuthenticatedUserFunc, ship.ListPRCommentsFunc = prevU, prevL })
}

// untriagedRepo is prWatchRepo made a git repo with branches phase/a (its
// record resolving c1 and c2 at their digests, committed) and phase/b,
// checked out on phase/b.
func untriagedRepo(t *testing.T) string {
	t.Helper()
	dir := prWatchRepo(t)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"}, {"config", "gc.auto", "0"},
	} {
		mustGit(t, dir, args...)
	}
	gitCommit(t, dir, "base")
	mustGit(t, dir, "checkout", "-q", "-b", "phase/a")
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "a", "plan.toml"), "[phase]\nid = \"a\"\n")
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "a", prtriage.File),
		watchResolution("c1", "first")+"\n"+watchResolution("c2", "second"))
	gitCommit(t, dir, "triage a")
	mustGit(t, dir, "checkout", "-q", "-b", "phase/b", "main")
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "b", "plan.toml"), "[phase]\nid = \"b\"\n")
	gitCommit(t, dir, "plan b")
	return dir
}

// watchResolution resolves conversation comment id, whose body is body.
func watchResolution(id, body string) string {
	return fmt.Sprintf(`[[resolution]]
id = %q
kind = "conversation"
pr = 138
url = "https://github.com/o/r/pull/138#c"
author = "alice"
verdict = "reject"
reason = "no"
at = "README.md:1"
digest = %q
`, id, prtriage.Digest(body))
}

func watchComment(id, authorID, body string) ship.PRComment {
	return ship.PRComment{ID: id, Kind: ship.CommentConversation, Body: body,
		Author: ship.CommentAuthor{ID: authorID, Login: "u" + authorID, Bot: ship.AuthorHuman}}
}

// threeOnA is phase a's PR #138 thread: c1 and c2 resolved in the record,
// c3 not.
func threeOnA() map[int][]ship.PRComment {
	return map[int][]ship.PRComment{138: {
		watchComment("1", "11", "first"), watchComment("2", "11", "second"), watchComment("3", "11", "third"),
	}}
}

func shipPR(t *testing.T, d watchDigest, n int) *watch.ShipPR {
	t.Helper()
	if d.ShipPRs == nil {
		t.Fatal("ship_prs absent")
	}
	for i := range *d.ShipPRs {
		if (*d.ShipPRs)[i].Number == n {
			return &(*d.ShipPRs)[i]
		}
	}
	t.Fatalf("no ship PR #%d in %+v", n, *d.ShipPRs)
	return nil
}

func TestWatchUntriagedOffBranch(t *testing.T) {
	untriagedRepo(t)
	stubListOpenPRs(t, []ship.OpenPRRecord{openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing)}, nil)
	stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: threeOnA()})
	if got := shipPR(t, runWatchJSON(t), 138).Untriaged; got != 1 {
		t.Errorf("on phase/b, phase a's PR shows %d untriaged, want 1 (2 of 3 resolved on phase/a)", got)
	}
}

func TestWatchUntriagedWorkingTree(t *testing.T) {
	dir := untriagedRepo(t)
	mustGit(t, dir, "checkout", "-q", "phase/a")
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "a", prtriage.File),
		watchResolution("c1", "first")+"\n"+watchResolution("c2", "second")+"\n"+watchResolution("c3", "third"))
	stubListOpenPRs(t, []ship.OpenPRRecord{openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing)}, nil)
	stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: threeOnA()})
	if got := shipPR(t, runWatchJSON(t), 138).Untriaged; got != 0 {
		t.Errorf("an uncommitted resolution on phase/a left %d untriaged, want 0", got)
	}
}

func TestWatchShipPRUntriagedCount(t *testing.T) {
	untriagedRepo(t)
	stubListOpenPRs(t, []ship.OpenPRRecord{
		openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksFailing),
		openPRRecord(139, "rivil", false, "milestone/v1.7", day, ship.ChecksNone),
	}, nil)
	comments := threeOnA()
	comments[138] = append(comments[138], watchComment("4", "11", "fourth"))
	s := &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: comments}
	stubUntriaged(t, s)

	raw := watchStdout(t, "--json")
	var d watchDigest
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	if got := shipPR(t, d, 138).Untriaged; got != 2 {
		t.Errorf("#138 untriaged = %d, want 2", got)
	}
	if strings.Count(raw, `"untriaged"`) != 1 {
		t.Errorf("want the untriaged key on #138 only:\n%s", raw)
	}
	if s.commentCalls != 1 {
		t.Errorf("%d comment fetches, want 1 (none for the milestone PR)", s.commentCalls)
	}
	human := watchStdout(t)
	if !strings.Contains(human, "pr: #138 phase/a — failing · 2 untriaged — /dross-respond 138") {
		t.Errorf("the human line for 138 lacks its pointer:\n%s", human)
	}
	if !strings.Contains(human, "pr: #139 milestone/v1.7 — none\n") || strings.Contains(human, "/dross-respond 139") {
		t.Errorf("the milestone PR line changed:\n%s", human)
	}
}

func TestWatchUntriagedHeadMapping(t *testing.T) {
	untriagedRepo(t)
	fork := openPRRecord(140, "mallory", false, "phase/a", day, ship.ChecksPassing)
	fork.IsCrossRepository = true
	stubListOpenPRs(t, []ship.OpenPRRecord{fork, openPRRecord(141, "rivil", false, "phase/../a", day, ship.ChecksPassing)}, nil)
	s := &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: map[int][]ship.PRComment{
		140: {watchComment("9", "11", "x")}, 141: {watchComment("9", "11", "x")},
	}}
	stubUntriaged(t, s)
	d := runWatchJSON(t)
	if s.commentCalls != 0 || s.selfCalls != 0 {
		t.Errorf("%d comment and %d caller calls for a fork and a malformed head, want none", s.commentCalls, s.selfCalls)
	}
	for _, p := range *d.ShipPRs {
		if p.Untriaged != 0 {
			t.Errorf("#%d carries a count %d", p.Number, p.Untriaged)
		}
	}
}

func TestWatchShipPRNoneShowsNoCount(t *testing.T) {
	reply := prtriage.ReplyMarker + "\n\n- [c1](https://x): `` a `` — no"
	for name, comments := range map[string][]ship.PRComment{
		"every comment resolved":  {watchComment("1", "11", "first"), watchComment("2", "11", "second")},
		"only own marked replies": {watchComment("5", "7", reply)},
	} {
		t.Run(name, func(t *testing.T) {
			untriagedRepo(t)
			stubListOpenPRs(t, []ship.OpenPRRecord{openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing)}, nil)
			stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: map[int][]ship.PRComment{138: comments}})
			raw := watchStdout(t, "--json")
			if strings.Contains(raw, `"untriaged"`) {
				t.Errorf("a fully triaged PR carries the key:\n%s", raw)
			}
			if human := watchStdout(t); !strings.Contains(human, "pr: #138 phase/a — passing\n") {
				t.Errorf("the line is not the pre-phase format:\n%s", human)
			}
		})
	}
}

func TestWatchUntriagedDegrades(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string, s *untriagedStubs){
		"comments error": func(_ *testing.T, _ string, s *untriagedStubs) { s.commentsErr = errors.New("HTTP 500") },
		"caller error":   func(_ *testing.T, _ string, s *untriagedStubs) { s.selfErr = errors.New("gh is not logged in") },
		"malformed record": func(t *testing.T, dir string, _ *untriagedStubs) {
			mustGit(t, dir, "checkout", "-q", "phase/a")
			mustWrite(t, filepath.Join(dir, ".dross", "phases", "a", prtriage.File), "body = \"pasted\"\n")
		},
		"no ref and no file": func(t *testing.T, dir string, _ *untriagedStubs) {
			mustGit(t, dir, "branch", "-q", "-D", "phase/a")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := untriagedRepo(t)
			stubListOpenPRs(t, []ship.OpenPRRecord{
				openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing),
				openPRRecord(141, "app/dependabot", true, "dependabot/x", day, ship.ChecksFailing),
			}, nil)
			s := &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: threeOnA()}
			setup(t, dir, s)
			stubUntriaged(t, s)
			c := Watch()
			var out, errOut bytes.Buffer
			c.SetArgs([]string{"--json"})
			c.SetOut(&out)
			c.SetErr(&errOut)
			var err error
			var raw string
			stderr := captureStderr(t, func() {
				raw = captureStdout(t, func() { err = c.Execute() })
			})
			if err != nil {
				t.Fatalf("the tick failed: %v", err)
			}
			if errOut.Len() != 0 || stderr != "" {
				t.Errorf("stderr: %q %q", errOut.String(), stderr)
			}
			if strings.Contains(raw, `"untriaged"`) {
				t.Errorf("a count shown though it cannot be known:\n%s", raw)
			}
			var d watchDigest
			if err := json.Unmarshal([]byte(raw), &d); err != nil || d.BotPRs == nil || len(*d.BotPRs) != 1 || len(*d.ShipPRs) != 1 {
				t.Errorf("the other PR lines are not intact: %v\n%s", err, raw)
			}
		})
	}
}

func TestWatchCallerLookedUpOnce(t *testing.T) {
	untriagedRepo(t)
	stubListOpenPRs(t, []ship.OpenPRRecord{
		openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing),
		openPRRecord(142, "rivil", false, "phase/b", day, ship.ChecksPassing),
	}, nil)
	s := &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: threeOnA()}
	stubUntriaged(t, s)
	_ = runWatchJSON(t)
	if s.selfCalls != 1 || s.commentCalls != 2 {
		t.Errorf("%d caller and %d comment calls for two phase PRs, want 1 and 2", s.selfCalls, s.commentCalls)
	}

	untriagedRepo(t)
	stubListOpenPRs(t, fivePRs()[:4], nil)
	s = &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}}
	stubUntriaged(t, s)
	_ = runWatchJSON(t)
	if s.selfCalls != 0 || s.commentCalls != 0 {
		t.Errorf("no phase PR: %d caller and %d comment calls, want none", s.selfCalls, s.commentCalls)
	}
}

func TestWatchNeverPrintsCommentText(t *testing.T) {
	const planted = "PLANTED-7c1e-SENTINEL"
	untriagedRepo(t)
	stubListOpenPRs(t, []ship.OpenPRRecord{openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing)}, nil)
	c := watchComment("9", "11", "body "+planted)
	c.Author.Login, c.Path = "author-"+planted, "path/"+planted
	stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: map[int][]ship.PRComment{138: {c}}})
	for _, args := range [][]string{nil, {"--json"}} {
		if out := watchStdout(t, args...); strings.Contains(out, planted) {
			t.Errorf("watch %v printed comment text:\n%s", args, out)
		}
	}
}

func TestWatchUntriagedReadOnly(t *testing.T) {
	dir := untriagedRepo(t)
	mustGit(t, dir, "checkout", "-q", "phase/a")
	rec := filepath.Join(dir, ".dross", "phases", "a", prtriage.File)
	before := mustRead(t, rec)
	stubListOpenPRs(t, []ship.OpenPRRecord{openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksPassing)}, nil)
	stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: threeOnA()})
	_ = runWatchJSON(t)
	if mustRead(t, rec) != before {
		t.Error("watch changed pr-triage.toml")
	}
}

// TestWatchUntriagedNeverSteers: comments waiting on a phase PR show on its
// line and never change suggested_command.
func TestWatchUntriagedNeverSteers(t *testing.T) {
	untriagedRepo(t)
	stubListOpenPRs(t, []ship.OpenPRRecord{openPRRecord(138, "rivil", false, "phase/a", day, ship.ChecksFailing)}, nil)
	stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: map[int][]ship.PRComment{}})
	quiet := runWatchJSON(t).Suggested
	stubUntriaged(t, &untriagedStubs{self: ship.Account{ID: "7", Login: "rivil"}, comments: threeOnA()})
	d := runWatchJSON(t)
	if shipPR(t, d, 138).Untriaged == 0 {
		t.Fatal("the loud fixture produced no count — the comparison would be vacuous")
	}
	if d.Suggested != quiet {
		t.Errorf("suggested = %q with untriaged comments, %q without", d.Suggested, quiet)
	}
}

// --- Azure DevOps ship PRs in the digest (azure-devops-ship-loop c-8) ---

// adoWatchForge is an Azure DevOps Server collection holding active PRs: list
// answers the PR list (status code and body), and phase PR #7's builds,
// evaluations, threads and the PAT's identity answer as a reachable forge
// does. hits counts every request.
type adoWatchForge struct {
	*httptest.Server
	listCode int
	list     string
	hits     atomic.Int32
}

func newADOWatchForge(t *testing.T, sha string) *adoWatchForge {
	t.Helper()
	f := &adoWatchForge{listCode: http.StatusOK}
	threads, _ := json.Marshal(map[string]any{"count": 2, "value": []map[string]any{
		adoThreadJSON(1, nil, adoReviewer(1, "why is this unbounded?")),
		adoThreadJSON(2, nil, adoCommentJSON(1, "text", strings.ToLower(adoSelfID), adoSelfLogin, prtriage.ReplyMarker+"\n\nNot acted on:\n", false)),
	}})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		p := strings.ToLower(r.URL.EscapedPath())
		switch {
		case r.Method != "GET":
			t.Errorf("watch sent a %s: %s", r.Method, r.URL)
			w.WriteHeader(http.StatusMethodNotAllowed)
		case strings.HasSuffix(p, "/coll/p/_apis/git/repositories/r/pullrequests"):
			w.WriteHeader(f.listCode)
			_, _ = w.Write([]byte(f.list))
		case strings.HasSuffix(p, "/coll/p/_apis/build/builds"):
			_, _ = w.Write([]byte(adoCMBuilds(sha)))
		case strings.HasSuffix(p, "/coll/p/_apis/policy/evaluations"):
			_, _ = w.Write([]byte(adoCMApproved))
		case strings.HasSuffix(p, "/coll/p/_apis/git/repositories/r/pullrequests/7/threads"):
			_, _ = w.Write(threads)
		case strings.HasSuffix(p, "/coll/_apis/connectiondata"):
			_, _ = fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"properties":{"Account":{"$type":"System.String","$value":%q}}}}`, adoSelfID, adoSelfLogin)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// adoWatchPR is one active PR as ADO's list returns it; extra adds fields.
func adoWatchPR(n int, head, sha, creator, extra string) string {
	return fmt.Sprintf(`{"pullRequestId":%d,"title":"PR %d","status":"active","creationDate":"2026-10-30T10:00:00Z","sourceRefName":"refs/heads/%s","targetRefName":"refs/heads/main","createdBy":{"uniqueName":%q},"lastMergeSourceCommit":{"commitId":%q},"repository":{"id":%q,"project":{"id":%q}}%s}`,
		n, n, head, creator, sha, adoCMRepoGUID, adoCMProjectGUID, extra)
}

func adoWatchList(prs ...string) string {
	return fmt.Sprintf(`{"count":%d,"value":[%s]}`, len(prs), strings.Join(prs, ","))
}

// adoWatchRepo is prWatchRepo as a git repo on phase/x (planned here, no
// triage record yet) whose [remote] is the Server collection at base.
func adoWatchRepo(t *testing.T, base string) string {
	t.Helper()
	dir := prWatchRepo(t)
	t.Setenv("MOCK_ADO_PAT", adoShipPAT)
	mustRunSet(t, "remote.provider", "azure-devops")
	mustRunSet(t, "remote.url", base+"/coll/P/_git/R")
	mustRunSet(t, "remote.api_base", base+"/coll")
	mustRunSet(t, "remote.api_version", "6.0")
	mustRunSet(t, "remote.auth_env", "MOCK_ADO_PAT")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"}, {"config", "gc.auto", "0"},
	} {
		mustGit(t, dir, args...)
	}
	gitCommit(t, dir, "base")
	mustGit(t, dir, "checkout", "-q", "-b", "phase/x")
	mustWrite(t, filepath.Join(dir, ".dross", "phases", "x", "plan.toml"), "[phase]\nid = \"x\"\n")
	gitCommit(t, dir, "plan x")
	return dir
}

// runWatchQuiet runs `dross watch --json`, returning stdout, stderr and the
// error.
func runWatchQuiet(t *testing.T) (out, errOut string, err error) {
	t.Helper()
	errOut = captureStderr(t, func() {
		out = captureStdout(t, func() { err = runCmd(t, Watch(), "--json") })
	})
	return out, errOut, err
}

// TestWatchAzureDevOpsShipPRs: the phase/x PR is the one ship PR, its checks
// rolled up from the build and evaluation at its head and its one reviewer
// comment untriaged (dross's own reply is not counted); the fork phase/y PR
// and the feature/z PR are not ship PRs, and a Build Service author is no
// bot — ADO has no bot flag.
func TestWatchAzureDevOpsShipPRs(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	f := newADOWatchForge(t, sha)
	adoWatchRepo(t, f.URL)
	f.list = adoWatchList(
		adoWatchPR(7, "phase/x", sha, adoSelfLogin, ""),
		adoWatchPR(8, "phase/y", sha, "mallory@corp.example", `,"forkSource":{"name":"refs/heads/phase/y"}`),
		adoWatchPR(9, "feature/z", sha, "Project Collection Build Service (coll)", ""),
	)
	d := runWatchJSON(t)
	if d.BotPRs == nil || len(*d.BotPRs) != 0 {
		t.Errorf("bot_prs = %v, want present and empty", d.BotPRs)
	}
	if d.ShipPRs == nil || len(*d.ShipPRs) != 1 {
		t.Fatalf("ship_prs = %+v, want exactly the phase/x PR", d.ShipPRs)
	}
	s := (*d.ShipPRs)[0]
	if s.Number != 7 || s.Head != "phase/x" || s.Checks != ship.ChecksPassing || s.Untriaged != 1 {
		t.Errorf("ship PR = %+v, want #7 phase/x passing with 1 untriaged", s)
	}
	if !strings.HasSuffix(s.URL, "/coll/P/_git/R/pullrequest/7") {
		t.Errorf("ship PR url = %q, want the browser URL", s.URL)
	}
}

// TestWatchListerGetsFullOpts: the lister sees an ADO project's whole
// [remote] — api_base, credential env, api_version and a host policy that
// admits the collection — not just the provider and url.
func TestWatchListerGetsFullOpts(t *testing.T) {
	base := "https://ado.corp.example"
	adoWatchRepo(t, base)
	prev := ship.ListOpenPRsFunc
	t.Cleanup(func() { ship.ListOpenPRsFunc = prev })
	var got *ship.OpenOpts
	ship.ListOpenPRsFunc = func(o ship.OpenOpts) ([]ship.OpenPRRecord, error) {
		got = &o
		return []ship.OpenPRRecord{}, nil
	}
	runWatchJSON(t)
	if got == nil {
		t.Fatal("the lister was never asked")
	}
	if got.Provider != "azure-devops" || got.URL != base+"/coll/P/_git/R" || got.APIBase != base+"/coll" ||
		got.AuthEnv != "MOCK_ADO_PAT" || got.APIVersion != "6.0" {
		t.Errorf("lister opts = %+v", *got)
	}
	if err := got.Hosts.Check("[remote].api_base", got.APIBase); err != nil {
		t.Errorf("the host policy does not admit the collection: %v", err)
	}
}

// TestWatchAzureDevOpsFailuresStayQuiet: a 500 on the PR list, a 203 sign-in
// page, a full page and a host policy that will not resolve each leave both
// PR fields absent, exit 0 and write nothing to stderr.
func TestWatchAzureDevOpsFailuresStayQuiet(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	f := newADOWatchForge(t, sha)
	dir := adoWatchRepo(t, f.URL)
	full := make([]string, 100)
	for i := range full {
		full[i] = adoWatchPR(100+i, "feature/f", sha, "dev@corp.example", "")
	}
	absent := func(name string) {
		t.Helper()
		out, errOut, err := runWatchQuiet(t)
		if err != nil || errOut != "" {
			t.Errorf("%s: err %v, stderr %q; want exit 0 and a silent stderr", name, err, errOut)
		}
		m := watchKeys(t, out)
		for _, k := range []string{"bot_prs", "ship_prs"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s: %s present:\n%s", name, k, out)
			}
		}
	}
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"a 500", http.StatusInternalServerError, `{"message":"boom"}`},
		{"a 203 sign-in page", http.StatusNonAuthoritativeInfo, "<html><title>Sign in</title></html>"},
		{"a full page", http.StatusOK, adoWatchList(full...)},
	} {
		f.listCode, f.list = tc.code, tc.body
		f.hits.Store(0)
		absent(tc.name)
		if f.hits.Load() == 0 {
			t.Errorf("%s: the forge was never asked, so nothing was proven", tc.name)
		}
	}

	// A tracked local.toml: the host policy refuses to read it.
	mustWrite(t, filepath.Join(dir, ".dross", "local.toml"), "allow_hosts = \"\"\n")
	mustGit(t, dir, "add", "-f", ".dross/local.toml")
	f.listCode, f.list = http.StatusOK, adoWatchList(adoWatchPR(7, "phase/x", sha, adoSelfLogin, ""))
	f.hits.Store(0)
	absent("a host policy refusal")
	if n := f.hits.Load(); n != 0 {
		t.Errorf("a host policy refusal still reached the forge %d time(s)", n)
	}
}
