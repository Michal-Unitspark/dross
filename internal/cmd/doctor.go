package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Rivil/dross/internal/architecture"
	"github.com/Rivil/dross/internal/boardsync"
	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/diag"
	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/hooks"
	"github.com/Rivil/dross/internal/localstore"
	"github.com/Rivil/dross/internal/milestone"
	"github.com/Rivil/dross/internal/mutationcfg"
	"github.com/Rivil/dross/internal/phase"
	"github.com/Rivil/dross/internal/pincheck"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/remote"
	"github.com/Rivil/dross/internal/review"
	"github.com/Rivil/dross/internal/state"
	"github.com/Rivil/dross/internal/testlane"
)

// Doctor checks project-level health for the current dross repo.
//
// Distinct from `make doctor` (which checks the dross dev install).
// This runs inside any dross-onboarded project to surface drift between
// what's recorded in project.toml and what's actually true on disk.
//
// Exit code is non-zero on any issue so it can gate CI / pre-push hooks.
func Doctor() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check project-level health (.dross/project.toml vs reality)",
		RunE: func(c *cobra.Command, _ []string) error {
			// LocateRoot, not FindRoot: an incomplete `.dross/` is doctor's
			// diagnosis to make, so it has to reach the foundational-files
			// block below rather than being turned away as not-a-dross-repo.
			root, rootMissing, err := LocateRoot()
			if err != nil {
				return err
			}
			repoDir := filepath.Dir(root) // root is .dross; parent is repo cwd
			issues := 0

			// Cross-field warnings are a second, softer class: each field is
			// individually legal but the *combination* fails at runtime. They
			// are reported and never added to issues — doctor's exit code gates
			// CI and pre-push hooks, and this class exists to stop doctor lying,
			// not to start breaking repos that work today.
			var warnings []string

			// Red-proof warnings are counted rather than collected: they are
			// per-pin lines printed in their own section, not cross-field
			// advisories for the "Combinations:" block. They still reach
			// finalizeDoctor's tally, because a warning nobody counts is a
			// warning nobody sees at the end of a long run.
			redProofWarnings := 0
			duplicateSlugWarnings := 0
			backfillResidueWarnings := 0
			pinWarnings := 0
			protectionWarnings := 0
			hookWarnings := 0

			// --- Foundational files ---
			//
			// project.toml + rules.toml must exist before
			// loadProject can succeed. Surface their absence with a
			// remediation hint — most common cause is a botched recovery
			// after a legacy .dross/-stripping ship.
			Print("Foundational files:")
			missing := checkFoundationalFiles(root)
			if len(missing) > 0 {
				for _, m := range missing {
					Printf("  ✗ %s — missing. If a recent squash-merge wiped .dross/, run `dross ship recover` to restore from the pre-merge HEAD. Otherwise %s.\n", m, RepairHint)
					issues++
				}
				Print("")
				// Two different diagnoses share this block. A missing
				// rules.toml is an ordinary repairable issue — the trio is
				// doctor's own, wider notion of "foundational". A missing
				// project.toml means the root isn't a dross repo
				// at all, which every other command now reports as such, so
				// doctor names it separately rather than collapsing the two.
				if len(rootMissing) > 0 {
					printIncompleteRoot(rootMissing)
					return finalizeIncompleteRoot(rootMissing, issues, len(warnings))
				}
				return finalizeDoctor(issues, len(warnings))
			}
			Print("  ✓ project.toml, rules.toml present")
			Print("")

			p, _, err := loadProject()
			if err != nil {
				return err
			}

			// --- [remote] checks ---
			gitURL, _ := gitrun.Read(repoDir, "remote", "get-url", "origin")

			Print("Remote:")
			switch {
			case gitURL == "" && p.Remote.URL == "":
				Print("  ✓ no git origin and no [remote] configured — fine for greenfield")
			case gitURL == "" && p.Remote.URL != "":
				Printf("  ⚠ project.toml has [remote].url = %s but git has no origin — push to remote, or remove the config\n", p.Remote.URL)
				issues++
			case gitURL != "" && p.Remote.URL == "":
				Printf("  ✗ git origin = %s but project.toml has no [remote] — run /dross-onboard or set manually\n", originForDisplay(gitURL))
				issues++
			default:
				if !sameRemoteURL(gitURL, p.Remote.URL) {
					Printf("  ⚠ git origin (%s) does not match [remote].url (%s)\n", originForDisplay(gitURL), p.Remote.URL)
					issues++
				} else {
					Printf("  ✓ git origin matches [remote].url (%s)\n", p.Remote.URL)
				}
			}

			if p.Remote.AuthEnv != "" {
				if v := os.Getenv(p.Remote.AuthEnv); v == "" {
					Printf("  ✗ [remote].auth_env = %s but $%s is not set in this shell\n", p.Remote.AuthEnv, p.Remote.AuthEnv)
					issues++
				} else {
					Printf("  ✓ $%s is set (length %d)\n", p.Remote.AuthEnv, len(v))
				}
			}

			// auth_scheme selects the credential header. The empty case is the
			// Set's own business — it carries private-token as the code default,
			// so Has("") is true here and false for [board].provider below.
			if !configenum.AuthSchemes.Has(p.Remote.AuthScheme) {
				Printf("  ✗ [remote].auth_scheme = %q is invalid (expected %s)\n", p.Remote.AuthScheme, configenum.AuthSchemes.List())
				issues++
			}

			warnings = append(warnings, diag.RemoteCombination(p.Remote)...)

			Print("")

			// --- [board] checks ---
			//
			// [board] is the single source for issue-board sync, independent
			// of [remote] (a repo can ship to one host and track issues on
			// another). Validate it only when something is configured — board
			// sync is opt-in, so an empty block is fine and stays silent.
			b := p.Board
			if b.Provider != "" || b.BaseURL != "" || b.AuthEnv != "" || b.Project != "" || b.Enabled || b.MilestoneMode != "" || len(b.StateMap) > 0 {
				Print("Board:")
				boardIssues := 0

				// The accept-set is configenum's, not a copy of it: doctor used
				// to reject jira and github boards the CLI dispatches happily,
				// which is the divergence this whole indirection exists to kill.
				// An empty provider stays invalid — BoardProviders has no default.
				if !configenum.BoardProviders.Has(b.Provider) {
					Printf("  ✗ [board].provider = %q is invalid (expected %s)\n", b.Provider, configenum.BoardProviders.List())
					boardIssues++
				}

				if b.AuthEnv == "" {
					Print("  ✗ [board].auth_env is not set (board ops read the token from this env var)")
					boardIssues++
				} else if v := os.Getenv(b.AuthEnv); v == "" {
					Printf("  ✗ [board].auth_env = %s but $%s is not set in this shell\n", b.AuthEnv, b.AuthEnv)
					boardIssues++
				}

				// base_url is optional only where the backend has an address to
				// fall back on (github → https://api.github.com). Optional is
				// not unvalidated: a value that *is* set is still parsed, so a
				// malformed github base_url fails rather than being ignored.
				switch {
				case b.BaseURL == "":
					if configenum.BoardRequiresBaseURL(b.Provider) {
						Printf("  ✗ [board].base_url is not set (the %s backend has no default API address)\n", b.Provider)
						boardIssues++
					}
				case !looksLikeBoardURL(b.BaseURL):
					Printf("  ✗ [board].base_url = %q is not a valid URL (expected http(s)://host)\n", b.BaseURL)
					boardIssues++
				}

				// Empty milestone_mode defaults to version in code; the Set
				// carries that default, and Has normalises exactly as the
				// consumers in forge do.
				if !configenum.MilestoneModes.Has(b.MilestoneMode) {
					Printf("  ✗ [board].milestone_mode = %q is invalid (expected %s)\n", b.MilestoneMode, configenum.MilestoneModes.List())
					boardIssues++
				}

				// A state_map key outside the lifecycle set is a dead override:
				// the lookup is keyed by what dross emits, so the state it was
				// meant to remap never applies. That is silently-broken config,
				// the same severity doctor already gives an invalid provider or
				// milestone_mode — an issue counting toward the non-zero exit,
				// not a warning (locked state_map_key_severity).
				//
				// `project set` refuses to write one, so a key here arrives by
				// hand-editing or predates the planned/planning rename;
				// `dross project set --unset board.state_map.<key>` clears it.
				for _, k := range diag.SortedStateMapKeys(b.StateMap) {
					if !configenum.LifecycleStatuses.Has(k) {
						Printf("  ✗ [board].state_map.%s is not a lifecycle status (expected %s)\n", k, configenum.LifecycleStatuses.List())
						Printf("    Fix: dross project set --unset board.state_map.%s\n", k)
						boardIssues++
					}
				}

				warnings = append(warnings, diag.BoardCombination(b.Provider, b.MilestoneMode, b.AuthUser)...)

				if boardIssues == 0 {
					if b.BaseURL == "" {
						Printf("  ✓ [board] is well-formed (%s)\n", b.Provider)
					} else {
						Printf("  ✓ [board] is well-formed (%s @ %s)\n", b.Provider, b.BaseURL)
					}
				}
				issues += boardIssues
				Print("")
			}

			// --- stranded board mirrors ---
			//
			// Advisory, never an issue: a stranded card is drift on the
			// tracker, not a fault in this repo's configuration, and failing
			// doctor over someone else's board would make the exit status
			// depend on network reachability.
			//
			// This is half of the locked prompt_edge decision. No prompt emits
			// `issue reap` — the forward lifecycle already closes new work, so
			// a sweep at ship would re-walk the whole board for nothing. What
			// keeps the debt visible instead is a detector: doctor reports the
			// count, and the sweep is run by hand when it is non-zero.
			if p.Board.Enabled {
				reportStrandedMirrors()
			}

			// --- .gitattributes ---
			//
			// Without `.dross/** linguist-generated=true`, planning artefacts
			// flood reviewer diffs on every PR. New projects get this for
			// free from `dross init`/`dross onboard`; legacy projects need
			// it added explicitly.
			Print(".gitattributes:")
			if ok, err := drossLinguistAttrPresent(repoDir); err != nil {
				Printf("  ⚠ couldn't read .gitattributes: %v\n", err)
				issues++
			} else if !ok {
				Printf("  ⚠ .dross/ is not marked linguist-generated — PR reviews will see planning noise.\n")
				Printf("    Fix: append `%s` to .gitattributes (or rerun `dross init` to auto-scaffold).\n", drossGitattributesLine)
				issues++
			} else {
				Print("  ✓ .dross/ is marked linguist-generated")
			}
			Print("")

			issues += checkConfigTrust(root, repoDir, p)

			// --- Hooks ---
			hookSec := hooksSection(repoDir)
			printSections([]diag.Section{hookSec})
			issues += diag.Issues([]diag.Section{hookSec})
			for _, l := range hookSec.Lines {
				if l.Level == diag.Warn {
					hookWarnings++
				}
			}

			// --- Reviewer ---
			revSec := reviewerSection(repoDir)
			printSections([]diag.Section{revSec})
			issues += diag.Issues([]diag.Section{revSec})

			// --- Phase work on main ---
			//
			// Phase commits should live on phase/<id> branches, not on
			// main. Legacy projects (or anyone using --no-branch) may
			// have accumulated phase commits on local main; flag them
			// so the user can migrate to the branch model before the
			// next ship.
			mainBranch := p.Repo.GitMainBranch
			if mainBranch == "" {
				mainBranch = "main"
			}
			Print("Phase branch hygiene:")
			leaked, err := phaseCommitsOnMain(root, repoDir, mainBranch)
			switch {
			case err != nil:
				Printf("  ⚠ couldn't check phase commits on %s: %v\n", mainBranch, err)
				// not a hard issue — most likely no origin configured yet
			case len(leaked) > 0:
				Printf("  ⚠ %d phase commit(s) found on local %s ahead of origin/%s:\n",
					len(leaked), mainBranch, mainBranch)
				for _, c := range leaked {
					Printf("      %s  (recorded in phase %s)\n", c.sha[:short7], c.phaseID)
				}
				Print("    Fix: move them to a phase branch, e.g.")
				Printf("      git branch phase/<id> %s && git reset --hard origin/%s\n", mainBranch, mainBranch)
				issues++
			default:
				Printf("  ✓ no recorded phase commits on local %s\n", mainBranch)
			}
			Print("")

			// --- Task statuses ---
			//
			// NextRunnable only ever advances a task whose status is one of
			// pending|in_progress|done|failed (empty meaning pending), so a
			// hand-edited "Done" or "in-progress" drops that task out of the
			// loop in total silence. Report it here rather than in `dross
			// validate`: validate is structural-only and runs in every slash
			// command's wrap step, where a newly-failing enum check would
			// break existing repos (locked status_check_home).
			Print("Task statuses:")
			if bad := taskStatusIssues(root); len(bad) == 0 {
				Print("  ✓ every task status is one of pending|in_progress|done|failed")
			} else {
				for _, b := range bad {
					Printf("  ✗ %s\n", b)
					issues++
				}
				Print("    Fix: `dross task status <phase-id> <task-id> <pending|in_progress|done|failed>`.")
			}
			Print("")

			// --- Red proofs ---
			//
			// A red proof pins the commit its replay was recorded at. Branches
			// get squash-merged and deleted, so a pin that was sound when it
			// was written rots into a SHA only its author's machine can find —
			// which is what happened to the c-5 pin. Pins are discovered by
			// convention (phases/*/changes.json), so a proof recorded by a
			// later phase is checked here with no new code, and a repo with
			// none gets no section at all.
			if lines, present := redProofChecks(root, repoDir); present {
				Print("Red proofs:")
				printLines(lines)
				for _, l := range lines {
					switch l.Level {
					case doctorIssue:
						issues++
					case doctorWarn:
						redProofWarnings++
					}
				}
				Print("")
			}

			// --- Architecture links ---
			//
			// ARCHITECTURE.md's `Symbol — file:line` bullets go stale as code
			// moves. Surface stale links ADVISORILY: this never touches `issues`
			// and never blocks the loop (the doc is best-effort; `dross
			// architecture check --fix` repairs it). A repo with no
			// ARCHITECTURE.md gets no section at all.
			if warnings, present := architectureLinkWarnings(repoDir); present {
				Print("Architecture links:")
				if len(warnings) == 0 {
					Print("  ✓ all ARCHITECTURE.md symbol links resolve")
				} else {
					for _, w := range warnings {
						Printf("  ⚠ %s\n", w)
					}
					Print("    Advisory only. Fix: `dross architecture check --fix` (or /dross-architecture to refresh).")
				}
				Print("")
			}

			// --- Interaction coverage ---
			//
			// Fail-closed classification of every command-backed prompt:
			// interactive ones (AskUserQuestion shim) must have a `### dross-<name>`
			// audit section; non-interactive ones must be enrolled in the doc's
			// `## Exempt` list. Reuses the same classifier the Go-test gate
			// (interaction_coverage_test.go) runs — that test is the enforcing
			// gate; this surfaces the same verdict on demand. It fires only in the
			// dross source tree (docs/interaction-audit.md is not embedded) and
			// stays silent in other onboarded projects.
			if warnings, present := interactionCoverageWarnings(repoDir); present {
				Print("Interaction coverage:")
				if len(warnings) == 0 {
					Print("  ✓ every command-backed prompt is sectioned or exempt")
				} else {
					for _, w := range warnings {
						Printf("  ✗ %s\n", w)
						issues++
					}
					Print("    Fix: add a `### dross-<name>` audit section (interactive) or an `## Exempt` entry (non-interactive) in docs/interaction-audit.md.")
				}
				Print("")
			}

			// --- state.json tracking ---
			//
			// The file is machine-local (locked state_tracking). A repo that
			// still has it in the index is one squash-merge away from a
			// checkout replacing the live copy — the incident this milestone
			// closed. The fix is one command, so print the command.
			Print("State file:")
			if gitrun.Quiet(repoDir, "ls-files", "--error-unmatch", "--", RootDirName+"/"+state.File) == nil {
				Printf("  ✗ %s/%s is tracked — a branch carrying a stale copy can replace the live one. Fix: `git rm --cached %s/%s` (it is already gitignored)\n",
					RootDirName, state.File, RootDirName, state.File)
				issues++
			} else {
				Printf("  ✓ %s/%s is not tracked\n", RootDirName, state.File)
			}
			Print("")

			// --- Clobbered / missing tracked .dross/ files ---
			//
			// Read-only reuse of `dross repair`'s own detectors (t-1, t-3): a
			// working-tree edit that never got committed, or a checkout that
			// wiped a phase dir origin still knows about, both leave this
			// project-level check silently wrong until someone notices by
			// hand. Fixing is `dross repair`'s job, not doctor's — this only
			// surfaces the finding.
			Print("Clobbered/missing .dross/ files:")
			clobbered, clobberErr := detectModifiedOrMissingTracked(repoDir)
			missingDirs, missingDirsErr := detectMissingPhaseDirs(repoDir, root, mainBranch)
			switch {
			case clobberErr != nil:
				Printf("  ⚠ couldn't scan tracked .dross/ files: %v\n", clobberErr)
			case missingDirsErr != nil:
				Printf("  ⚠ couldn't scan for missing phase dirs: %v\n", missingDirsErr)
			case len(clobbered) == 0 && len(missingDirs) == 0:
				Print("  ✓ no clobbered or missing tracked .dross/ files")
			default:
				for _, f := range clobbered {
					if f.Missing {
						Printf("  ✗ %s — missing\n", f.Path)
					} else {
						Printf("  ✗ %s — diverged from HEAD\n", f.Path)
					}
					issues++
				}
				for _, id := range missingDirs {
					Printf("  ✗ %s/phases/%s — phase dir known to origin but absent from working tree\n", RootDirName, id)
					issues++
				}
				Print("    Fix: `dross repair` (add --apply to write the restores).")
			}
			Print("")

			// --- Version parity ---
			//
			// project.toml's [project].version is what release.yml tags from
			// and state.json's is what dross bumps; writeVersion writes both,
			// so a difference means something wrote one behind the other's back.
			// Skipped where there is no state.json — a fresh clone has none,
			// and comparing against a value seeded FROM project.toml would be
			// tautological anyway.
			Print("Version:")
			switch {
			case p.Project.Version == "":
				Printf("  ✗ .dross/project.toml has no [project].version — release.yml resolves the release tag from it. Fix: `dross project set project.version <major.minor.patch.internal>`\n")
				issues++
			default:
				if st, err := state.Load(filepath.Join(root, state.File)); err != nil {
					Printf("  ✓ [project].version = %s (no state.json to compare — fresh clone)\n", p.Project.Version)
				} else if st.Version != p.Project.Version {
					Printf("  ✗ version drift: project.toml = %s, state.json = %s. Fix: `dross state set version <value>` writes both\n",
						p.Project.Version, st.Version)
					issues++
				} else {
					Printf("  ✓ project.toml and state.json agree on %s\n", p.Project.Version)
				}
			}
			Print("")

			// --- Stale milestone branches ---
			//
			// Read-only, always: doctor names them and `dross milestone prune`
			// deletes them (locked prune_surface). Deleting a remote branch as
			// a diagnostic side effect is exactly what that decision rules out.
			mainForStale := p.Repo.GitMainBranch
			if mainForStale == "" {
				mainForStale = "main"
			}
			if stale, err := staleMilestoneBranches(root, repoDir, mainForStale); err == nil && len(stale) > 0 {
				Print("Stale milestone branches:")
				for _, b := range stale {
					where := "local"
					if b.HasRemote {
						where = "local + origin"
					}
					Printf("  ✗ %s (%s, %s) — its work is already on %s. Fix: `dross milestone prune`\n",
						b.Name, b.Reason, where, mainForStale)
					issues++
				}
				Print("")
			}

			// --- Pin currency ---
			//
			// The pins Dependabot cannot reach — run-block `go install` pins,
			// setup-node's version, goreleaser-action's version, go.mod's
			// toolchain — checked against their upstreams by the same checker
			// and staleness rules as the dross repo's weekly cron (locked
			// decision check_surface), in lenient mode (doctor_net): a stale or
			// unpinned pin is a warning naming what to do, an unreachable
			// upstream or a newer major is a line. None of it moves the exit
			// code — /dross-ship and /dross-review gate on doctor, and upstream's
			// release calendar must not block them. Silent when the repo holds
			// no such pin.
			if sec, _, err := PinCurrencySection(context.Background(), repoDir, pinResolver, time.Now()); err != nil {
				Print("Pin currency:")
				Printf("  ⚠ could not scan the repo's pins: %v\n", err)
				Print("")
				pinWarnings++
			} else if len(sec.Lines) > 0 {
				Print(sec.Heading)
				printLines(sec.Lines)
				Print("    Advisory only — bump a stale pin to the version named; this never changes doctor's exit code.")
				Print("")
				for _, l := range sec.Lines {
					if l.Level == doctorWarn {
						pinWarnings++
					}
				}
			}

			// --- Branch protection ---
			//
			// main's live rules on GitHub against origin/<main>'s pull_request
			// jobs (protectionSection). Gaps are warnings naming `dross
			// protect`; none moves the exit code — /dross-ship gates on doctor.
			// Omitted with no [remote], like the other remote-only sections.
			if sec, present := protectionSection(root, repoDir, p); present {
				Print(sec.Heading)
				printLines(sec.Lines)
				Print("    Advisory only — this never changes doctor's exit code.")
				Print("")
				for _, l := range sec.Lines {
					if l.Level == doctorWarn {
						protectionWarnings++
					}
				}
			}

			// --- Duplicate roadmap slugs ---
			//
			// Carrying a phase forward onto a later milestone's roadmap is a
			// legitimate re-scope, so `dross phase list` dedups it silently
			// (phase.Ordered) rather than printing the directory twice. That
			// makes the ambiguity invisible at the listing, which is why it is
			// named here instead: doctor is where faults belong. Advisory —
			// nothing is broken, so the exit code is unchanged.
			if dups := diag.RoadmapDuplicates(root); len(dups) > 0 {
				Print("Duplicate roadmap slugs:")
				for _, d := range dups {
					Printf("  ⚠ %s is on %d milestone roadmaps (%s) — listed once, at its %s position\n",
						d.Slug, len(d.Versions), strings.Join(d.Versions, ", "), d.Versions[0])
					duplicateSlugWarnings++
				}
				Print("    Advisory only — a re-scoped phase is legitimate; this never changes doctor's exit code.")
				Print("")
			}

			// --- Unbackfillable roadmap phases ---
			//
			// c-5: a phase a milestone signed up for, carrying no completion
			// marker, that `dross phase backfill` cannot close from evidence.
			// Doneness reads changes.json alone now (internal/phase/done.go), so such a
			// phase counts not-done forever with nothing saying why — it is
			// indistinguishable at every surface from a phase that was never
			// started. Named here rather than in the sweep's output, following
			// the duplicate-slug precedent that doctor is where faults belong
			// and that output which scrolls away is not standing visibility.
			//
			// Scoped to the milestones' phases arrays by the locked
			// backfill_residue decision: a phase directory on no roadmap (the
			// deliberate v14-mutation-pass scratch dir) would otherwise nag
			// forever, and no accept-mechanism has to be invented to silence
			// it. Advisory — nothing is broken, so the exit code is unchanged.
			if residue := backfillResidue(root, repoDir, mainForStale); len(residue) > 0 {
				Print("Unbackfillable roadmap phases:")
				for _, r := range residue {
					Printf("  ⚠ %s — %s. Fix: finish it, or close it by hand once it ships\n", r.Slug, r.Reason)
					backfillResidueWarnings++
				}
				Print("    Advisory only — these never change doctor's exit code.")
				Print("")
			}

			// --- Cross-field combinations ---
			//
			// Collected above, reported here as one advisory block. Each value
			// involved is individually valid, so none of these touch `issues`;
			// what they catch is a pairing that only fails once a command runs.
			if len(warnings) > 0 {
				Print("Combinations:")
				for _, w := range warnings {
					Printf("  ⚠ %s\n", w)
				}
				Print("    Advisory only — these never change doctor's exit code.")
				Print("")
			}

			return finalizeDoctor(issues, len(warnings)+redProofWarnings+duplicateSlugWarnings+backfillResidueWarnings+pinWarnings+protectionWarnings+hookWarnings)
		},
	}
}

