package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Rivil/dross/internal/changes"
	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/localstore"
	"github.com/Rivil/dross/internal/phase"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/prtriage"
	"github.com/Rivil/dross/internal/render"
	"github.com/Rivil/dross/internal/secretscan"
	"github.com/Rivil/dross/internal/ship"
	"github.com/Rivil/dross/internal/state"
	"github.com/Rivil/dross/internal/verify"
)

// remotePolicy derives the API host allowlist for [remote] calls: the host of
// [remote].url, the built-in SaaS defaults, and the machine-local additions
// from .dross/local.toml.
//
// It returns an error — rather than an empty extras list — when git reports
// local.toml tracked, because a committed local.toml is a repo authorizing its
// own exfiltration host through the one input the derivation trusts.
func remotePolicy(root, repoDir string, p *project.Project) (hostallow.Policy, error) {
	extra, err := localstore.ReadAllowHosts(root, repoDir)
	if err != nil {
		return hostallow.Policy{}, err
	}
	return hostallow.Derive(p.Remote.URL, extra), nil
}

// buildOpenOpts maps a project's [remote] config onto ship.OpenOpts. Extracted
// from the inline ship literal so the provider / auth_user / auth_scheme /
// project_id / api_version wiring is unit-testable — a dropped field (e.g. GitLab silently
// using default auth or a derived project id even when the user overrode them,
// or Bitbucket losing the user half of its Basic credential and 401ing on every
// ship) is caught by ship_test.go.
func buildOpenOpts(p *project.Project, hosts hostallow.Policy) ship.OpenOpts {
	return ship.OpenOpts{
		Hosts:      hosts,
		Provider:   p.Remote.Provider,
		URL:        p.Remote.URL,
		APIBase:    p.Remote.APIBase,
		AuthEnv:    p.Remote.AuthEnv,
		AuthUser:   p.Remote.AuthUser,
		AuthScheme: p.Remote.AuthScheme,
		ProjectID:  p.Remote.ProjectID,
		APIVersion: string(p.Remote.APIVersion),
		Reviewers:  p.Remote.Reviewers,
	}
}

// buildCommentOpts maps a project's [remote] config onto ship.CommentOpts,
// carrying the same provider / auth / project fields as buildOpenOpts.
func buildCommentOpts(p *project.Project, hosts hostallow.Policy) ship.CommentOpts {
	return ship.CommentOpts{
		Hosts:      hosts,
		Provider:   p.Remote.Provider,
		URL:        p.Remote.URL,
		APIBase:    p.Remote.APIBase,
		AuthEnv:    p.Remote.AuthEnv,
		AuthUser:   p.Remote.AuthUser,
		AuthScheme: p.Remote.AuthScheme,
		ProjectID:  p.Remote.ProjectID,
		APIVersion: string(p.Remote.APIVersion),
	}
}

