package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// optionsPrompt reads the /dross-options prompt, which is the only surface that
// claims to reach EVERY dross-managed setting.
func optionsPrompt(t *testing.T) string {
	t.Helper()
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "assets", "prompts", "options.md"))
	if err != nil {
		t.Fatalf("read options.md: %v", err)
	}
	return string(b)
}

// TestOptionsCoversEveryLocalKey walks localKeys itself rather than a hand-typed
// list, so a key added to the store without a line in the prompt fails here.
//
// The store is the case that motivated this test: .dross/local.toml is
// gitignored, absent from `dross project show`, and every value in it was
// written by a one-shot command. If the settings surface does not name a key,
// nothing else will, and the user finds out it exists by reading source.
func TestOptionsCoversEveryLocalKey(t *testing.T) {
	prompt := optionsPrompt(t)
	for key := range localKeys {
		if !strings.Contains(prompt, key) {
			t.Errorf("options.md does not mention the settable local key %q", key)
		}
	}
}

// TestOptionsCoversTheConsentVerbs pins the keys `dross local set` deliberately
// REFUSES.
//
// They are the ones a settings surface is most likely to miss, precisely
// because the generic key-writer cannot reach them — and the ones whose absence
// costs most, since a stale grant or a lapsed trust is silent until a verify
// refuses to run.
func TestOptionsCoversTheConsentVerbs(t *testing.T) {
	prompt := optionsPrompt(t)
	for _, want := range []string{
		"mutation remote grant",
		"mutation remote revoke",
		"mutation remote status",
		"dross trust",
		"trusted_test_command",
		"mutation_remote_host",
		// The lane grant is the newest member of the same family, and the
		// one a settings surface is most likely to miss: it is keyed per
		// lane, so a user who never opens local.toml has no way to learn
		// that renaming a lane silently untrusts it.
		"dross test lane add",
		"dross trust --lane",
		"trusted_lane_commands",
		// --selector changes what a consented lane spawns, so a settings
		// surface claiming to reach every dross-managed setting cannot omit
		// the one field that does that.
		"--selector",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("options.md does not mention %q", want)
		}
	}
	// ...and it must not tell the user to write a grant key through the generic
	// writer, which would be the consent_model decision inverted.
	for _, forbidden := range []string{
		"local set mutation_remote_host",
		"local set mutation_remote_workdir",
		"local set trusted_test_command",
		"local set trusted_lane_commands",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("options.md tells the user to write a consent key via the generic key-writer: %q", forbidden)
		}
	}
}

// TestOptionsCoversEveryProjectSection is the drift guard for project.toml.
//
// options.md instructs the reader to STOP on a field it does not cover, calling
// that a schema-vs-prompt drift bug. That instruction was itself unenforced:
// [board] and [mutation] were printed by `dross project show` while the prompt
// named neither. This asserts the top-level sections; adding a new one to the
// Project struct without a section here fails.
func TestOptionsCoversEveryProjectSection(t *testing.T) {
	prompt := optionsPrompt(t)
	for _, section := range []string{
		"project.", "stack.", "runtime.", "repo.", "remote.",
		"board.", "mutation.", "paths.", "env.", "goals.",
		"constraints", "competition",
	} {
		if !strings.Contains(prompt, section) {
			t.Errorf("options.md does not cover the project.toml section %q", section)
		}
	}
}

