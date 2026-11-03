package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// promptContent loads one prompt out of assets/prompts/ and normalises it —
// lowercased, with markdown emphasis and backticks stripped — so assertions
// test the presence of a rule, not its exact formatting.
func promptContent(t *testing.T, name string) string {
	t.Helper()
	root := repoRootFromTest(t)
	b, err := os.ReadFile(filepath.Join(root, "assets", "prompts", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	s := strings.ToLower(string(b))
	return strings.NewReplacer("`", "", "*", "", "_", "").Replace(s)
}

// shipPromptContent is promptContent pinned to ship.md, the prompt most of this
// file's assertions are about.
func shipPromptContent(t *testing.T) string {
	t.Helper()
	return promptContent(t, "ship.md")
}

// TestShipPromptRecoverySection (c-5) gates the recovery cookbook: all three
// mid-merge failure states and both recovery commands must be present, and the
// section must never instruct manual .dross/ surgery (the drift the cookbook
// exists to prevent).
func TestShipPromptRecoverySection(t *testing.T) {
	content := shipPromptContent(t)

	// Required: the three failure-state phrases + both recovery commands.
	for _, n := range []string{
		"fast-forward",
		"diverged",
		"dirty tree",
		"dross phase complete --recover",
		"dross ship recover",
	} {
		if !strings.Contains(content, n) {
			t.Errorf("ship.md recovery section missing required phrase %q", n)
		}
	}

	// Forbidden: manual .dross/ surgery presented as a user step. The whole
	// point is that a dross command owns the restore — reintroducing these
	// must fail the gate.
	for _, n := range []string{
		"git add .dross",
		"-- .dross/",
	} {
		if strings.Contains(content, n) {
			t.Errorf("ship.md must not instruct manual .dross/ surgery (found %q)", n)
		}
	}
}

// TestShipPromptReadsTypedLandmarks proves c-1's consumer side: ship.md §3.5
// reads the structured `landmarks` array of {feature, symbol, loc, what} objects
// from `dross changes show`, and no longer parses a notes string for the
// landmark. Regressing §3.5 to "notes is a landmark" reintroduces the forbidden
// phrase and fails this.
func TestShipPromptReadsTypedLandmarks(t *testing.T) {
	content := shipPromptContent(t)
	for _, needle := range []string{"landmarks", "feature", "symbol", "loc", "what"} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md §3.5 must read the typed landmark fields: missing %q", needle)
		}
	}
	if strings.Contains(content, "notes is a landmark") {
		t.Error("ship.md §3.5 must not parse the notes string for the landmark (legacy phrasing survived)")
	}
}

// TestShipPromptAutoFastPath (c-2) gates the non-interactive --auto path: ship.md
// must document an --auto fast-path that skips the §1 body-preview dump, the §2
// body-override prompt, and the §3 reviewer prompt, and shells out to
// `dross ship --auto`. The uniquely-added tokens ("dross ship --auto",
// "non-interactive", "returns without merging") are absent from the rest of the
// prompt, so deleting the fast-path section drops them and fails this test.
func TestShipPromptAutoFastPath(t *testing.T) {
	content := shipPromptContent(t)

	// Tokens unique to the fast-path section — these carry the fail-on-removal
	// guarantee (none appear elsewhere in ship.md).
	for _, needle := range []string{
		"dross ship --auto",       // shells out to the non-interactive CLI path
		"non-interactive",         // the section's defining property
		"returns without merging", // c-4: --auto opens the PR and stops
	} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md --auto fast-path missing unique token %q", needle)
		}
	}

	// The section must name what it skips: the body-override and reviewer
	// prompts (and the body preview).
	for _, needle := range []string{"skip", "body override", "reviewers"} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md --auto fast-path must name skipped step %q", needle)
		}
	}
}