// pinResolver is the seam doctor's Pin currency section reads upstream
// through. Typed as pincheck's Resolver so internal/cmd never imports net/http
// itself (boundary_test.go). The test binary's TestMain swaps in a counting
// resolver that never dials and fails the binary if anything reached it, so a
// doctor test can only touch upstream through a stub it installs on purpose.
var pinResolver pincheck.Resolver = pincheck.NewResolver(pincheck.DefaultEndpoints(), pinHostTimeout)

// pinHostTimeout bounds each upstream request doctor makes. Short on purpose:
// doctor runs in every dross-managed repo and must stay usable offline, where
// an unreachable host is a line, not a wait (locked decision doctor_net).
const pinHostTimeout = 3 * time.Second

// PinCurrencySection builds doctor's "Pin currency" block over the generic
// pin sites under repoDir — the subset every dross-managed repo can carry; the
// Go-source pins only the dross repo holds are the weekly cron's alone. It runs
// pincheck.Check in lenient mode, so the lines' levels come from the one
// checker the cron uses: stale and unpinned warn, unknown and info are notes,
// and nothing is ever an Issue. The Section is empty when the repo holds no
// such pin. The results the lines were rendered from come back beside it.
//
// Exported so cmd/pincheck's parity test can drive this path beside the cron's
// and prove both classify a pin the same way.
func PinCurrencySection(ctx context.Context, repoDir string, r pincheck.Resolver, now time.Time) (diag.Section, []pincheck.Result, error) {
	sites, err := pincheck.Scan(repoDir)
	if err != nil {
		return diag.Section{}, nil, err
	}
	sec := diag.Section{Heading: "Pin currency:"}
	results := pincheck.Check(ctx, sites, r, pincheck.Lenient, now).Results
	for _, res := range results {
		sec.Lines = append(sec.Lines, pinCurrencyLine(res))
	}
	return sec, results, nil
}