// TestOptionsCoversTheProviderConditionalRemoteFields.
//
// These three are the fields a reader skims past because they are empty in the
// common case — and each one is load-bearing for exactly one provider, where a
// missing value produces an auth failure that names nothing useful.
func TestOptionsCoversTheProviderConditionalRemoteFields(t *testing.T) {
	prompt := optionsPrompt(t)
	for _, want := range []string{
		"remote.auth_user", "remote.auth_scheme", "remote.project_id", "remote.api_version",
		"azure-devops",
		"board.auth_user", "board.github_project", "board.milestone_mode",
		"stack.profile",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("options.md does not mention %q", want)
		}
	}

	// The azure-devops auth guidance follows the auth_scheme lock: Basic :PAT by
	// default, bearer for Entra, private-token/basic ineffective, auth_user
	// ignored. The api_version bullet carries its default and the Server caps.
	for key, wants := range map[string][]string{
		"remote.auth_scheme": {"azure-devops", "`bearer` sends an Entra ID token", "`private-token` and `basic` have no effect"},
		"remote.auth_user":   {"azure-devops ignores it"},
		"remote.api_version": {"azure-devops", "`7.1`", "`5.0`", "`6.0`", "`7.0`"},
	} {
		bullet := optionsBullet(prompt, key)
		if bullet == "" {
			t.Errorf("options.md has no `- `%s`` bullet", key)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(bullet, want) {
				t.Errorf("the %s bullet does not say %q: %s", key, want, bullet)
			}
		}
	}
}

// optionsBullet returns the options.md bullet line that documents key.
func optionsBullet(prompt, key string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "- `"+key+"` —") {
			return line
		}
	}
	return ""
}

// TestOptionsSaysMutationIsNotSettableThroughProjectSet.
//
// `dross project set mutation.adapters` fails — there is no dotted-path writer
// for [mutation]. A prompt that listed the field without that caveat would send
// the user into an error and leave the adapters allowlist unset, which is the
// state that makes `dross doctor` probe every toolchain and fail a Go-only repo
// over a missing npx.
func TestOptionsSaysMutationIsNotSettableThroughProjectSet(t *testing.T) {
	prompt := optionsPrompt(t)
	idx := strings.Index(prompt, "## 13. Mutation adapters")
	if idx < 0 {
		t.Fatal("options.md has no mutation adapters section")
	}
	section := prompt[idx:]
	if end := strings.Index(section[3:], "\n## "); end >= 0 {
		section = section[:end+3]
	}
	if !strings.Contains(section, "not settable") {
		t.Error("the mutation section does not say the fields are unsettable through `dross project set`")
	}
	if !strings.Contains(section, "mutation.adapters") {
		t.Error("the mutation section does not name mutation.adapters")
	}
}

// TestOptionsSectionNumbersAreContiguous keeps the numbering honest: the prompt
// cross-references sections by number ("see §11", "see §14"), and a duplicated
// or skipped heading silently points the reader at the wrong one.
func TestOptionsSectionNumbersAreContiguous(t *testing.T) {
	prompt := optionsPrompt(t)
	seen := 0
	for _, line := range strings.Split(prompt, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		rest := strings.TrimPrefix(line, "## ")
		dot := strings.Index(rest, ".")
		if dot <= 0 {
			continue // "## Section pick" and friends carry no number
		}
		n, err := strconv.Atoi(rest[:dot])
		if err != nil {
			continue
		}
		if n != seen {
			t.Errorf("section heading %q breaks the sequence: expected %d", line, seen)
		}
		seen = n + 1
	}
	if seen < 17 {
		t.Errorf("options.md has only %d numbered sections; the settings surface lost one", seen)
	}
}