// Ship orchestrates /dross-ship: pushes the current phase/<id> branch
// and opens a provider-aware PR with auto-assigned human reviewers.
// The provider's squash-merge collapses the per-task commits on the
// branch into a single commit on main — no client-side squash needed.
func Ship() *cobra.Command {
	var (
		title           string
		body            string
		bodyFile        string
		noPush          bool
		draft           bool
		forceUnverified bool
		forcePush       bool
		printBody       bool
		auto            bool
		jsonOut         bool
	)
	c := &cobra.Command{
		Use:   "ship [phase-id]",
		Short: "Push phase/<id> and open a PR via the project's provider",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			repoDir := filepath.Dir(root)

			// narrate is the human-facing progress channel. Under --json it
			// goes silent so stdout carries exactly one machine-readable JSON
			// object (emitted at the end); otherwise it prints as usual.
			narrate := func(format string, a ...any) {
				if !jsonOut {
					Printf(format, a...)
				}
			}
			p, _, err := loadProject()
			if err != nil {
				return err
			}
			s, err := state.Load(filepath.Join(root, state.File))
			if err != nil {
				return err
			}

			// 1) Resolve phase id.
			phaseID := ""
			if len(args) == 1 {
				phaseID = args[0]
			} else {
				phaseID = s.CurrentPhase
			}
			if phaseID == "" {
				return errors.New("no phase id given and state has no current_phase")
			}
			phaseBranch := "phase/" + phaseID

			// 2) Load phase artefacts.
			phaseDir := phase.Dir(root, phaseID)
			spec, err := phase.LoadSpec(filepath.Join(phaseDir, "spec.toml"))
			if err != nil {
				return fmt.Errorf("load spec: %w", err)
			}
			vTests, vToml := verify.FilePaths(root, phaseID)
			_ = vTests
			vrf, err := verify.LoadVerify(vToml)
			if err != nil {
				return fmt.Errorf("load verify for %s: %w\n\nNext: run /dross-verify to write verify.toml, then `dross verify finalize %s`, then re-run ship",
					phaseID, err, phaseID)
			}
			// LoadVerify reports a MISSING file as (nil, nil) — not an error —
			// so a phase that was never verified arrives here with vrf nil and
			// every dereference below it is a crash rather than the refusal
			// this path already knows how to word.
			if vrf == nil {
				return fmt.Errorf("no verify.toml for %s — the phase has not been verified\n\nNext: run /dross-verify to write it, then `dross verify finalize %s`, then re-run ship",
					phaseID, phaseID)
			}

			// 3) Pre-flight gates.
			//
			// Secret gate first — ahead of the verdict switch, the
			// --print-body / --no-push returns, and the auto-commit below.
			// autoCommitDrossDirt only fires on a dirty tree, so a token
			// already COMMITTED into a .dross artifact would otherwise ride
			// a clean tree straight to the push; this scans the tracked set
			// regardless. It is the shared scanner, not the whole of
			// `dross validate` (the ship_gate_scope decision): pulling all
			// of validate in here would newly fail ships on schema problems
			// in any old phase.
			if hits, err := scanDrossArtifacts(repoDir); err != nil {
				return err
			} else if len(hits) > 0 {
				return fmt.Errorf("secret in tracked .dross artifact — fix it by hand or mark the line dross:allow-secret, then re-run ship\n%w",
					&secretscan.ErrHit{Hits: hits})
			}
			if p.Remote.URL == "" || p.Remote.Provider == "" {
				return errors.New("project has no [remote].url or .provider — run /dross-options or /dross-onboard")
			}
			// Heal-before-gate: a resolved verdict that was never
			// finalized gets its outcome event recorded here, BEFORE the
			// pass-only refusal — a partial/fail verdict is recorded,
			// then still refused. Keeps the forget-to-finalize hole from
			// leaking pending events into stats. The marker write dirties
			// verify.toml; the pre-stage autoCommitDrossDirt below folds
			// it into the push.
			switch vrf.Verify.Verdict {
			case "pass", "partial", "fail":
				if !vrf.Verify.Finalized {
					recorded, verdict, err := finalizeVerify(root, phaseID)
					if err != nil {
						return fmt.Errorf("auto-finalize verify for %s: %w", phaseID, err)
					}
					if recorded {
						narrate("auto-finalized verify verdict=%s (was resolved but unrecorded)\n", verdict)
					}
					vrf.Verify.Finalized = true
				}
			}
			if vrf.Verify.Verdict != "pass" && !forceUnverified {
				switch vrf.Verify.Verdict {
				case "pending":
					return fmt.Errorf("verify verdict is \"pending\" for %s — finalize with `dross verify finalize %s`, or pass --force-unverified to override",
						phaseID, phaseID)
				case "fail", "partial":
					return fmt.Errorf("verify verdict is %q for %s — fix the failing criteria and re-run /dross-verify, or pass --force-unverified to override",
						vrf.Verify.Verdict, phaseID)
				default:
					return fmt.Errorf("verify verdict is %q (need \"pass\"); use --force-unverified to override",
						vrf.Verify.Verdict)
				}
			}

			// Must be on the phase branch. Pushing from anywhere else is
			// almost certainly a mistake — phase work belongs on phase/<id>.
			//
			// The suggestion names the guarded verb, not `git checkout`: a raw
			// checkout of a branch that still tracks .dross/state.json replays
			// it over the live machine-local copy without complaint, which is
			// what destroyed a live history on the state-json-branch-safety
			// ship. A refusal that hands the user the unguarded form reopens
			// that hole by hand, one obedient copy-paste at a time.
			cur, err := gitrun.Trim(repoDir, "symbolic-ref", "--short", "HEAD")
			if err != nil {
				return fmt.Errorf("read current branch: %w", err)
			}
			if cur != phaseBranch {
				return fmt.Errorf("must be on %s to ship (currently on %s); switch with `dross phase checkout %s`",
					phaseBranch, cur, phaseID)
			}

			// A pass covers the tree its run measured, not whatever is here
			// now. Checked on the phase branch (another branch's tree would
			// name changes that are not this phase's) and before --print-body,
			// --no-push, the red-proof repoint and the .dross auto-commit, so
			// a stale pass is refused before ship writes or pushes anything.
			if vrf.Verify.Verdict == "pass" {
				if err := gateFreshness(root, repoDir, phaseID, forceUnverified); err != nil {
					return err
				}
			}

			// An accepted review comment is a promise to do its task; shipping
			// with that task still pending or in progress breaks it. Checked
			// before the body, the push and every provider call, and a record
			// that does not load refuses rather than being skipped.
			if err := gateAcceptedTriage(phaseDir); err != nil {
				return err
			}

			// 4) Title + body.
			if title == "" {
				title = fmt.Sprintf("phase %s: %s", phaseID, spec.Phase.Title)
			}
			if bodyFile != "" {
				b, err := os.ReadFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read --body-file: %w", err)
				}
				body = string(b)
			}
			if body == "" {
				rec, err := changes.Load(changes.FilePath(root, phaseID), phaseID)
				if err != nil {
					return fmt.Errorf("load changes.json: %w", err)
				}
				body = ship.BuildPRBody(spec, vrf, rec.Reviews)
			}

			if printBody {
				Print(body)
				return nil
			}

			if noPush {
				narrate("--no-push set; not pushing or opening PR.\n")
				return nil
			}

			// c-7: repair any red-proof pin held up only by this phase's own
			// remote-tracking branch, BEFORE the merge that deletes it. Run
			// ahead of the clean-tree gate on purpose — the repair rewrites a
			// tracked doc, so it commits its own two files rather than leaving
			// dirt the gate would refuse, and the rewrite then rides this
			// phase's PR where it is reviewed alongside the code.
			if repointed, err := repointDoomedRedProofs(root, repoDir, phaseID); err != nil {
				return err
			} else if repointed {
				narrate("repointed a red proof that this merge would have orphaned\n")
			}

			// Pre-stage gate: the tree must be clean before ship starts
			// staging its own commits. Bookkeeping-only dirt under .dross/
			// is auto-committed so it rides the push; any other dirt refuses
			// before anything is staged or pushed.
			drossCommitted, err := autoCommitDrossDirt(repoDir, "shipping")
			if err != nil {
				return err
			}
			if drossCommitted {
				narrate("auto-committed .dross-only bookkeeping\n")
			}

			// 6) Resolve the PR base: the active milestone's integration
			//    branch when it exists, else main (rollout_cutover /
			//    no_milestone_fallback via the shared resolver). Resolved
			//    before the push so the remote-base guard can run first.
			baseBranch, milestoneActive, err := resolveNewWorkBase(repoDir, root)
			if err != nil {
				return err
			}
			// Guard only the milestone case: milestone/<version> is pushed at
			// scope time, so its absence on origin means scoping was
			// incomplete — refuse rather than open a PR against a base the
			// provider can't see. main is the always-present default.
			if milestoneActive {
				if err := gitrun.Quiet(repoDir, gitRefArgs("ls-remote", []string{"--exit-code", "--heads"}, "origin", baseBranch)...); err != nil {
					return fmt.Errorf("base branch %q is not on origin — it is pushed when the milestone is scoped; re-scope or push it before shipping", baseBranch)
				}
			}
			// Nudge (never require) scoping a milestone when falling back to
			// main with none active — mirrors phase create / base-branch.
			// Silent in the cutover case (a milestone is set but predates the
			// branch model).
			if !milestoneActive && s.CurrentMilestone == "" {
				narrate("no milestone active — PR targets %s; scope one with `dross milestone <version>` for a staging branch\n", baseBranch)
			}

			// Safety net (c-2): .dross-only chores sitting unpushed on the
			// local base re-seed divergence at the next squash-merge. Ship
			// already requires network, so it absorbs them — pushed straight
			// to an unprotected base, or through a chore PR when the base
			// refuses direct pushes; a code-ahead base, an unreadable
			// protection answer or a failed push is a hard refusal.
			baseRoute, err := routeBaseChores(repoDir, baseBranch)
			if err != nil {
				return err
			}
			narrateBaseChores(narrate, baseBranch, "", baseRoute)
			quickRoute, quickBase, err := routeQuickBaseChores(repoDir, root, baseBranch)
			if err != nil {
				return err
			}
			narrateBaseChores(narrate, quickBase, " (recorded quick_base)", quickRoute)
			shipChore := baseRoute.ChorePR
			if shipChore == nil {
				shipChore = quickRoute.ChorePR
			}

			// Everything past the base safety net is an ordered ladder of
			// stages, each gated on the one before, with ONE durable flip
			// point at the end (the shipped_timing locked decision): the
			// shipped markers are written only after the push that carries
			// the PR record has landed. Between PR-open and that push the
			// phase still reads verified, and status names the retry.
			//
			// (a) Load the record: a PR number already there means this is
			//     a re-run (the existing_pr_source locked decision — the
			//     record is read before any provider is asked).
			ch, err := changes.Load(changes.FilePath(root, phaseID), phaseID)
			if err != nil {
				return fmt.Errorf("load changes.json: %w", err)
			}
			existingPR := ch.PR
			changesRel := filepath.Join(".dross", "phases", phaseID, changes.File)

			// (b) Push phase/<id>, gated on origin rather than on the index:
			//     a record commit left local by a failed run is ahead of
			//     origin and goes out here whether or not anything is
			//     staged. A behind-only or diverged branch refuses BEFORE
			//     any PR exists, with pushPhaseBranch's own pull / --force
			//     guidance (the diverged_phase_branch locked decision).
			pushed, err := pushPhaseBranch(repoDir, phaseBranch, forcePush)
			if err != nil {
				return err
			}
			if pushed {
				narrate("Pushed %s to origin\n", phaseBranch)
			} else {
				narrate("%s already on origin\n", phaseBranch)
			}

			hosts, herr := remotePolicy(root, repoDir, p)
			if herr != nil {
				return herr
			}
			opts := buildOpenOpts(p, hosts)
			opts.HeadBranch = phaseBranch
			opts.BaseBranch = baseBranch
			opts.Title = title
			opts.Body = body
			opts.Draft = draft

			// (b') Record first, provider fallback (the existing_pr_source
			//      locked decision). Only when the record carries no number
			//      is the provider asked for an open PR with head phase/<id>
			//      — the narrow window where ship died between opening the
			//      PR and writing the record, which the record cannot
			//      answer. A hit is recorded like a re-run's existing PR; an
			//      unwired provider announces the skip and opens; any other
			//      lookup failure refuses before OpenPR — fail-closed, since
			//      "could not check" read as "none" is the duplicate this
			//      exists to prevent.
			var existingURL string
			if existingPR == 0 {
				found, ferr := ship.FindOpenPRByHeadFunc(opts, phaseBranch)
				switch {
				case ferr == nil && found != nil && found.Number > 0:
					existingPR = found.Number
					existingURL = found.URL
					narrate("found open PR #%d for %s — recording it rather than opening a second\n", found.Number, phaseBranch)
				case errors.Is(ferr, ship.ErrHeadPRLookupUnsupported):
					narrate("open-PR lookup skipped: %s does not support it; opening\n", p.Remote.Provider)
				case ferr != nil:
					return fmt.Errorf("could not check origin for an open PR on %s: %w; fix and re-run `dross ship %s`", phaseBranch, ferr, phaseID)
				}
			}

			// (c) Open the PR — unless the record (or the lookup above)
			//     already names one, in which case the run is a retry and
			//     there is nothing to open.
			if auto {
				// Per-invocation, non-destructive: request zero reviewers
				// for this run without mutating remote.reviewers config.
				// --auto governs prompts/defaults only — the generated body
				// stays the default and explicit --body/--body-file/--draft
				// still win (they were already applied above). Downstream
				// narration + telemetry read opts.Reviewers, so clearing it
				// here is the single source that suppresses the "Reviewers
				// requested" line and zeroes the telemetry count too.
				opts.Reviewers = nil
			}
			var res *ship.OpenResult
			existing := existingPR > 0
			if existing {
				// changes.json stores no URL, so a re-run reports the
				// number alone; the URL is the first run's or the provider
				// page. A provider hit carries its URL through.
				res = &ship.OpenResult{Number: existingPR, URL: existingURL}
				narrate("PR #%d already open — pushing the pending record\n", existingPR)
			} else {
				res, err = ship.OpenPRFunc(opts)
				if err != nil && res == nil {
					return fmt.Errorf("open PR: %w", err)
				}
				if res != nil {
					narrate("PR opened: %s (#%d)\n", res.URL, res.Number)
					// Read opts.Reviewers, not p.Remote.Reviewers: under
					// --auto the former is cleared, so no reviewers were
					// actually requested and this line must stay silent.
					if len(opts.Reviewers) > 0 {
						narrate("Reviewers requested: %s\n", strings.Join(opts.Reviewers, ", "))
					}
				}
				if err != nil {
					// Non-fatal post-PR errors (e.g. reviewer add failed).
					narrate("Warning: %v\n", err)
				}
			}

			// (d) Record the PR number and the base it was opened against in
			//     the phase-scoped changes.json — the record `dross phase
			//     complete` gates its merge check on — and commit it onto
			//     phase/<id>. NOT the shipped status: that is the flip at
			//     (f), and it must not ride a commit whose push has not
			//     landed yet. Commit only when the add staged something, so
			//     a re-run recording the same pair is not an error.
			if res != nil && res.Number > 0 {
				if err := changes.SetPR(root, phaseID, res.Number); err != nil {
					return fmt.Errorf("persist PR number: %w", err)
				}
				// The authoritative half of base_write_timing: what the PR
				// was actually opened against wins over the value create
				// recorded, if the two ever diverge (a milestone scoped
				// after the fork is the ordinary way that happens).
				if err := changes.SetBase(root, phaseID, baseBranch); err != nil {
					return fmt.Errorf("persist PR base branch: %w", err)
				}
				if err := commitIfStaged(repoDir, changesRel, fmt.Sprintf("chore(dross): record PR #%d for %s", res.Number, phaseID)); err != nil {
					return err
				}
			}

			// (e) Push the record. A failure here is a hard error and leaves
			//     the record commit in place (the failed_push_residue locked
			//     decision): it is the unit the next run retries, and the
			//     origin gate at (b) is what finds it. A divergence refusal
			//     carries pushPhaseBranch's own pull / --force guidance, never
			//     the bare re-run — a re-run would loop on the same refusal.
			if res != nil && res.Number > 0 {
				if pushed, err := pushPhaseBranch(repoDir, phaseBranch, forcePush); err != nil {
					return fmt.Errorf("%w\nthe PR record for #%d is committed locally but not on origin — re-run `dross ship %s` to push the record", err, res.Number, phaseID)
				} else if pushed {
					narrate("Pushed PR record to %s\n", phaseBranch)
				}
			}

			// (f) The flip. Only now — the record is on origin — do both
			//     markers read shipped, together: state.json (machine-local,
			//     gitignored, so nothing to stage) and changes.json's status
			//     (tracked, so a fresh clone reads it). The status write is a
			//     second commit pushed on its own; if THAT push fails the
			//     phase IS shipped — both markers say so and only origin's
			//     copy lags — so the error says shipped and names the re-run,
			//     after the JSON and telemetry below have been emitted.
			//
			//     Idempotent: a re-run re-writes the same status, the
			//     `shipped <id>` entry is history-scan-guarded so it never
			//     doubles up, and the commit only runs when something staged.
			var markerErr error
			if res != nil && res.Number > 0 {
				s.CurrentPhaseStatus = "shipped"
				if !historyHasAction(s, "shipped "+phaseID) {
					s.Touch(fmt.Sprintf("shipped %s", phaseID))
				}
				if err := s.Save(filepath.Join(root, state.File)); err != nil {
					return fmt.Errorf("save state: %w", err)
				}
				if err := changes.SetStatus(root, phaseID, changes.StatusShipped); err != nil {
					return fmt.Errorf("persist phase status: %w", err)
				}
				if err := commitIfStaged(repoDir, changesRel, fmt.Sprintf("chore(dross): mark %s shipped", phaseID)); err != nil {
					return err
				}
				if _, err := pushPhaseBranch(repoDir, phaseBranch, forcePush); err != nil {
					markerErr = fmt.Errorf("%w\n%s is shipped (PR #%d) but the shipped marker is committed locally and not on origin — re-run `dross ship %s` to push it", err, phaseID, res.Number, phaseID)
				} else {
					narrate("Marked %s shipped — once the PR merges, `dross phase complete %s` writes the completion record\n", phaseID, phaseID)
				}
			}

			// Telemetry — capture shape of this ship without leaking repo
			// URL, body content, or reviewer names.
			tags := map[string]string{
				"provider": p.Remote.Provider,
				"result":   shipResultTag(res, err, existing),
			}
			if draft {
				tags["draft"] = "true"
			}
			if forceUnverified || forcePush {
				tags["force"] = "true"
			}
			if auto {
				tags["auto"] = "true"
			}
			counts := map[string]int{
				// opts.Reviewers is post-auto-clearing: --auto records 0,
				// matching what was actually requested.
				"reviewers":   len(opts.Reviewers),
				"body_chars":  len(body),
				"title_chars": len(title),
			}
			RecordOutcomeEvent("ship", counts, nil, tags)

			// --json: emit a single machine-readable object on stdout (the
			// only thing printed under --json, since narration was suppressed).
			// Composable with --auto — result is the same shipResultTag bucket.
			// Emitted BEFORE a marker-push error is returned: the phase is
			// shipped and the caller must learn its number either way.
			if jsonOut {
				out := struct {
					URL    string `json:"url"`
					Number int    `json:"number"`
					Result string `json:"result"`
					// ChorePR is the PR the base's .dross chores went through
					// when the base refuses direct pushes; absent otherwise.
					ChorePR *struct {
						URL    string `json:"url"`
						Number int    `json:"number"`
						State  string `json:"state"`
					} `json:"chore_pr,omitempty"`
				}{Result: shipResultTag(res, err, existing)}
				if res != nil {
					out.URL = res.URL
					out.Number = res.Number
				}
				if c := shipChore; c != nil {
					out.ChorePR = &struct {
						URL    string `json:"url"`
						Number int    `json:"number"`
						State  string `json:"state"`
					}{URL: c.URL, Number: c.Number, State: choreState(c)}
				}
				b, mErr := render.MarshalJSON(out)
				if mErr != nil {
					return fmt.Errorf("marshal --json output: %w", mErr)
				}
				Print(string(b))
			}
			if markerErr != nil {
				return markerErr
			}
			return nil
		},
	}
	c.Flags().StringVar(&title, "title", "", "PR title (default: 'phase <id>: <spec title>')")
	c.Flags().StringVar(&body, "body", "", "PR body override (overrides generated body)")
	c.Flags().StringVar(&bodyFile, "body-file", "", "read PR body from file")
	c.Flags().BoolVar(&noPush, "no-push", false, "don't push the phase branch or open a PR")
	c.Flags().BoolVar(&draft, "draft", false, "open the PR as draft")
	c.Flags().BoolVar(&forceUnverified, "force-unverified", false, "ship anyway when the verify verdict is not pass, or is a pass whose measured tree has since changed")
	c.Flags().BoolVar(&forcePush, "force", false,
		"force-with-lease the push (use when re-pushing after rewriting phase/<id>)")
	c.Flags().BoolVar(&printBody, "print-body", false, "print the generated PR body and exit (no push, no PR)")
	c.Flags().BoolVar(&auto, "auto", false,
		"non-interactive: request zero reviewers for this run (without mutating remote.reviewers) and use the generated body; for scripts and loops")
	c.Flags().BoolVar(&jsonOut, "json", false,
		"emit a single JSON object {url, number, result} on stdout and suppress human narration; composable with --auto for scripts and loops")
	c.AddCommand(shipComment())
	c.AddCommand(shipRecover())
	return c
}