// pinCurrencyLine renders one lenient result. The level follows the checker's
// severity; the text names the pin and what its verdict points at.
func pinCurrencyLine(res pincheck.Result) doctorLine {
	where := fmt.Sprintf("%s:%d %s", res.File, res.Line, res.Name)
	var text string
	switch res.Verdict {
	case pincheck.Current:
		text = fmt.Sprintf("%s %s is current", where, res.Version)
	case pincheck.Stale:
		text = fmt.Sprintf("%s %s is stale — bump to %s (a newer release on its line has been out more than 7 days)", where, res.Version, res.Target)
	case pincheck.Unpinned:
		text = fmt.Sprintf("%s is not pinned to an exact release: %s", where, res.Classification.Reason)
	case pincheck.Info:
		text = fmt.Sprintf("    · %s %s — a newer release exists off its line (%s): a migration, not a bump", where, res.Version, res.Latest)
	default:
		text = fmt.Sprintf("    · %s %s — could not check upstream: %s", where, res.Version, res.Classification.Reason)
	}
	switch res.Severity {
	case pincheck.SeverityWarn:
		return doctorLine{Level: doctorWarn, Text: text}
	case pincheck.SeverityFail:
		return doctorLine{Level: doctorIssue, Text: text}
	case pincheck.SeverityLine:
		return doctorLine{Level: diag.Note, Text: text}
	}
	return doctorLine{Level: doctorOK, Text: text}
}