// TestShipPromptGitLabSections proves c-4: ship.md's §5 (CI gate) and §6 (merge
// gate) carry the GitLab pipeline-watch and squash-merge steps, and §5 pins the
// ENTIRE locked pipeline_status_mapping — terminal, keep-polling, AND ambiguous
// states — not just the terminal ones. Dropping any branch of the mapping or
// either ship step removes its token and fails this. Tokens are matched against
// shipPromptContent, which lowercases and strips underscores (so merge_requests
// -> mergerequests, should_remove_source_branch -> shouldremovesourcebranch).
// (r-01: the prompt edit is only live for the running binary after `make install`;
// this reads the source file, gating the committed prompt directly.)
func TestShipPromptGitLabSections(t *testing.T) {
	content := shipPromptContent(t)

	// §5 GitLab CI-watch endpoint + both auth schemes.
	for _, needle := range []string{
		"projects/<id>/pipelines",
		"pipelines?sha",
		"private-token",
		"bearer",
	} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md §5 missing GitLab CI-watch token %q", needle)
		}
	}

	// Locked pipeline_status_mapping — every branch must be documented.
	for _, state := range []string{
		"success", "failed", "canceled", // terminal
		"running", "pending", "created", "preparing", // keep-polling
		"manual", "skipped", // surface-and-ask
	} {
		if !strings.Contains(content, state) {
			t.Errorf("ship.md §5 dropped pipeline status %q from the locked mapping", state)
		}
	}

	// No-pipeline-for-the-SHA ask path.
	if !strings.Contains(content, "empty pipelines array") {
		t.Error("ship.md §5 missing the no-pipeline (empty pipelines array) surface-and-ask path")
	}

	// §6 GitLab squash-merge endpoint (underscore-stripped form).
	if !strings.Contains(content, "mergerequests") {
		t.Errorf("ship.md §6 missing GitLab squash-merge endpoint token %q", "mergerequests")
	}
	// The remote-branch removal flag is deliberately GONE. gitlab-ship-provider
	// pinned `should_remove_source_branch` as part of the locked merge mapping;
	// completion-state-truth supersedes that, because provider-side teardown is
	// now dross's job on every provider (see TestShipPromptNoUnguardedSwitch).
	if strings.Contains(content, "shouldremovesourcebranch") {
		t.Error("ship.md §6 must not ask GitLab to remove the source branch — `dross phase complete` owns the teardown")
	}
}

// TestShipPromptNoUnguardedSwitch (c-1) is the executable guard on the hole
// this phase closes: no step of /dross-ship may switch branches outside dross's
// guarded primitives. `gh pr merge --delete-branch` does its own raw checkout of
// the base branch — that is what destroyed a live state.json on the
// state-json-branch-safety ship — and a raw `git checkout` in the prompt is the
// same hole with the user's hands on it.
//
// The whole file is scanned, not just §4-to-EOF: §0's pre-flight step 5 has a
// guarded replacement now (`dross phase checkout`), so there is nowhere left
// that legitimately types one.
func TestShipPromptNoUnguardedSwitch(t *testing.T) {
	content := shipPromptContent(t)

	for _, forbidden := range []string{
		"--delete-branch",
		"shouldremovesourcebranch", // should_remove_source_branch
		"deletesourcebranch",       // delete_source_branch, deleteSourceBranch
		"closesourcebranch",        // close_source_branch (Bitbucket)
		"bypasspolicy",             // bypassPolicy (Azure DevOps)
		"autocompletesetby",        // autoCompleteSetBy (Azure DevOps)
		"git checkout",
		"git switch",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("ship.md must not contain %q — every branch switch and branch deletion goes through dross", forbidden)
		}
	}
}

// rawPhaseSwitch matches an instruction to switch onto a phase branch with git
// rather than `dross phase checkout`. Deliberately narrower than "mentions git
// checkout": `git checkout -- <files>` (quick.md's abort path) restores files
// and switches nothing, and pause.md's hard rules *forbid* checkout by naming
// it. What is banned is the branch-switching form aimed at phase/<id> — the one
// that replays a tracked .dross/state.json over the live machine-local copy.
// `git switch` has no path-restore form at all, so any use of it is a switch.
var rawPhaseSwitch = regexp.MustCompile(`git\s+checkout[^\n]*phase/|git\s+switch\b`)