// TestReadmeDocumentsTestLanes is the documentation half of the lane surface,
// on the TestDocsCoverAllowHosts precedent: a flag nobody can find is a flag
// that does not exist.
//
// It names the two refusal exit codes as well as the verbs. Those are the
// codes a caller reads when a run did NOT happen, and an agent that treats an
// undocumented non-zero as a red suite is one commit away from the false-green
// the whole exit contract exists to prevent.
func TestReadmeDocumentsTestLanes(t *testing.T) {
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(b)
	for _, want := range []string{
		"dross test --files",
		"dross test lane add",
		"dross test lane remove",
		"runtime.test_lane",
		"dross trust --lane",
		// The pre-selector wording, kept intact: it is still one of the two
		// ways a run measures nothing, and an agent reading only the new
		// clause would have no name for the plain no-lane case.
		"matched no lane",
		// The selector surface. A shipped flag with no README line is a flag
		// nobody can find, which is the same rule `dross test --files`
		// already answers to.
		"--selector",
		"go-package",
		"--empty-exit",
		"selector miss",
		// The prepare surface. A bootstrap line is code the repo asks this
		// machine to run before its tests, so a reader who cannot find it
		// documented cannot know it exists to review.
		"--prepare",
		"dross test lane edit",
		// The template surface. It reaches runners the closed selector enum
		// cannot shape, and it is the one scoping field that changes the
		// consent line — a reader who cannot find it documented cannot know
		// why their granted lane went stale.
		"--selector-template",
		"--selector-join",
		"{path}",
		"{paths}",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not document %q", want)
		}
	}
	// The c-4 warning, in the README as well as at the two surfaces that print
	// it. It describes a lane that VALIDATES and still measures the whole
	// suite, so a reader who never meets the wording has no name for the one
	// failure a green scoped run can hide.
	for _, want := range []string{"./...", "runs the union"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not document the whole-tree warning: %q missing", want)
		}
	}
	// Exit 5 now has two causes, and an agent that only ever learned the first
	// reads an all-miss run as an unexplained non-zero — which is exactly the
	// state the exit contract exists to keep it out of.
	if !strings.Contains(readme, "every matched lane's selector collected nothing") {
		t.Error("README.md's exit-5 contract does not cover the all-miss run")
	}
	// Exit 7 is documented AND placed. The position is the contract: an agent
	// that read 7 as ranking below a red suite would commit on a run where a
	// lane never got as far as being measured.
	if !strings.Contains(readme, "`7`") {
		t.Fatal("README.md's `dross test` row does not name exit 7")
	}
	order := []string{"transport", "partial", "prepare", "red", "refused", "nothing-measured"}
	at := -1
	for _, name := range order {
		i := strings.Index(readme, "> "+name)
		if name == "transport" {
			i = strings.Index(readme, "mislead: transport")
		}
		if i < 0 {
			t.Fatalf("README.md's stated precedence order omits %q", name)
		}
		if i <= at {
			t.Errorf("README.md states %q out of precedence order", name)
		}
		at = i
	}
}