// doctorLine and the three levels are the in-package names for diag.Line:
// every check returns lines, and Doctor() decides what each level costs.
type doctorLine = diag.Line

const (
	doctorOK    = diag.OK
	doctorWarn  = diag.Warn
	doctorIssue = diag.Issue
)

// printLines renders check lines with doctor's glyphs: ✓ / ⚠ / ✗ under a
// two-space indent, and a Note verbatim — it carries its own indentation.
func printLines(lines []doctorLine) {
	for _, l := range lines {
		switch l.Level {
		case diag.Issue:
			Printf("  ✗ %s\n", l.Text)
		case diag.Warn:
			Printf("  ⚠ %s\n", l.Text)
		case diag.Note:
			Print(l.Text)
		default:
			Printf("  ✓ %s\n", l.Text)
		}
	}
}

// printSections renders headed blocks, each followed by a blank line.
func printSections(sections []diag.Section) {
	for _, sec := range sections {
		Print(sec.Heading)
		printLines(sec.Lines)
		Print("")
	}
}

// redProofChecks classifies every discovered red-proof pin and hands the
// verdicts to diag.RedProof. present is false when the repo records none, so
// projects without red proofs get no section. The git-backed classification
// (reachability, the doc's own SHA, the repoint hint) is read HERE and passed
// in: diag renders verdicts and spawns nothing.
func redProofChecks(root, repoDir string) ([]doctorLine, bool) {
	pins, err := discoverRedProofPins(root, repoDir)
	if err != nil {
		return diag.RedProof(nil, err)
	}
	in := make([]diag.RedProofPin, 0, len(pins))
	for _, pin := range pins {
		in = append(in, classifyRedProofPin(root, repoDir, pin))
	}
	return diag.RedProof(in, nil)
}

// redProofPinLines is the per-pin verdict, classified here and rendered by
// diag.PinLines.
func redProofPinLines(root, repoDir string, pin redProofPin) []doctorLine {
	return diag.PinLines(classifyRedProofPin(root, repoDir, pin))
}

// classifyRedProofPin reads everything diag needs to judge one pin: whether
// origin reaches the SHA, what the replay doc says its base commit is, and
// the repair to name if the pin is unreachable.
func classifyRedProofPin(root, repoDir string, pin redProofPin) diag.RedProofPin {
	out := diag.RedProofPin{Phase: pin.Phase, SHA: pin.SHA, Doc: pin.Doc.Rel()}
	verdict, why, err := classifyReachability(repoDir, pin.SHA)
	out.Reach, out.Why, out.ReachErr = diag.Reach(verdict), why, err
	if err != nil {
		return out
	}
	// The doc cross-check runs whatever the verdict: it is a separate claim
	// about a separate artefact, and a shallow clone can still read a file.
	out.DocSHA, out.DocErr = redProofDocSHA(pin.Doc)
	if verdict == reachUnreachable {
		out.RepointHint = redProofRepointHint(root, repoDir, pin)
	}
	return out
}

// redProofRepointHint names the repair: the `red-proof repoint` verb, which
// resolves the owning phase's fork point itself.
//
// It used to spell out a `red-proof set --sha <fork> --doc <doc>` line, which
// asked the operator to copy a SHA doctor had already computed and left the
// replay doc — carrying the same SHA in three places — for them to edit by
// hand. Naming the verb instead is the locked repoint_surface decision: doctor
// stays diagnostic and the repair lives in one typed command.
//
// The fork point is still resolved here, but only to say whether a repair is
// available at all: when it cannot be resolved the hint names the phase and NO
// command, because a copy-pasteable line with a blank SHA is worse than none.
func redProofRepointHint(root, repoDir string, pin redProofPin) string {
	fork, err := phaseForkPoint(repoDir, root, pin.Phase)
	if err != nil {
		return fmt.Sprintf("repoint it to a commit origin reaches (%s's fork point could not be resolved: %v)", pin.Phase, err)
	}
	// The fork point is still named — a diagnostic that says where a thing
	// would go is more useful than one that only says a verb exists — but it
	// is reported, not handed over as an argument to retype.
	return fmt.Sprintf("repoint it to %s's fork point %s — `dross phase red-proof repoint %s --apply`",
		pin.Phase, fork, pin.Phase)
}

// architectureLinkWarnings resolves every symbol link in ARCHITECTURE.md against
// the live code (paths pinned to repoDir) and returns one advisory warning per
// Moved/Unresolved bullet. present=false means there is no ARCHITECTURE.md — the
// caller emits no section and no error. Ambiguous and Skipped links never warn:
// a duplicate name or a language codex can't index is not "stale".
func architectureLinkWarnings(repoDir string) (warnings []string, present bool) {
	body, err := os.ReadFile(filepath.Join(repoDir, architecture.File))
	if err != nil {
		return nil, false // absent (or unreadable) → no section
	}
	for _, r := range architecture.ResolveAllIn(string(body), repoDir) {
		switch r.Status {
		case architecture.StatusMoved:
			warnings = append(warnings, fmt.Sprintf("%s — %s:%d moved to line %d", r.Link.Symbol, r.Link.File, r.Link.Line, r.NewLine))
		case architecture.StatusUnresolved:
			warnings = append(warnings, fmt.Sprintf("%s — %s:%d no longer resolves", r.Link.Symbol, r.Link.File, r.Link.Line))
		}
	}
	return warnings, true
}