// TestNoPromptPerformsRawBranchSwitch (c-1) generalises the ship.md guard to
// every prompt. ship.md was fixed first and covered by
// TestShipPromptNoUnguardedSwitch, while execute.md, plan.md, spec.md and
// resume.md kept telling the user to type `git checkout phase/<id>` — the same
// hole, four more doors, none of them scanned. A guard bound to one file only
// proves that file is clean.
func TestNoPromptPerformsRawBranchSwitch(t *testing.T) {
	root := repoRootFromTest(t)
	dir := filepath.Join(root, "assets", "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		scanned++
		content := promptContent(t, e.Name())
		for i, line := range strings.Split(content, "\n") {
			if rawPhaseSwitch.MatchString(line) {
				t.Errorf("%s:%d instructs a raw branch switch — use `dross phase checkout <id>`, which guards the live state.json:\n  %s",
					e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
	// A mis-rooted walk finds nothing and would otherwise report success.
	if scanned == 0 {
		t.Fatalf("scanned no prompts under %s — the guard proved nothing", dir)
	}

	// The negative alone is satisfied by deleting the instruction. Each prompt
	// that needs to get the user onto the phase branch must still say how.
	for _, name := range []string{"execute.md", "plan.md", "spec.md", "resume.md"} {
		if !strings.Contains(promptContent(t, name), "dross phase checkout") {
			t.Errorf("%s must name `dross phase checkout` as the way onto the phase branch", name)
		}
	}
}

// TestShipPromptNamesCompleteAsTeardownOwner (c-1, c-3): with the provider's
// delete flags gone, the prompt has to say who does the deletion instead — and
// §6.3's description of `dross phase complete` has to stop describing the old
// behaviour (a switch to main, and a chore commit carrying the record).
func TestShipPromptNamesCompleteAsTeardownOwner(t *testing.T) {
	// Collapse whitespace so multi-word needles match across line wraps.
	content := strings.Join(strings.Fields(shipPromptContent(t)), " ")

	for _, needle := range []string{
		"dross phase complete performs the local and remote",
		"on every provider",
	} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md §6 must name `dross phase complete` as the teardown owner: missing %q", needle)
		}
	}
	for _, stale := range []string{
		"switches to main",
		"chore commit",
	} {
		if strings.Contains(content, stale) {
			t.Errorf("ship.md §6.3 still describes the retired completion behaviour: %q", stale)
		}
	}
}

// TestShipPromptAutoBackfill proves ship-architecture-autogen: an absent
// ARCHITECTURE.md self-heals automatically on the interactive ship path (c-1),
// --auto documents skipping that backfill (c-2), and the backfill is non-blocking
// (c-3). Regressing §3.5 back to a manual-run-or-skip drops these needles.
func TestShipPromptAutoBackfill(t *testing.T) {
	// Collapse whitespace so multi-word needles match across line wraps.
	content := strings.Join(strings.Fields(shipPromptContent(t)), " ")
	// c-1: absent doc self-heals via an automatic backfill.
	for _, needle := range []string{"self-heal", "automatically backfill"} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md §3.5 must auto-backfill an absent ARCHITECTURE.md: missing %q", needle)
		}
	}
	// c-1 regression guard: must not punt to a manual run.
	if strings.Contains(content, "generate it first") {
		t.Error("ship.md §3.5 regressed to a manual /dross-architecture run (should auto-backfill)")
	}
	// c-2: --auto documents skipping the auto-backfill.
	if !strings.Contains(content, "auto-backfill") {
		t.Error("ship.md must document the ARCHITECTURE.md auto-backfill (incl. the --auto skip)")
	}
	// c-3: the backfill is non-blocking.
	for _, needle := range []string{"non-blocking", "must never block the ship"} {
		if !strings.Contains(content, needle) {
			t.Errorf("ship.md backfill must be non-blocking: missing %q", needle)
		}
	}
}

// TestShipPromptEmitsTerminalBoardStatuses proves c-6 and its successor: ship
// moves the board issue to shipped once the PR merges, and the terminal
// complete belongs to `dross phase complete`, which finalizes the board itself.
//
// The bare `phase sync <phase-id> --close` that once closed the card is why
// "shipped" and "complete" sat in both forge state maps as keys nothing ever
// resolved. ship's own `--status complete --close` line has since moved into
// the binary (boardsync.FinalizePhase), so a prompt still carrying it would
// close the phase card a second time.
func TestShipPromptEmitsTerminalBoardStatuses(t *testing.T) {
	content := shipPromptContent(t)

	marks := []struct {
		name string
		at   int
	}{
		{"squash-merge bullet", strings.Index(content, "squash-merge via provider")},
		{"--status shipped call", strings.Index(content, "phase sync <phase-id> --status shipped")},
		{"dross phase complete", strings.Index(content, "dross phase complete <phase-id>")},
	}
	for _, m := range marks {
		if m.at < 0 {
			t.Fatalf("ship.md has no %s", m.name)
		}
	}

	// Order, not mere presence: shipped is the state once the PR merges but
	// before the phase finalizes, so it must be emitted before complete runs.
	for i := 1; i < len(marks); i++ {
		if marks[i].at <= marks[i-1].at {
			t.Errorf("%s (at %d) must come after %s (at %d)", marks[i].name, marks[i].at, marks[i-1].name, marks[i-1].at)
		}
	}

	if strings.Contains(content, "phase sync <phase-id> --status complete --close") {
		t.Error("ship.md still closes the phase card itself — dross phase complete finalizes the board, so this would close it a second time")
	}
	// The pattern that produced the dead map entries.
	if strings.Contains(content, "phase sync <phase-id> --close") {
		t.Error("ship.md still closes the board issue with a bare --close — that call emits no status, which is what left shipped and complete as state-map keys nothing resolves")
	}

	// The retry. complete exits non-zero when the board did not follow; the
	// step that runs it and the cookbook must both name the command that
	// finishes the job, or the failure reads as the completion failing and an
	// agent reaches for --recover.
	const finalize = "dross issue phase finalize <phase-id>"
	for _, heading := range []string{"## 6. merge gate", "## recovery"} {
		if !strings.Contains(promptSection(t, content, heading), finalize) {
			t.Errorf("ship.md %q never names %q — a board that did not finalize has no named retry", heading, finalize)
		}
	}
}