// TestOptionsDocumentsTheSelectorSurfaceCorrectly guards the two ways the
// settings prompt could describe this surface wrongly: by sending the reader to
// hand-edit project.toml, or by advertising a flag the command does not
// register.
//
// The remove-then-re-add assertion is RE-POINTED rather than dropped. It used
// to require the prompt to route match, command, selector and empty_exit
// through remove-then-re-add; lane-selector-custom made all four editable in
// place, so a doc still saying that sends the reader to a workaround that
// DROPS the lane's grant. What the guard protects is unchanged — a doc must
// never route a lane change around the CLI's refusals — so it now requires the
// prompt to name the in-place field set, and still refuses any instruction to
// open the [[runtime.test_lane]] block by hand.
func TestOptionsDocumentsTheSelectorSurfaceCorrectly(t *testing.T) {
	prompt := optionsPrompt(t)

	// Only the lane's NAME is remove-then-re-add now, and the prompt has to
	// say so: it keys the consent store, so it cannot move without the grant
	// moving with it.
	if !strings.Contains(prompt, "remove-then-re-add") {
		t.Error("options.md no longer says which lane change is remove-then-re-add")
	}
	if !strings.Contains(prompt, "dross test lane edit") {
		t.Error("options.md does not name `dross test lane edit`")
	}
	// Every field the edit verb writes must be named. A prompt that omitted
	// one would leave the reader believing that field still needs the
	// grant-dropping workaround.
	for _, field := range []string{
		"--match", "--command", "--prepare", "--selector",
		"--selector-template", "--selector-join", "--empty-exit",
		"--toolchain", "--install",
	} {
		if !strings.Contains(prompt, field) {
			t.Errorf("options.md does not name the in-place-editable field %q", field)
		}
	}
	// The claim the phase falsified must be gone: a SENTENCE that routes one
	// of those four fields through remove-then-re-add is the failure this
	// guard was re-pointed to catch. Sentences, not lines — a markdown
	// paragraph is one line, and this one legitimately says "a blank command"
	// and "an empty match list" describing what the refusal gate catches.
	//
	// Word-bounded, so `trusted_lane_commands` is not read as the word
	// "command": the key name has to appear in the very sentence that explains
	// why the lane's NAME is the exception.
	editable := regexp.MustCompile(`\b(match|command|selector|empty_exit)\b`)
	namesTheException := false
	for _, sentence := range strings.Split(prompt, ". ") {
		if !strings.Contains(sentence, "remove-then-re-add") {
			continue
		}
		if got := editable.FindString(sentence); got != "" {
			t.Errorf("options.md still routes %s through remove-then-re-add: %q", got, sentence)
		}
		if regexp.MustCompile(`\bname\b`).MatchString(sentence) {
			namesTheException = true
		}
	}
	if !namesTheException {
		t.Error("options.md does not say that the lane's NAME is the one field remove-then-re-add still applies to")
	}
	for _, forbidden := range []string{
		"hand-edit runtime.test_lane",
		"edit the [[runtime.test_lane]] block",
		"edit runtime.test_lane",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("options.md tells the reader to hand-edit a lane: %q", forbidden)
		}
	}

	// Every --flag advertised for a lane verb, in options.md AND in the
	// README, has to be one the cobra command actually registers. A doc naming
	// a flag that does not exist sends the user to a refusal.
	//
	// Both verbs are parsed, not just `lane add`: the edit verb now carries the
	// larger flag set, so a guard reading only the add examples would leave the
	// half most likely to drift unchecked.
	addFlags := map[string]bool{}
	testLaneAdd().Flags().VisitAll(func(f *pflag.Flag) { addFlags[f.Name] = true })
	editFlags := map[string]bool{}
	testLaneEdit().Flags().VisitAll(func(f *pflag.Flag) { editFlags[f.Name] = true })

	root := repoRootForDocs(t)
	readmeBytes, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs := []struct{ name, body string }{
		{"options.md", prompt},
		{"README.md", string(readmeBytes)},
	}
	for _, verb := range []struct {
		name       string
		registered map[string]bool
	}{
		{"dross test lane add", addFlags},
		{"dross test lane edit", editFlags},
	} {
		flagRe := regexp.MustCompile(regexp.QuoteMeta(verb.name) + `[^\n` + "`" + `]*`)
		checked := 0
		for _, doc := range docs {
			for _, example := range flagRe.FindAllString(doc.body, -1) {
				for _, tok := range strings.Fields(example) {
					if !strings.HasPrefix(tok, "--") {
						continue
					}
					name := strings.TrimPrefix(strings.SplitN(tok, "=", 2)[0], "--")
					name = strings.Trim(name, "`*_,.|")
					if name == "" {
						continue
					}
					checked++
					if !verb.registered[name] {
						t.Errorf("%s advertises `%s --%s`, which the command does not register (in %q)", doc.name, verb.name, name, example)
					}
				}
			}
		}
		if checked == 0 {
			t.Errorf("no `%s` flag example was found in either doc — the guard is vacuous for that verb", verb.name)
		}
	}
}