// interactionCoverageWarnings runs the interaction-contract coverage classifier
// — the same one the Go-test gate uses — against the dross source tree and
// returns one warning per unclassified command-backed prompt. present=false means
// repoDir is not the dross source tree: docs/interaction-audit.md is absent (it is
// not embedded, so the classifier has nothing to read in other onboarded
// projects), and the caller emits no section. The Go test is the enforcing gate;
// this lint only surfaces the same classification on demand inside the dross repo.
func interactionCoverageWarnings(repoDir string) (warnings []string, present bool) {
	if _, err := os.Stat(filepath.Join(repoDir, "docs", "interaction-audit.md")); err != nil {
		return nil, false // not the dross source tree → no section
	}
	res, err := interactionCoverage(repoDir)
	if err != nil {
		return nil, false // partial/malformed tree → skip silently
	}
	for _, gap := range res.Uncovered {
		warnings = append(warnings, fmt.Sprintf("%s — %s", gap.Name, gap.Reason))
	}
	return warnings, true
}

// taskStatusIssues walks every phase's plan.toml and returns one line per task
// whose status is outside the runnable set, each naming both the phase and the
// task so the offender is addressable without opening the file.
//
// A phase directory with no plan.toml is skipped silently — a spec'd but
// unplanned phase is normal. An unparseable plan.toml is its own issue: it
// would otherwise be swallowed into a clean ✓, which is the same silence the
// check exists to remove.
func taskStatusIssues(root string) []string {
	ids, err := phase.List(root)
	if err != nil {
		return []string{fmt.Sprintf("couldn't list phases: %v", err)}
	}
	var out []string
	for _, id := range ids {
		planPath := filepath.Join(phase.Dir(root, id), "plan.toml")
		if _, err := os.Stat(planPath); err != nil {
			continue
		}
		plan, err := phase.LoadPlan(planPath)
		if err != nil {
			out = append(out, fmt.Sprintf("phase %s — plan.toml is unreadable: %v", id, err))
			continue
		}
		for _, t := range plan.Task {
			switch t.Status {
			case "", phase.StatusPending, phase.StatusInProgress, phase.StatusDone, phase.StatusFailed:
			default:
				out = append(out, fmt.Sprintf("phase %s, task %s — unrecognised status %q (want pending|in_progress|done|failed); NextRunnable skips it silently", id, t.ID, t.Status))
			}
		}
	}
	return out
}

// looksLikeBoardURL reports whether s is a plausible instance base URL: an
// absolute http(s) URL with a host. Shape-only — it doesn't dial the host.
func looksLikeBoardURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// finalizeDoctor records the doctor outcome event and returns the
// appropriate error (or nil) for the issue count. Shared between the
// foundational-files short-circuit path and the full-check path so the
// telemetry shape stays consistent.
//
// warnings is reported alongside issues and deliberately never folded into it:
// the exit code answers "is anything invalid", not "is anything suspicious".
func finalizeDoctor(issues, warnings int) error {
	result := "passed"
	if issues > 0 {
		result = "issues_found"
	}
	RecordOutcomeEvent("doctor",
		map[string]int{"issues": issues, "warnings": warnings},
		nil,
		map[string]string{"result": result},
	)
	if issues == 0 {
		Print("All project-level checks passed.")
		return nil
	}
	return fmt.Errorf("%d project-level issue(s) found", issues)
}

// incompleteRootHeading opens doctor's incomplete-root block. The block runs
// from this line to the next blank line and lists exactly LocateRoot's Missing
// slice — never doctor's wider foundational trio, which also covers rules.toml.
const incompleteRootHeading = "Not a dross repo — .dross/ is incomplete:"

func printIncompleteRoot(rootMissing []string) {
	Print(incompleteRootHeading)
	for _, m := range rootMissing {
		Printf("  ✗ %s — missing\n", m)
	}
	Printf("  → %s\n", RepairHint)
	Print("")
}

// finalizeIncompleteRoot is the incomplete-root verdict — deliberately not
// finalizeDoctor's. A repo missing only rules.toml is repairable in place; one
// missing project.toml or state.json is not a dross repo, and the two must not
// read as the same outcome to a human or to telemetry.
func finalizeIncompleteRoot(rootMissing []string, issues, warnings int) error {
	RecordOutcomeEvent("doctor",
		map[string]int{"issues": issues, "warnings": warnings},
		nil,
		map[string]string{"result": "incomplete_root"},
	)
	return fmt.Errorf("not a dross repo — .dross/ is incomplete, missing %s (%d project-level issue(s) found)",
		strings.Join(rootMissing, ", "), issues)
}

// checkFoundationalFiles returns the list of missing foundational files
// (relative paths) that loadProject would otherwise crash on. Empty
// slice means the pair is intact.
//
// state.json is deliberately not in the set: it is machine-local and gitignored
// (locked state_tracking), so a fresh clone legitimately has none, and root
// resolution materializes one on demand. Reporting its absence would make every
// clone read as broken.
func checkFoundationalFiles(root string) []string {
	var missing []string
	for _, rel := range []string{"project.toml", "rules.toml"} {
		if _, err := os.Stat(filepath.Join(root, rel)); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, ".dross/"+rel)
		}
	}
	return missing
}

// leakedPhaseCommit pairs a phase commit SHA with the phase id whose
// changes.json recorded it. Returned by phaseCommitsOnMain.
type leakedPhaseCommit struct {
	sha     string
	phaseID string
}

// short7 caps the SHA preview in doctor output. 7 chars is enough to
// disambiguate in any realistic repo.
const short7 = 7

// phaseCommitsOnMain returns the commits between origin/<mainBranch>
// and local <mainBranch> that match any recorded task commit in
// .dross/phases/*/changes.json. An empty result means main is clean
// — either it's at origin or its ahead-commits aren't phase work.
//
// Returns an error if origin/<mainBranch> isn't reachable (no origin
// configured, never pushed). Caller should treat that as a soft skip
// rather than an issue: nothing to leak if there's no upstream.
func phaseCommitsOnMain(root, repoDir, mainBranch string) ([]leakedPhaseCommit, error) {
	// Collect all recorded phase commit SHAs.
	phasesDir := filepath.Join(root, "phases")
	entries, err := os.ReadDir(phasesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	recorded := make(map[string]string) // sha → phaseID
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		phaseID := e.Name()
		body, err := os.ReadFile(filepath.Join(phasesDir, phaseID, "changes.json"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read changes.json for %s: %w", phaseID, err)
		}
		for _, sha := range extractCommitSHAs(string(body)) {
			recorded[sha] = phaseID
		}
	}
	if len(recorded) == 0 {
		return nil, nil
	}

	// List commits on local main not in origin/main.
	out, err := gitrun.Trim(repoDir, gitRefArgs("rev-list", nil, "origin/"+mainBranch+".."+mainBranch)...)
	if err != nil {
		return nil, err
	}
	var leaked []leakedPhaseCommit
	for _, sha := range strings.Fields(out) {
		if pid, ok := recorded[sha]; ok {
			leaked = append(leaked, leakedPhaseCommit{sha: sha, phaseID: pid})
		}
	}
	return leaked, nil
}

// extractCommitSHAs pulls all `"commit": "<sha>"` values out of a
// changes.json body. Cheap regex-style scan rather than a full JSON
// parse — keeps doctor from coupling to the changes/ package shape.
func extractCommitSHAs(body string) []string {
	var out []string
	const key = `"commit":`
	for i := 0; i < len(body); {
		j := strings.Index(body[i:], key)
		if j < 0 {
			break
		}
		j += i + len(key)
		// Skip whitespace and the opening quote.
		for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '"') {
			j++
		}
		k := j
		for k < len(body) && body[k] != '"' {
			k++
		}
		if k > j {
			out = append(out, body[j:k])
		}
		i = k
	}
	return out
}