// gateAcceptedTriage refuses while any accepted review comment names a task
// its plan still has pending or in progress, naming each comment and task. A
// task done, or failed with its recorded reason, has settled. No pr-triage.toml,
// or one holding only rejects and routes, leaves ship as it was.
func gateAcceptedTriage(phaseDir string) error {
	path := filepath.Join(phaseDir, prtriage.File)
	rec, data, err := prtriage.Load(path)
	if err != nil {
		return fmt.Errorf("%s does not load, so its accepted comments cannot be checked — fix it (`dross validate` names each problem), then re-run ship: %w", path, err)
	}
	if data == nil {
		return nil
	}
	var accepts []prtriage.Resolution
	for _, r := range rec.Resolution {
		if r.Verdict == prtriage.VerdictAccept {
			accepts = append(accepts, r)
		}
	}
	if len(accepts) == 0 {
		return nil
	}
	plan, err := phase.LoadPlan(filepath.Join(phaseDir, "plan.toml"))
	if err != nil {
		return fmt.Errorf("load plan to check accepted review comments: %w", err)
	}
	status := map[string]string{}
	for _, t := range plan.Task {
		status[t.ID] = t.Status
	}
	var open []string
	for _, r := range accepts {
		id, task := prtriage.OneLine(r.ID), prtriage.OneLine(r.Task)
		switch st, ok := status[r.Task]; {
		case !ok:
			open = append(open, fmt.Sprintf("%s -> %s (not in the plan)", id, task))
		case st != phase.StatusDone && st != phase.StatusFailed:
			open = append(open, fmt.Sprintf("%s -> %s (%s)", id, task, prtriage.OneLine(st)))
		}
	}
	if len(open) == 0 {
		return nil
	}
	return fmt.Errorf("accepted review comments still have open tasks — finish them with /dross-execute, or mark one failed with its reason, then re-run ship:\n  %s",
		strings.Join(open, "\n  "))
}