// TestReadmeDocumentsDetachedRuns: a shipped verb with no README line is a verb
// nobody can find, which is the rule `dross test --files` already answers to.
//
// The exit codes matter most. `verify results` reports five states through five
// codes precisely so a caller can poll without parsing prose, and a contract
// nobody wrote down is one every caller re-derives differently — the same
// argument the `dross test` exit table already carries.
func TestReadmeDocumentsDetachedRuns(t *testing.T) {
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(b)
	for _, want := range []string{
		"--detach",
		"dross verify results",
		"dross verify status",
		"--cancel",
		// The scheduling half. An --at with no documented meaning leaves the
		// reader guessing whose clock it is on, which is the one thing that
		// decides whether an off-hours run lands off-hours.
		"--at",
		"host's clock",
		// The provenance rule the whole fetch rests on.
		"recorded at dispatch",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not document %q", want)
		}
	}
	// The states are the contract a poller reads. Documented as a set, because
	// a reader who learned only "finished" treats every other code as failure —
	// including the two that mean the run is alive and fine.
	for _, state := range []string{"scheduled", "running", "finished", "unreachable", "gone"} {
		if !strings.Contains(readme, state) {
			t.Errorf("README.md does not document the %q result state", state)
		}
	}
}

// TestReadmeDocumentsLaneHostAffinity is the documentation half of per-lane
// routing, on the TestDocsCoverAllowHosts precedent: a behaviour nobody can
// find documented is one a reader meets first as a surprise.
//
// The announcement contract is the part that most needs writing down. A run
// whose lanes landed on two machines LOOKS like a fault the first time you see
// it — and without a line saying it is expected, the reader's next move is to
// revoke a grant to make the transcript legible again, which is precisely the
// workaround this whole surface exists to end.
func TestReadmeDocumentsLaneHostAffinity(t *testing.T) {
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(b)

	for _, want := range []string{
		// The pool itself. It was reachable from `--add` and from the store
		// and documented in neither, so a reader had no way to learn that a
		// second host could be authorized at all.
		"--add",
		"[[remote_pool]]",
		"candidate zero",
		// c-1: a lane moves rather than coming home.
		"local is the last resort",
		"one probe per candidate",
		// c-4: the split flag, and that it is expected rather than a fault.
		"split across",
		"not interchangeable evidence",
		// c-2: the four surfaces that must agree, named, so a reader can tell
		// the guarantee from a coincidence.
		"granted, but unreachable",
		"can never name a machine the next run would not use",
		// c-3: bootstrap spans the pool.
		"provisions **every** granted candidate",
		"could not be reached",
		// c-4's persisted half.
		"measured_on",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not document %q", want)
		}
	}

	// The four surfaces c-2 names are named individually: a sentence promising
	// "every surface agrees" without listing them is a claim the reader cannot
	// check, and the one that is missing is the one that drifted.
	agree := strings.Index(readme, "can never name a machine the next run would not use")
	if agree < 0 {
		t.Fatal("README.md states no resolution guarantee at all")
	}
	// The window ends AT the phrase: the enumeration precedes it, and reaching
	// past it would let a mention further down the row stand in for a name
	// missing from the guarantee itself.
	clause := readme[max(0, agree-700):agree]
	for _, surface := range []string{"dross doctor", "dross remote status", "dross remote bootstrap", "dross test lane install"} {
		if !strings.Contains(clause, surface) {
			t.Errorf("the resolution guarantee does not name %q — the unnamed surface is the one that drifts", surface)
		}
	}
}

// TestVerifyPromptTeachesTheDetachedPath: the prompt is what an agent reads
// before deciding how to run a two-hour leg. One that only describes the
// attached form will keep holding a session for the length of the run, which is
// the entire problem this phase exists to remove — the feature would ship and
// never be used.
func TestVerifyPromptTeachesTheDetachedPath(t *testing.T) {
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "assets", "prompts", "verify.md"))
	if err != nil {
		t.Fatalf("read assets/prompts/verify.md: %v", err)
	}
	prompt := string(b)
	for _, want := range []string{
		"--detach",
		"dross verify results",
		"dross verify status",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("assets/prompts/verify.md does not name %q", want)
		}
	}
	// The exit codes that mean "no verdict was produced" must be called out,
	// or an agent reads a still-running run as a failed one and sends the user
	// to fix code that was never measured — the same false-red the `dross test`
	// exit contract exists to prevent.
	if !strings.Contains(prompt, "did not report") && !strings.Contains(prompt, "not a verdict") {
		t.Error("assets/prompts/verify.md does not tell the agent that a non-finished " +
			"result state is not a verdict about the code")
	}
	// The host lock's reading, beside the detached strings: a scheduled run
	// naming a holder is a wait, not a stall, and --no-wait is for an
	// unattended probe — an agent that cancelled a waiting run, or reached
	// for --no-wait to "speed it up", would be acting on the opposite reading.
	for _, want := range []string{"waiting on", "not a failure", "--no-wait"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("assets/prompts/verify.md does not teach %q beside the detached path", want)
		}
	}
}