// drossLinguistAttrPresent returns whether .gitattributes covers .dross/
// with linguist-generated=true. Missing file → not present. Read error
// (permissions etc.) → propagated.
func drossLinguistAttrPresent(repoDir string) (bool, error) {
	body, err := os.ReadFile(filepath.Join(repoDir, ".gitattributes"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return hasDrossLinguistLine(string(body)), nil
}

// sameRemoteURL compares two git remote forms loosely — strips trailing
// .git and compares hostname + path. Avoids false-positives from "the
// same remote in https vs ssh form".
//
// An Azure Repos remote has more spellings than that — ssh v3 drops _git, a
// Server's ssh url carries the daemon's port, names are case-insensitive — so
// when either side parses as one, both are compared by their canonical url,
// host kept. A side that parses against one that does not is a mismatch: a
// url without _git names no repo, however close it reads.
func sameRemoteURL(a, b string) bool {
	adoA, okA := project.ParseAzureDevOpsRemote(a)
	adoB, okB := project.ParseAzureDevOpsRemote(b)
	if okA || okB {
		return okA && okB && strings.EqualFold(adoA.URL, adoB.URL)
	}
	hA, pA := parseGitForCompare(a)
	hB, pB := parseGitForCompare(b)
	return hA == hB && pA == pB
}

// originForDisplay cuts the user[:password]@ off a URL-form git origin before
// doctor prints it: an https origin often carries a PAT there, and doctor's
// output lands in terminals, CI logs and bug reports. The cut runs to the
// authority's last @, so a password holding an @ goes with it. An scp-form
// origin (git@host:path) is printed as-is — its user is an ssh login name.
func originForDisplay(raw string) string {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(raw), "://")
	if !ok {
		return raw
	}
	authority, tail, hasTail := strings.Cut(rest, "/")
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	out := scheme + "://" + authority
	if hasTail {
		out += "/" + tail
	}
	return out
}

func parseGitForCompare(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	// Reuse parseGitRemote semantics by lifting from the project pkg
	// indirectly: short-and-dumb host:path extractor here keeps the
	// dependency direction right (cmd → project, never project → cmd).
	if !strings.Contains(raw, "://") && strings.Contains(raw, "@") && strings.Contains(raw, ":") {
		afterAt := raw[strings.Index(raw, "@")+1:]
		colon := strings.Index(afterAt, ":")
		if colon < 0 {
			return "", ""
		}
		return afterAt[:colon], afterAt[colon+1:]
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return rest, ""
		}
		return rest[:slash], rest[slash+1:]
	}
	return "", ""
}

// --- config-trust checks (c-6, c-7) ---

// gitVersionOutput is the seam TestDoctorReportsOldGit stubs. The version floor
// cannot be exercised any other way: CI's git is new enough, so without a seam
// the check would only ever be observed passing.
var gitVersionOutput = func() (string, error) {
	return gitrun.Trim(".", "--version")
}

// checkConfigTrust gathers what diag.ConfigTrust needs from this machine —
// the host allowlist, the git version, the consent store, the gitignore
// matcher, the ref validator and the gated-command roster — prints the
// sections it returns, then the remote-mutation and local-toolchain checks
// beneath them. It returns the number of issues found.
func checkConfigTrust(root, repoDir string, p *project.Project) int {
	extra, hostErr := localstore.ReadAllowHosts(root, repoDir)
	sections, issues := diag.ConfigTrust(root, repoDir, p, diag.TrustInputs{
		AllowHosts:      extra,
		AllowHostsErr:   hostErr,
		GitVersion:      gitVersionOutput,
		Grants:          localstore.GrantStore(root),
		IgnoresPath:     ignoresPath,
		LocalIgnorePath: drossLocalIgnorePath,
		ValidateRef:     validateGitRef,
		GatedCommands:   execGatedCommands,
	})
	printSections(sections)

	issues += checkRemoteMutation(root, repoDir, p)
	checkMutationToolchain(p)
	return issues
}

// projectSettingsFiles are the repo-level settings files Claude Code layers
// over the user-level one, relative to the repo root.
var projectSettingsFiles = []string{".claude/settings.json", ".claude/settings.local.json"}

// hooksSection reports the dross hooks in the user-level settings.json Claude
// Code reads (CLAUDE_CONFIG_DIR honoured) — every hook ensureUserHooks wires,
// in its order. A missing gate hook is an issue — the gates are off — and so
// is a gate command wired only under a matcher (it fires for some tools and
// not others), "disableAllHooks": true, or a file that cannot be read. The
// same key in repoDir's own .claude/settings.json or settings.local.json
// turns every hook off in this repo however the user-level file is wired, so
// it is an issue too, as is either file failing to parse. A missing
// PreCompact or SessionStart hook is a warning: dross works without them, it
// only loses pause and re-entry.
func hooksSection(repoDir string) diag.Section {
	sec := diag.Section{Heading: "Hooks:"}
	add := func(level diag.Level, format string, a ...any) {
		sec.Lines = append(sec.Lines, diag.Line{Level: level, Text: fmt.Sprintf(format, a...)})
	}
	path, err := userSettingsPath()
	if err != nil {
		add(diag.Issue, "cannot locate the user-level settings.json: %v", err)
		return sec
	}
	doc, err := hooks.ReadSettings(path)
	if err != nil {
		add(diag.Issue, "the user-level settings.json cannot be read (%v) — Claude Code runs none of its hooks until it is fixed", err)
		return sec
	}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		add(diag.Issue, "the user-level settings.json cannot be read: %v", err)
		return sec
	}
	if off, _ := doc["disableAllHooks"].(bool); off {
		add(diag.Issue, "settings.json sets \"disableAllHooks\": true — Claude Code runs no hooks, so every dross gate is off. Fix: remove the key")
	}
	for _, h := range userHooks {
		// The gate pair is the tool-call gates: without it every gate is off.
		gate := h.command == GateCheckHook || h.command == GateRecordHook
		merged, err := hooks.MergeHook(b, h.event, h.command)
		if err != nil {
			add(diag.Issue, "%s → %s cannot be checked: %v", h.event, h.command, err)
			continue
		}
		present := len(b) > 0 && bytes.Equal(merged, b)
		switch {
		case !present && gate:
			add(diag.Issue, "%s → `%s` is not wired, so the tool-call gates are off. Fix: `dross hooks ensure`", h.event, h.command)
		case !present:
			add(diag.Warn, "%s → `%s` is not wired. Fix: `dross hooks ensure`", h.event, h.command)
		case gate && !matcherless(doc, h.event, h.command):
			add(diag.Issue, "%s → `%s` is wired only under a matcher, so it fires for some tools and not others. Fix: remove the matcher, or move the command into a matcher-less group (`dross hooks ensure` leaves a file that already names it alone)", h.event, h.command)
		default:
			add(diag.OK, "%s → %s", h.event, h.command)
		}
	}
	for _, rel := range projectSettingsFiles {
		pdoc, err := hooks.ReadSettings(filepath.Join(repoDir, filepath.FromSlash(rel)))
		if err != nil {
			add(diag.Issue, "%s cannot be read (%v), so whether it turns this repo's hooks off cannot be told. Fix: repair or remove it", rel, err)
			continue
		}
		if off, _ := pdoc["disableAllHooks"].(bool); off {
			add(diag.Issue, "%s sets \"disableAllHooks\": true — Claude Code runs no hooks in this repo, so every dross gate is off here though the user-level file wires them. Fix: remove the key", rel)
		}
	}
	return sec
}

// reviewerSection reports the solo task reviewer definition: installed where
// Claude Code reads user agents and byte-identical to the one this dross
// ships. Missing, stale (a linked source edited past the binary included) or
// shadowed by a repo-level definition is an issue — every solo begin refuses
// in that state — naming the path and the fix.
func reviewerSection(repoDir string) diag.Section {
	sec := diag.Section{Heading: "Reviewer:"}
	home, err := os.UserHomeDir()
	if err != nil {
		sec.Lines = append(sec.Lines, diag.Line{Level: diag.Issue, Text: fmt.Sprintf("cannot resolve home to find the solo reviewer: %v", err)})
		return sec
	}
	r := reviewerStatusIn(userAgentsDir(home), repoDir)
	if r.State == reviewerOK {
		sec.Lines = append(sec.Lines, diag.Line{Level: diag.OK, Text: fmt.Sprintf("solo reviewer %s is installed and current", review.ReviewerAgent)})
		return sec
	}
	sec.Lines = append(sec.Lines, diag.Line{Level: diag.Issue, Text: fmt.Sprintf("%s — solo runs refuse to begin. Fix: %s", r.Problem(), r.Remedy())})
	return sec
}