// shipComment posts a markdown comment to an existing PR via the
// project's provider. Used by /dross-review to publish the aggregated
// subagent panel findings as a single consolidated comment.
func shipComment() *cobra.Command {
	var (
		prNumber int
		body     string
		bodyFile string
	)
	c := &cobra.Command{
		Use:   "comment --pr <n> (--body \"...\" | --body-file <path>)",
		Short: "Post a comment to a PR via the project's provider",
		RunE: func(_ *cobra.Command, _ []string) error {
			if prNumber <= 0 {
				return errors.New("--pr is required")
			}
			if body == "" && bodyFile == "" {
				return errors.New("either --body or --body-file is required")
			}
			if bodyFile != "" {
				b, err := os.ReadFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read --body-file: %w", err)
				}
				body = string(b)
			}
			p, _, err := loadProject()
			if err != nil {
				return err
			}
			if p.Remote.URL == "" || p.Remote.Provider == "" {
				return errors.New("project has no [remote].url or .provider — run /dross-options or /dross-onboard")
			}
			root, rerr := FindRoot()
			if rerr != nil {
				return rerr
			}
			hosts, herr := remotePolicy(root, filepath.Dir(root), p)
			if herr != nil {
				return herr
			}
			co := buildCommentOpts(p, hosts)
			co.PRNumber = prNumber
			co.Body = body
			if err := ship.PostComment(co); err != nil {
				return fmt.Errorf("post comment: %w", err)
			}
			Printf("Posted comment to PR #%d\n", prNumber)
			return nil
		},
	}
	c.Flags().IntVar(&prNumber, "pr", 0, "PR number to comment on (required)")
	c.Flags().StringVar(&body, "body", "", "comment body (markdown)")
	c.Flags().StringVar(&bodyFile, "body-file", "", "read comment body from file")
	return c
}