// TestShipPromptClosesTaskCardsAfterPhaseComplete is c-2's emission half, now
// owned by the binary. The task cards still close after the phase finishes —
// boardsync.FinalizePhase, run last by `dross phase complete`, moves each to
// task-complete — so ship.md must say so on the complete step and must no
// longer carry a task close of its own, which would re-close cards the
// finalizer already closed.
func TestShipPromptClosesTaskCardsAfterPhaseComplete(t *testing.T) {
	content := shipPromptContent(t)

	if strings.Contains(content, "dross issue task sync <phase-id> --status task-complete --close") {
		t.Error("ship.md still closes the task cards itself — dross phase complete finalizes them")
	}
	var step string
	for _, line := range strings.Split(promptSection(t, content, "## 6. merge gate"), "\n") {
		if strings.Contains(line, "dross phase complete <phase-id>") {
			step = line
			break
		}
	}
	if step == "" {
		t.Fatal("ship.md §6 no longer carries the `dross phase complete` step")
	}
	for _, needle := range []string{"task-complete", "phase card to complete"} {
		if !strings.Contains(step, needle) {
			t.Errorf("ship.md's `dross phase complete` step must say it closes the board cards (%q missing):\n%s", needle, step)
		}
	}
}

// TestShipPromptReRunIsTheRetry (c-3): the "do not re-run dross ship" guidance
// is gone — a re-run is the sanctioned retry. §5's On-failure block says so,
// §4's step list describes the gated push -> open-or-reuse -> record -> mark
// order the binary actually runs, and Recovery gains the record-push item.
func TestShipPromptReRunIsTheRetry(t *testing.T) {
	content := shipPromptContent(t)

	for _, stale := range []string{"do not re-run", "would open a second pr"} {
		if strings.Contains(content, stale) {
			t.Errorf("ship.md still carries the retired guidance %q", stale)
		}
	}

	onFailure := promptSection(t, content, "on failure:")
	if !strings.Contains(onFailure, "re-run dross ship") && !strings.Contains(onFailure, "re-running dross ship") {
		t.Errorf("ship.md §5 On-failure must name re-running dross ship as safe:\n%s", onFailure)
	}
	for _, needle := range []string{"dross ship --force", "git pull --rebase origin phase/<id>"} {
		if !strings.Contains(onFailure, needle) {
			t.Errorf("ship.md §5 On-failure must name %q for the refusal it answers", needle)
		}
	}
	// A re-run reports the number, not a URL: changes.json stores none. The
	// slice runs from the re-run sentence to the next heading.
	rerun := strings.Index(onFailure, "re-run")
	if rerun < 0 {
		rerun = strings.Index(onFailure, "re-running")
	}
	if rerun >= 0 && strings.Contains(onFailure[rerun:], "<pr-url>") {
		t.Error("ship.md §5 claims a re-run returns a PR URL — changes.json stores none")
	}

	recovery := promptSection(t, content, "## recovery")
	for _, needle := range []string{"record push", "dross ship"} {
		if !strings.Contains(recovery, needle) {
			t.Errorf("ship.md Recovery must name %q", needle)
		}
	}

	// §4's CLI step list: the gate precedes the open, the record precedes
	// the mark, and the mark is explicitly last.
	steps := promptSection(t, content, "## 4. ship")
	order := []string{"gates phase/<id> on origin", "opens the pr", "commits the pr record", "marks the phase shipped"}
	last := -1
	for _, needle := range order {
		at := strings.Index(steps, needle)
		if at < 0 {
			t.Errorf("ship.md §4 step list missing %q", needle)
			continue
		}
		if at < last {
			t.Errorf("ship.md §4 step %q is out of order", needle)
		}
		last = at
	}
}