// matcherless reports whether command runs from a group under hooks.<event>
// that matches every tool: no matcher, an empty one, or "*".
func matcherless(doc map[string]any, event, command string) bool {
	hk, _ := doc["hooks"].(map[string]any)
	groups, _ := hk[event].([]any)
	for _, g := range groups {
		group, _ := g.(map[string]any)
		if m, ok := group["matcher"].(string); ok && m != "" && m != "*" {
			continue
		}
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			if entry, _ := e.(map[string]any); entry["command"] == command {
				return true
			}
		}
	}
	return false
}

// checkMutationToolchain prints the local toolchain gaps diag reports, as a
// section only when there is one — no gaps, no heading.
func checkMutationToolchain(p *project.Project) {
	lines := diag.MutationToolchain(p, execLookPath)
	if len(lines) == 0 {
		return
	}
	Print("Mutation toolchain:")
	printLines(lines)
	Print("")
}

// execLookPath is the PATH lookup seam, so a test can drive both arms without
// depending on what the developer happens to have installed. It is bound to
// mutationcfg's seam so doctor and the adapter layer resolve through the same
// lookup.
var execLookPath = mutationcfg.LookPath

// remoteProbeFn is the readiness seam.
//
// It is a package-level var so that doctor and the verify wiring probe through
// the SAME function. A doctor that performed its own slightly different check
// would be the worst kind of green: it would pass on a host the run then fails
// on, which is exactly the mid-run discovery c-5 exists to prevent.
var remoteProbeFn = remote.Probe

// remoteProbeTools is everything doctor asks the host about, in ONE probe: the
// mutation adapters' tools first, then every declared lane's toolchain.
//
// One probe rather than two is c-8's "never disagree" clause taken literally.
// A second question asked separately is a second answer that can differ from
// the first — and the failure it produces is the one doctor exists to prevent:
// doctor passing on a host the run then falls back from.
//
// The lane half goes through laneToolUnion, which is the same derivation the
// run uses. Doctor re-deriving it would be a copy, and a copy drifts.
//
// It returns TWO attributions over the one tool list — the adapter that wants a
// tool, and the lane that does — because the callers need to say why the host
// needs each one, and "adapter" and "lane" are different sentences with
// different remedies. They are disjoint by construction: a tool an adapter
// already claimed is never also attributed to a lane, so a tool wanted by both
// gremlins and a Go lane is ONE entry in the probe and reports as the adapter's.
// A tool wanted by two lanes belongs to the FIRST in lane order, matching the
// list's own order — array position is already the tie-break everywhere lanes
// are read.
//
// This exists so `dross remote bootstrap` can tag a lane's install step without
// growing a private derivation of its own, which is exactly the drift its own
// test forbids.
func remoteProbeTools(p *project.Project) (tools []string, needBy, laneBy map[string]string) {
	tools, needBy = mutationcfg.Tools(p)
	seen := map[string]bool{}
	for _, tool := range tools {
		seen[tool] = true
	}
	// The host lock's tool rides the same probe, after the adapters and
	// before the lanes. It belongs to neither attribution: no adapter or
	// lane wants it — every remote mutation leg does — so the callers that
	// print "the X adapter needs it" or "lane Y needs it" skip it and doctor
	// reports it in its own words.
	if !seen[remote.LockTool] {
		seen[remote.LockTool] = true
		tools = append(tools, remote.LockTool)
	}
	laneBy = map[string]string{}
	for _, lane := range p.Runtime.TestLane {
		for _, tool := range testlane.Toolchain(lane.Command, lane.Prepare, lane.Toolchain) {
			if seen[tool] {
				// Claimed by an adapter. Left unattributed to any lane rather
				// than double-tagged: one tool, one reason it is being asked
				// for, or a caller printing both would report one gap twice.
				continue
			}
			if _, taken := laneBy[tool]; taken {
				continue
			}
			laneBy[tool] = lane.Name
		}
	}
	for _, tool := range laneToolUnion(p.Runtime.TestLane) {
		if seen[tool] {
			continue
		}
		seen[tool] = true
		tools = append(tools, tool)
	}
	return tools, needBy, laneBy
}

// reportLaneToolchains prints one row per declared lane: its effective
// toolchain, and which of it the host lacks.
//
// Nothing here is an ISSUE. A lane whose toolchain the host is missing still
// runs — it runs here instead, and reports its own suite result — so failing
// doctor on it would fail a repo that works, which is how a check gets ignored.
// An adapter's missing tool still increments, because a mutation run has no
// local fallback to take.
//
// A lane declaring no probable token at all is surfaced with the --toolchain
// fix rather than left to look like a missing binary. The locked first-token
// rule takes `FOO=1 go test` at its word, and a lane pinned to local by an env
// prefix with nothing naming the cause is exactly the silent failure the
// override exists to end.
func reportLaneToolchains(host string, p *project.Project, missing []string) {
	gone := map[string]bool{}
	for _, tool := range missing {
		gone[tool] = true
	}
	for _, lane := range p.Runtime.TestLane {
		tools := testlane.Toolchain(lane.Command, lane.Prepare, lane.Toolchain)
		var gaps []string
		for _, tool := range tools {
			if gone[tool] {
				gaps = append(gaps, tool)
			}
		}
		if len(gaps) == 0 {
			Printf("  ✓ lane %s toolchain on %s: %s\n", lane.Name, host, strings.Join(tools, " "))
			continue
		}
		Printf("  ⚠ lane %s will run on this machine — %s has no %s (lane needs %s)\n",
			lane.Name, host, strings.Join(gaps, " "), strings.Join(tools, " "))
		for _, tool := range gaps {
			// Decided by laneToolchainProblems, the same rules `dross validate`
			// applies to a declared override, so the two surfaces cannot
			// disagree about which tokens are probable at all.
			if len(laneToolchainProblems("", project.TestLane{Toolchain: []string{tool}})) == 0 {
				continue
			}
			Printf("    %q is not a binary name — it is the first token of the lane's own line, so no host will ever resolve it.\n", tool)
			Printf("    Fix: `dross test lane edit %s --toolchain <binary>`\n", lane.Name)
		}
	}
}