// TestReadmeDocumentsTheHostLock pins the facts a reader of the README needs
// to predict a wait: where the lock is, that a leg waits and can refuse, that
// the suite waits up to a cap and then shares the host, how a wait is named,
// and that a dead holder leaves nothing to clear.
func TestReadmeDocumentsTheHostLock(t *testing.T) {
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(b)
	for _, want := range []string{
		"/tmp/dross-host.lock",
		"waiting on",
		"running alongside",
		"--wait",
		"10m",
		"--wait 0",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not document %q", want)
		}
	}
	if !strings.Contains(readme, "no stale lock") && !strings.Contains(readme, "nothing to clear") {
		t.Error("README.md does not say a dead holder leaves nothing to clear")
	}
	verifyRow := readmeRow(t, "dross verify <phase>`")
	if !strings.Contains(verifyRow, "--no-wait") || !strings.Contains(verifyRow, "`15`") {
		t.Errorf("the dross verify row does not document --no-wait with exit 15:\n%s", verifyRow)
	}
	testRow := readmeRow(t, "dross test [selector...]")
	if !strings.Contains(testRow, "--wait") || !strings.Contains(testRow, "10m") || !strings.Contains(testRow, "--wait 0") {
		t.Errorf("the dross test row does not document --wait / 10m / --wait 0:\n%s", testRow)
	}
	doctorRow := readmeRow(t, "dross doctor`")
	if !strings.Contains(doctorRow, "flock") || !strings.Contains(doctorRow, "host busy") {
		t.Errorf("the dross doctor row does not document flock and host busy:\n%s", doctorRow)
	}
}

// TestResultsRowStillEnumeratesTenToFourteen: the waiting state is documented
// as exit 10 with a holder reason, not as a new code (locked
// detached_waiting_state). The row's backticked numbers are the contract a
// poller reads, and they must be exactly 10–14.
func TestResultsRowStillEnumeratesTenToFourteen(t *testing.T) {
	row := readmeRow(t, "dross verify results <phase>")
	codes := map[string]bool{}
	for _, m := range regexp.MustCompile("`([0-9]+)`").FindAllStringSubmatch(row, -1) {
		codes[m[1]] = true
	}
	want := []string{"10", "11", "12", "13", "14"}
	for _, c := range want {
		if !codes[c] {
			t.Errorf("results row does not enumerate exit %s", c)
		}
	}
	if len(codes) != len(want) {
		t.Errorf("results row enumerates %v, want exactly 10–14", codes)
	}
}

// TestArchitectureDocumentsTheHostLock pins the facts a future editor would
// simplify away and break c-7 or c-4 with: the path, the protected_regular
// open rule, the conv=nocreat write, and the read -t lease.
func TestArchitectureDocumentsTheHostLock(t *testing.T) {
	root := repoRootForDocs(t)
	b, err := os.ReadFile(filepath.Join(root, "ARCHITECTURE.md"))
	if err != nil {
		t.Fatalf("read ARCHITECTURE.md: %v", err)
	}
	doc := string(b)
	for _, want := range []string{"/tmp/dross-host.lock", "protected_regular", "conv=nocreat", "read -t"} {
		if !strings.Contains(doc, want) {
			t.Errorf("ARCHITECTURE.md does not document %q", want)
		}
	}
}