// TestShipPromptTestsBeforeDocsCommit: the ARCHITECTURE.md commit changes the
// tree verify measured, so §3.5 must run a full `dross test` before it — the
// commit gate refuses a code commit without a recorded green for that tree.
func TestShipPromptTestsBeforeDocsCommit(t *testing.T) {
	sec := sectionOf(promptBody(t, "ship.md"), "## 3.5", "## 4.")
	commit := strings.Index(sec, `git commit -m "docs(`)
	if commit < 0 {
		t.Fatal("ship.md §3.5 no longer commits ARCHITECTURE.md with `git commit -m \"docs(` — re-point this test")
	}
	before := false
	for _, loc := range bareDrossTestRE.FindAllStringIndex(sec, -1) {
		if loc[0] < commit {
			before = true
		}
	}
	if !before {
		t.Error("ship.md §3.5 has no bare `dross test` line before the docs commit")
	}
}

// rawShipSection returns ship.md's raw text from heading to the next "## "
// heading. Raw, not normalised: the Forgejo body is matched as written,
// capital D and all.
func rawShipSection(t *testing.T, heading string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "assets", "prompts", "ship.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	at := strings.Index(doc, heading)
	if at < 0 {
		t.Fatalf("ship.md has no %q section", heading)
	}
	rest := doc[at+len(heading):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		rest = rest[:next]
	}
	return rest
}

// TestShipPromptReShipsAfterEveryCIFix (c-7): a fix pushed after the PR opened
// is followed by a required `dross ship <phase-id>`, so the freshness gate sees
// it before CI is watched again — and the old "also safe" wording, which made
// the re-ship optional, is gone.
func TestShipPromptReShipsAfterEveryCIFix(t *testing.T) {
	content := shipPromptContent(t)
	onFailure := promptSection(t, content, "on failure:")
	push := strings.Index(onFailure, "git push origin phase/<id>")
	// The unconditional step, not the re-ship inside the stale-refusal branch.
	reship := strings.Index(onFailure, "re-run dross ship <phase-id> — required")
	loop := strings.Index(onFailure, "loop back")
	if push < 0 || reship < 0 || loop < 0 {
		t.Fatalf("ship.md §5 On failure lacks a step: push %d, re-ship %d, loop back %d", push, reship, loop)
	}
	if !(push < reship && reship < loop) {
		t.Errorf("§5 On failure must push, then re-run dross ship <phase-id>, then loop back (at %d, %d, %d)", push, reship, loop)
	}
	if strings.Contains(content, "re-running dross ship is also safe") {
		t.Error("ship.md still calls the re-ship optional ('also safe') — after a fix it is required")
	}
}

// TestShipPromptReShipsBeforeMerge (c-7): §6 re-runs `dross ship <phase-id>`
// before every provider's merge call, so a fix pushed after the pass cannot
// merge under it, and a stale refusal is answered with /dross-verify.
func TestShipPromptReShipsBeforeMerge(t *testing.T) {
	gate := rawShipSection(t, "## 6. Merge gate")
	reship := strings.Index(gate, "`dross ship <phase-id>`")
	if reship < 0 {
		t.Fatal("ship.md §6 never re-runs `dross ship <phase-id>`")
	}
	for _, call := range []string{"gh pr merge", "/pulls/<n>/merge", `{"Do":"squash"}`, `{"squash":true}`} {
		at := strings.Index(gate, call)
		if at < 0 {
			t.Errorf("ship.md §6 lost its merge call %q", call)
			continue
		}
		if reship > at {
			t.Errorf("§6's re-ship comes after the merge call %q — a stale pass could merge", call)
		}
	}
	onFailure := rawShipSection(t, "**On failure:**")
	if !strings.Contains(gate, "/dross-verify") && !strings.Contains(onFailure, "/dross-verify") {
		t.Error("neither §5 On failure nor §6 names /dross-verify as the answer to a stale refusal")
	}
}

// prChecksWatch matches a `gh pr checks` invocation carrying --watch on the
// same line, in any argument order.
var prChecksWatch = regexp.MustCompile("gh pr checks[^\n`]*--watch")