// checkRemoteMutation reports whether a granted remote is actually usable, and
// returns the number of issues found.
//
// The point is timing. Without it, an unreachable host or a missing remote
// toolchain is discovered part-way through a verify — after the tree has been
// pushed, with a report that is empty for reasons the run cannot distinguish
// from "nothing to measure". Doctor asks the same questions before any of that.
//
// No grant is an ADVISORY, not an issue. Most repos have no remote and never
// will; failing doctor on that would make a normal local clone look broken,
// which is how a check gets ignored.
func checkRemoteMutation(root, repoDir string, p *project.Project) int {
	issues := 0
	// ONE section, not one per consumer. The grant is a single authorization —
	// "run this repo's code on that machine" — and mutation runs and `dross
	// test` both read it. Reporting it twice would invite the reader to think
	// there are two grants to manage, and to withdraw one of them.
	Print("Remote:")
	// The lane attribution is unused HERE: doctor reports lanes through
	// reportLaneToolchains, which walks the lanes themselves and so already
	// knows which lane each row is about. It is bootstrap that needs the map,
	// from a step that only has a tool name.
	//
	// The tools ride the SAME probe the resolution already pays for. Resolving
	// and then probing separately would be two questions where the run asks
	// one, and the second answer is the one that drifts.
	tools, needBy, _ := remoteProbeTools(p)
	// Resolved the way a RUN resolves it — walking the pool — so doctor can
	// never bless a machine the next run would not use (c-2). With a pool
	// declared and the scalar host down, this names the host that answered.
	target, pool, err := resolveRemoteHost(root, repoDir, tools)
	switch {
	case err != nil:
		// A candidate that ANSWERED and failed is named, because the remedy is
		// about that machine. Anything else is a store-level refusal with no
		// host to name.
		if target != nil {
			Printf("  ✗ remote host %s is not usable: %v\n", target.Host, err)
			Printf("    Fix: check ssh access, or withdraw the grant with `dross remote revoke`.\n")
		} else {
			Printf("  ✗ %v\n", err)
		}
		issues++
	case pool.Fallback:
		// Granted and unreachable is a fault, not the ungranted advisory below:
		// the user authorized a machine and runs are silently coming home from
		// it. Every candidate is covered by this one line — Why names the last
		// one tried, and the skips are the pool's own notices.
		Printf("  ✗ remote host is not usable: %s\n", pool.Why)
		for _, n := range pool.Notices {
			Printf("    %s\n", n)
		}
		Printf("    Fix: check ssh access, or withdraw the grant with `dross remote revoke`.\n")
		issues++
	case target == nil:
		Printf("  ⚠ no remote granted — mutation runs and `dross test` run on this machine.\n")
		Printf("    Grant one with `dross remote grant <host> <workdir>`.\n")
	default:
		ready := pool.Candidates[0].Ready
		// Every candidate that was SKIPPED to get here, named. A doctor that
		// silently reported the second host would hide the first one being
		// down, which is the fault the user has to fix.
		for _, n := range pool.Notices {
			Printf("  ⚠ %s\n", n)
		}
		Printf("  ✓ %s reachable — workdir %s, %d cores (mutation runs and `dross test`)\n", target.Host, target.Workdir, ready.Cores)
		lockable := true
		for _, missing := range ready.Missing {
			if missing == remote.LockTool {
				// The lock's tool is a host requirement in its own words: no
				// adapter wanted it, every mutation leg needs it, and a leg
				// refuses rather than measuring unlocked without it (c-5).
				Printf("  ✗ %s is not installed on %s — the host lock needs it; mutation legs refuse until it is (util-linux).\n",
					remote.LockTool, target.Host)
				issues++
				lockable = false
				continue
			}
			// One line per missing tool, each naming the adapter that wanted
			// it: "something is missing" sends the user looking, and the
			// remedy differs per toolchain.
			//
			// Gated on needBy, because the probe set now carries lane tools
			// too. Without the gate a lane's missing binary would fall through
			// here and print "the  adapter needs it there" with an empty name,
			// and would count as an issue for a lane that still runs.
			adapter, wanted := needBy[missing]
			if !wanted {
				continue
			}
			Printf("  ✗ %s is not installed on %s — the %s adapter needs it there.\n", missing, target.Host, adapter)
			issues++
		}
		if lockable {
			reportHostLock(*target)
		}
		reportLaneToolchains(target.Host, p, ready.Missing)
	}
	Print("")
	return issues
}

// remoteLockStatusFn probes the host lock: LockStatusScript over the same
// transport a run uses, parsed by ParseLockStatus. A seam so doctor's tests
// can name a holder without a host. It is only reached once the probe has
// found flock, because the script's own tool gate would just say so again.
//
// The script is a parameter rather than built inside, so a test can pin that
// what reaches the host is the probing script and never a bare read of the
// record — a record is stale the moment its writer dies.
var remoteLockStatusFn = func(t remote.Target, script string) (remote.LockStatus, error) {
	out, err := remote.ExecScript(t, script)
	if err != nil {
		return remote.LockStatus{}, err
	}
	return remote.ParseLockStatus(out)
}

// reportHostLock prints one line about the host lock: free, or held by whom.
//
// A held lock is ADVISORY, never an issue. Another leg measuring on the host
// is the system working — the next leg waits its turn — and the line exists
// so the person reading doctor knows what a wait they are about to see is
// waiting on (c-5). A probe that could not be read is a warning for the same
// reason: it says nothing about this repo's health.
func reportHostLock(t remote.Target) {
	st, err := remoteLockStatusFn(t, remote.LockStatusScript())
	switch {
	case err != nil:
		Printf("  ⚠ could not read the host lock on %s: %v\n", t.Host, err)
	case st.ToolMissing:
		// The probe said flock was there a moment ago; the script disagrees.
		// Reported rather than reconciled — the host is changing under us.
		Printf("  ⚠ %s answered the probe but the lock script found no %s\n", t.Host, remote.LockTool)
	case !st.Held:
		Printf("  ✓ host lock free\n")
	case st.Holder.IsZero():
		Printf("  ℹ host busy: the host lock is held (holder not yet recorded)\n")
	default:
		h := st.Holder
		Printf("  ℹ host busy: held by %s/%s run %s (user %s, pid %d) since %s (%s)\n",
			h.Project, h.Phase, h.RunID, h.User, h.PID,
			h.Since.UTC().Format(time.RFC3339), time.Since(h.Since).Round(time.Second))
	}
}

// backfillResidueEntry is one roadmap phase backfill cannot close, with why.
type backfillResidueEntry struct {
	Slug   string
	Reason string
}

// backfillResidue lists the phases on any milestone's phases array that carry
// no completion marker AND cannot be closed from ship-commit evidence.
//
// The liveness half is deliberately OFFLINE: local refs plus the cached
// refs/remotes/origin/ ones, never ls-remote. The sweep proves absence against
// origin because it WRITES; doctor only reports, and reading a stale cache
// errs toward naming a phase that is actually closeable — a line the next
// `dross phase backfill` immediately corrects. Opening a network connection to
// print an advisory would be the worse trade.
//
// An unscaffolded roadmap slug is residue too, and the locked backfill_residue
// rule is applied literally: an in-flight or never-built phase is exactly the
// case "listed and not delivered" describes, so it is named rather than
// special-cased into silence.
func backfillResidue(root, repoDir, base string) []backfillResidueEntry {
	versions, err := milestone.List(root)
	if err != nil {
		return nil
	}
	ships := map[string]string{}
	if compare, err := resolveMainCompareRef(repoDir, base); err == nil {
		if found, err := backfillShipCommitsAtRef(repoDir, compare); err == nil {
			ships = found
		}
	}
	seen := map[string]bool{}
	var out []backfillResidueEntry
	for _, v := range versions {
		m, err := milestone.Load(milestone.FilePath(root, v))
		if err != nil {
			continue
		}
		for _, slug := range m.Phases {
			if seen[slug] {
				continue
			}
			seen[slug] = true
			if phase.Done(root, slug) {
				continue
			}
			switch {
			case !phase.DirExists(root, slug):
				out = append(out, backfillResidueEntry{slug, "on " + v + "'s roadmap with no phase directory"})
			case phaseBranchRefCached(repoDir, slug):
				out = append(out, backfillResidueEntry{slug, "phase/" + slug + " still exists — in flight, not shipped"})
			default:
				if _, ok := ships[backfillSlugKey(slug)]; !ok {
					out = append(out, backfillResidueEntry{slug, "no completion marker and no ship commit on " + base})
				}
			}
		}
	}
	return out
}

// phaseBranchRefCached reports whether phase/<slug> exists as a local branch or
// as a cached remote-tracking ref.
func phaseBranchRefCached(repoDir, slug string) bool {
	for _, ref := range []string{"refs/heads/phase/" + slug, "refs/remotes/origin/phase/" + slug} {
		if gitRefExists(repoDir, ref) {
			return true
		}
	}
	return false
}

// reportStrandedMirrors prints doctor's read-only stranded-mirror advisory.
//
// It reuses the sweep's own classifier rather than approximating it, so the
// number doctor prints is the number `dross issue reap` would act on. A second
// counting path would drift, and a detector that disagrees with the thing it
// points at is worse than no detector.
func reportStrandedMirrors() {
	Print("Board mirrors:")
	ctx, enabled, err := openBoard()
	if err != nil || !enabled {
		// Unreachable or misconfigured: the [board] checks above already own
		// config faults, and a network failure is not this section's to
		// report as one.
		Printf("  … could not read the board (%v)\n", err)
		Print("")
		return
	}
	plan, _, err := boardsync.Inventory(ctx, nil)
	if err != nil {
		Printf("  … could not classify board mirrors (%v)\n", err)
		Print("")
		return
	}
	if len(plan.Cards) == 0 {
		Print("  ✓ no stranded mirrors — every card matches its record")
		Print("")
		return
	}
	byLane := map[string]int{}
	for _, c := range plan.Cards {
		byLane[c.Lane]++
	}
	Printf("  ! %d stranded board mirror(s) — cards whose artefact finished but whose card did not\n", len(plan.Cards))
	for _, lane := range boardsync.ReapLanes {
		if n := byLane[lane.Name]; n > 0 {
			Printf("    %-12s %d    Fix: dross issue reap --namespace %s\n", lane.Name, n, lane.Name)
		}
	}
	Print("")
}