// shipResultTag classifies a ship's outcome into a single token. Used
// for the telemetry "result" tag so ship outcomes are easy to bucket.
func shipResultTag(res *ship.OpenResult, err error, existing bool) string {
	switch {
	case existing && res != nil:
		return "existing" // re-run: the record already named the PR; nothing opened
	case err != nil && res == nil:
		return "failed"
	case err != nil && res != nil:
		return "partial" // PR opened, post-step (reviewers etc.) failed
	case res != nil:
		return "opened"
	default:
		return "noop"
	}
}

// commitIfStaged stages rel and commits it under msg when — and only when —
// the add actually staged a change, so a re-run that re-writes the same
// content neither errors on "nothing to commit" nor grows the log.
func commitIfStaged(repoDir, rel, msg string) error {
	if err := gitrun.Run(repoDir, gitPathArgs("add", nil, rel)...); err != nil {
		return fmt.Errorf("git add %s: %w", rel, err)
	}
	if gitrun.Quiet(repoDir, "diff", "--cached", "--quiet") == nil {
		return nil // nothing staged
	}
	if err := gitrun.Run(repoDir, "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// narrateBaseChores reports what the base safety net did with base's .dross
// chores. note qualifies the branch, e.g. " (recorded quick_base)".
func narrateBaseChores(narrate func(string, ...any), base, note string, r baseChores) {
	switch {
	case r.Pushed:
		narrate("pushed unpushed .dross chores on %s%s to origin\n", base, note)
	case r.ChorePR != nil:
		narrate("%s\n", r.ChorePR.narrate())
	}
}

// choreState is a chore PR's state for `ship --json`: "merged", "manual"
// (auto-merge unavailable — a human merges it) or "auto-merge".
func choreState(c *chorePR) string {
	switch {
	case c.Merged:
		return "merged"
	case c.Manual:
		return "manual"
	}
	return "auto-merge"
}