// TestShipPromptGitHubCIWatchWaitsForRuns: `gh pr checks --watch` exits 0 as
// soon as the checks registered SO FAR pass, so with only a fast third-party
// check registered and the Actions run still queued it reads green with CI
// unrun (seen on #138, worked around by hand on every ship since). §5's GitHub
// arm waits for the head SHA's runs to register, watches each run, and only
// then reads the whole check set — in that order. No prompt may gate on the
// --watch form, so a milestone or review flow cannot reintroduce it.
func TestShipPromptGitHubCIWatchWaitsForRuns(t *testing.T) {
	gate := rawShipSection(t, "## 5. CI gate")
	steps := []string{"gh run list --commit <sha>", "gh run watch <run-id> --exit-status", "gh pr checks <pr-url>"}
	last := -1
	for _, step := range steps {
		at := strings.Index(gate, step)
		if at < 0 {
			t.Errorf("ship.md §5 lost its GitHub step %q", step)
			continue
		}
		if at < last {
			t.Errorf("ship.md §5 runs %q out of order — register, watch each run, then read every check", step)
		}
		last = at
	}

	dir := filepath.Join(repoRootFromTest(t), "assets", "prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		seen++
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			// The one sanctioned mention is §5's own warning naming the form.
			if m := prChecksWatch.FindString(line); m != "" && !strings.Contains(line, "never gate on `"+m) {
				t.Errorf("%s gates CI on %q — it exits green before the Actions run registers", e.Name(), m)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no prompts found — the ban checked nothing")
	}
}

// --- §5 / §6 on Azure DevOps and Bitbucket (azure-devops-ship-loop c-5, c-6, c-10, c-11) ---

// shipCIExits is every exit status `dross ship ci` can end with, read from the
// binary: the verdict constants, and the status any untagged error maps to.
func shipCIExits() map[int]string {
	return map[int]string{
		0:                         "pass",
		exitCIFail:                "fail",
		exitCIPending:             "pending",
		exitCINone:                "none",
		exitCIAsk:                 "ask",
		ExitCode(errors.New("x")): "error",
	}
}

// shipExitLine is the one line of section that carries "exit <code> = <word>".
func shipExitLine(t *testing.T, section string, code int, word string) string {
	t.Helper()
	needle := fmt.Sprintf("exit %d = %s", code, word)
	for _, line := range strings.Split(section, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Errorf("no line carries %q", needle)
	return ""
}

// shipADOArm is section from the Azure DevOps / Bitbucket arm that opens with
// start to the next line that opens a sibling bullet or a bold block.
func shipADOArm(t *testing.T, section, start string) string {
	t.Helper()
	at := strings.Index(section, start)
	if at < 0 {
		t.Fatalf("no Azure DevOps / Bitbucket arm opening %q", start)
	}
	arm := section[at:]
	for _, end := range []string{"\n- ", "\non failure:", "\n2. mark the board"} {
		if i := strings.Index(arm, end); i >= 0 {
			arm = arm[:i]
		}
	}
	return arm
}

// TestShipPromptAzureDevOpsCI: §5 polls `dross ship ci <phase-id>` on Azure
// DevOps and Bitbucket every ~30s, maps every verdict to its next step, and
// never reads none or ask as a pass.
func TestShipPromptAzureDevOpsCI(t *testing.T) {
	gate := promptSection(t, shipPromptContent(t), "## 5. ci gate")
	arm := shipADOArm(t, gate, "azure devops / bitbucket: poll dross ship ci <phase-id> every ~30s")
	for _, want := range []string{"~10-minute window", "none and ask are never a pass", "only exit 0 is"} {
		if !strings.Contains(arm, want) {
			t.Errorf("§5's Azure DevOps / Bitbucket arm must say %q", want)
		}
	}
	for _, step := range []struct {
		code    int
		verdict string
		next    []string
	}{
		{0, "pass", []string{"go to §6"}},
		{exitCIFail, "fail", []string{"on failure", "link"}},
		{exitCIPending, "pending", []string{"keep polling"}},
		{exitCINone, "none", []string{"keep polling", "proceed without ci or stop"}},
		{exitCIAsk, "ask", []string{"proceed or stop at once", "reason shown"}},
	} {
		line := shipExitLine(t, arm, step.code, step.verdict)
		for _, want := range step.next {
			if !strings.Contains(line, want) {
				t.Errorf("§5's %s line must say %q: %s", step.verdict, want, line)
			}
		}
		if step.verdict != "pass" && strings.Contains(line, "go to §6") {
			t.Errorf("§5 sends %s to the merge gate: %s", step.verdict, line)
		}
	}
	if strings.Contains(arm, "curl") {
		t.Error("§5's Azure DevOps / Bitbucket arm hands the model a curl call")
	}
}

// TestShipPromptStaleHeadAsks: a pending that still carries a reason — the PR
// does not carry the watched head — is asked about at the window's end, with
// the reason shown, instead of being polled forever.
func TestShipPromptStaleHeadAsks(t *testing.T) {
	gate := promptSection(t, shipPromptContent(t), "## 5. ci gate")
	line := shipExitLine(t, gate, exitCIPending, "pending")
	for _, want := range []string{"pending that carries a reason", "end of a ~10-minute window", "proceed or stop", "reason shown"} {
		if !strings.Contains(line, want) {
			t.Errorf("§5's pending line must say %q: %s", want, line)
		}
	}
	// A pending stuck in the queue reaches the same window-end ask: it is the
	// other state polling alone would never leave.
	queued, ask := strings.Index(line, "a pending (queued)"), strings.Index(line, "end of a ~10-minute window")
	if queued < 0 || ask < 0 || queued > ask {
		t.Errorf("§5's pending line must send a pending (queued) to the window-end ask: %s", line)
	}
}

// shipLineWith is the one line of section containing needle.
func shipLineWith(t *testing.T, section, needle string) string {
	t.Helper()
	for _, line := range strings.Split(section, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Errorf("no line carries %q", needle)
	return ""
}

var shipExitClaim = regexp.MustCompile(`exit (\d+) = ([a-z]+)`)

// TestShipPromptCIExitCodesMatch: the statuses §5 lists for ship ci, and the
// merged and blocked statuses §6 lists for ship merge, are the binary's own.
func TestShipPromptCIExitCodesMatch(t *testing.T) {
	content := shipPromptContent(t)
	ci := shipCIExits()
	gate := promptSection(t, content, "## 5. ci gate")
	for code, word := range ci {
		shipExitLine(t, gate, code, word)
	}
	for _, m := range shipExitClaim.FindAllStringSubmatch(gate, -1) {
		code, _ := strconv.Atoi(m[1])
		if ci[code] != m[2] {
			t.Errorf("§5 says exit %d = %s; dross ship ci exits %d on %q", code, m[2], code, ci[code])
		}
	}

	merge := map[int]string{0: "merged", exitMergeBlocked: "blocked"}
	mgate := promptSection(t, content, "## 6. merge gate")
	for code, word := range merge {
		shipExitLine(t, mgate, code, word)
	}
	for _, m := range shipExitClaim.FindAllStringSubmatch(mgate, -1) {
		code, _ := strconv.Atoi(m[1])
		if ci[code] != m[2] && merge[code] != m[2] {
			t.Errorf("§6 says exit %d = %s, which neither dross ship ci nor dross ship merge exits with", code, m[2])
		}
	}
}

// TestShipPromptWatchesClassicPipelines: an Azure DevOps repo without
// azure-pipelines.yml is still watched, and the skip sentence never covers
// either provider dross ship ci reads.
func TestShipPromptWatchesClassicPipelines(t *testing.T) {
	gate := promptSection(t, shipPromptContent(t), "## 5. ci gate")
	skip := ""
	for _, line := range strings.Split(gate, "\n") {
		if strings.Contains(line, "skip this section only if") {
			skip = line
		}
	}
	if skip == "" {
		t.Fatal("§5 lost its skip sentence")
	}
	never := strings.Index(skip, "on azure-devops and bitbucket never skip this section")
	other := strings.Index(skip, "on every other provider, skip this section only if")
	if never < 0 || other < 0 || other < never {
		t.Errorf("§5's skip sentence must exclude azure-devops and bitbucket before it allows a skip: %s", skip)
	}
	for _, want := range []string{"no azure-pipelines.yml", "still watched"} {
		if !strings.Contains(skip, want) {
			t.Errorf("§5 must say %q", want)
		}
	}
}

// TestShipPromptMergeGate: on Azure DevOps and Bitbucket §6 reads CI again
// right before `dross ship merge`, merges on a pass or on §5's proceed for the
// same head, never merges a fail or a moved head, sends a policy block to
// awaiting-merge, and never completes the phase after a failed merge.
func TestShipPromptMergeGate(t *testing.T) {
	gate := promptSection(t, shipPromptContent(t), "## 6. merge gate")
	arm := shipADOArm(t, gate, "azure devops / bitbucket: a fresh ci read, then dross ship merge")
	ci, merge := strings.Index(arm, "dross ship ci <phase-id> immediately before the merge"), strings.Index(arm, "run dross ship merge <phase-id>")
	if ci < 0 || merge < 0 || merge < ci {
		t.Errorf("§6 must run dross ship ci immediately before dross ship merge (at %d, %d)", ci, merge)
	}
	if line := shipExitLine(t, arm, 0, "pass"); !strings.Contains(line, "→ merge") {
		t.Errorf("§6 must merge on a pass: %s", line)
	}
	// A moved head never merges, whatever the verdict — checked ahead of every
	// verdict, so no proceed rule below can reach it.
	moved := shipLineWith(t, arm, "a head that moved since §5")
	for _, want := range []string{"no merge", "whatever the verdict"} {
		if !strings.Contains(moved, want) {
			t.Errorf("§6's moved-head line must say %q: %s", want, moved)
		}
	}
	if m, p := strings.Index(arm, moved), strings.Index(arm, "exit 0 = pass"); m < 0 || p < 0 || m > p {
		t.Error("§6 must check for a moved head before it reads any verdict")
	}
	// None and ask reach the merge on §5's proceed for the same head — on their
	// own line, so the rule cannot drift to another verdict.
	proceed := shipLineWith(t, arm, fmt.Sprintf("exit %d none or exit %d ask", exitCINone, exitCIAsk))
	for _, want := range []string{"merge only if the user chose proceed in §5 for this same head", "otherwise ask again"} {
		if !strings.Contains(proceed, want) {
			t.Errorf("§6's none/ask line must say %q: %s", want, proceed)
		}
	}
	if strings.Contains(proceed, "pending") {
		t.Errorf("§6 lets a none/ask proceed carry over to a pending: %s", proceed)
	}
	// A running build never merges on a proceed given before it started.
	if pending := shipLineWith(t, arm, fmt.Sprintf("exit %d pending", exitCIPending)); !strings.Contains(pending, "a pending (running) never merges") {
		t.Errorf("§6's pending line must say a pending (running) never merges: %s", pending)
	}
	// A fail never merges.
	if refuse := shipLineWith(t, arm, fmt.Sprintf("exit %d fail", exitCIFail)); !strings.Contains(refuse, "no merge") {
		t.Errorf("§6's fail line must say no merge: %s", refuse)
	}
	if !strings.Contains(arm, "never run dross phase complete") {
		t.Error("§6's Azure DevOps / Bitbucket arm must say a failed merge never runs dross phase complete")
	}
	if line := shipExitLine(t, arm, exitMergeBlocked, "blocked"); !strings.Contains(line, "awaiting-merge") {
		t.Errorf("§6 must send a policy block to awaiting-merge: %s", line)
	}
	if strings.Contains(arm, "curl") {
		t.Error("§6's Azure DevOps / Bitbucket arm hands the model a curl call")
	}
}

var shipMergeTimeout = regexp.MustCompile("`dross ship merge <phase-id>`[^\n]*`timeout: (\\d+)`")

// TestShipPromptMergeTimeout: `dross ship merge` waits for the forge, so §6
// gives its Bash call an explicit timeout above the 2-minute default.
func TestShipPromptMergeTimeout(t *testing.T) {
	m := shipMergeTimeout.FindStringSubmatch(rawShipSection(t, "## 6. Merge gate"))
	if m == nil {
		t.Fatal("§6 runs dross ship merge with no explicit Bash timeout on the same line")
	}
	if ms, _ := strconv.Atoi(m[1]); ms <= 120000 {
		t.Errorf("§6's dross ship merge timeout is %dms — not above the 2-minute default", ms)
	}
}

// TestReadmeDocumentsShipCIMerge: README's command table and the man page name
// both commands.
func TestReadmeDocumentsShipCIMerge(t *testing.T) {
	root := repoRootFromTest(t)
	for file, wants := range map[string][]string{
		"README.md":    {"| `dross ship ci <phase-id>` |", "| `dross ship merge <phase-id>` |"},
		"docs/dross.1": {".B dross ship ci", ".B dross ship merge"},
	} {
		b, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s does not document %q", file, want)
			}
		}
	}
}
