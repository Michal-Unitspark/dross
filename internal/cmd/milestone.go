package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/milestone"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/render"
	"github.com/Rivil/dross/internal/ship"
	"github.com/Rivil/dross/internal/state"
)

func Milestone() *cobra.Command {
	c := &cobra.Command{
		Use:   "milestone",
		Short: "Manage milestones under .dross/milestones/",
	}
	c.AddCommand(
		milestoneList(),
		milestoneCreate(),
		milestoneShow(),
		milestoneGet(),
		milestoneSet(),
		milestoneAdd(),
		milestoneRemove(),
		milestoneReplace(),
		milestoneProgressCmd(),
		milestoneComplete(),
		milestonePrune(),
	)
	return c
}

// milestonePrune deletes milestone/* branches whose work is already on the main
// branch — local, and on origin where origin still has them.
//
// It is a separate, explicitly-typed command rather than a `doctor --fix` or a
// step inside `milestone complete` (locked prune_surface). Deleting a remote
// branch should be a conscious act, doctor stays a pure diagnostic, and folding
// the sweep into completion would never fire for a milestone that was
// interrupted — which is precisely how the stale branches accumulate.
//
// It never decides for itself what is stale: the set comes from
// staleMilestoneBranches, and a branch that detector did not name is not touched.
func milestonePrune() *cobra.Command {
	var dryRun, yes bool
	c := &cobra.Command{
		Use:   "prune",
		Short: "Delete milestone/* branches whose work is already on the main branch",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			repoDir := filepath.Dir(root)
			p, _, err := loadProject()
			if err != nil {
				return err
			}
			mainBranch := p.Repo.GitMainBranch
			if mainBranch == "" {
				mainBranch = "main"
			}

			// Refuse on real dirt before deleting anything: a branch delete is
			// irreversible from the user's side, and losing track of uncommitted
			// work while refs disappear is the worst possible pairing.
			if _, err := autoCommitDrossDirt(repoDir, "pruning milestone branches"); err != nil {
				return err
			}

			stale, err := staleMilestoneBranches(root, repoDir, mainBranch)
			if err != nil {
				return err
			}
			if len(stale) == 0 {
				Print("no stale milestone branches — nothing to prune")
				return nil
			}

			// The current branch cannot be deleted, and silently skipping it
			// would report a prune that did not happen. Refuse by name instead.
			//
			// The suggestion names the guarded verb, not `git checkout`: the
			// branch being left is stale by definition, so it is exactly the
			// vintage that may still track .dross/state.json, and a raw switch
			// off it replays that copy over the live machine-local one.
			cur, err := gitrun.Trim(repoDir, "symbolic-ref", "--short", "HEAD")
			if err != nil {
				cur = ""
			}
			for _, b := range stale {
				if b.Name == cur {
					return fmt.Errorf("HEAD is on %s, which is stale and would be deleted — switch to %s first (`dross checkout %s`)",
						b.Name, mainBranch, mainBranch)
				}
			}

			// Checked for every candidate before the first delete, so a blocked
			// branch stops the whole sweep rather than being discovered halfway
			// through an irreversible one.
			for _, b := range stale {
				if err := guardMilestoneBranchDelete(root, repoDir, b.Name, mainBranch); err != nil {
					return err
				}
			}

			// Show the whole set before touching any of it. Deleting a branch
			// on origin is irreversible from the user's side, and prune used to
			// do the lot with no preview — the one shape where a wrong
			// stale-detection result cannot be walked back.
			Printf("%d stale milestone branch(es) to delete:\n\n", len(stale))
			for _, b := range stale {
				where := "local"
				if b.HasRemote {
					where = "local + origin"
				}
				Printf("    %s (%s) — %s\n", b.Name, b.Reason, where)
			}
			Print("")

			if dryRun {
				Print("--dry-run: nothing deleted.")
				return nil
			}
			if !yes {
				if !confirmPrune(cmd) {
					return fmt.Errorf("not confirmed — nothing deleted. Re-run and answer y, or pass --yes to skip the prompt")
				}
			}

			for _, b := range stale {
				if err := gitrun.Run(repoDir, gitRefArgs("branch", []string{"-D"}, b.Name)...); err != nil {
					return fmt.Errorf("delete local %s: %w", b.Name, err)
				}
				where := "local"
				if b.HasRemote {
					if err := gitrun.Run(repoDir, gitRefArgs("push", []string{"--delete"}, "origin", b.Name)...); err != nil {
						return fmt.Errorf("delete origin/%s: %w", b.Name, err)
					}
					where = "local + origin"
				}
				Printf("pruned %s (%s) — %s\n", b.Name, b.Reason, where)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be deleted and exit without deleting anything")
	c.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (for scripted use)")
	return c
}

// confirmPrune asks before an irreversible delete, and answers NO when stdin is
// not a terminal.
//
// Refusing rather than proceeding on a non-interactive stdin is the whole point:
// a prompt that is silently skipped when nobody is watching is not a
// confirmation, it is a delay. A script that means it says so with --yes, which
// is a deliberate act the way the flag name reads.
func confirmPrune(cmd *cobra.Command) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	fmt.Fprint(cmd.OutOrStdout(), "Delete these branches (local and on origin)? [y/N] ")
	var resp string
	_, _ = fmt.Fscanln(os.Stdin, &resp)
	return isAffirmative(resp)
}

// isAffirmative is the answer parsing, split out from the prompt so it is
// reachable from a test.
//
// The read itself needs a terminal, so everything after it — including which
// answers mean yes — was unexecuted by the whole suite while sitting in front of
// an irreversible delete. Splitting it is what makes the rule assertable rather
// than merely plausible: only an explicit y/yes proceeds, and the default of a
// bare Enter is NO.
func isAffirmative(resp string) bool {
	r := strings.ToLower(strings.TrimSpace(resp))
	return r == "y" || r == "yes"
}

// milestoneComplete closes out a milestone in two steps. Without --finalize it
// opens the single milestone/<version> -> main PR (the staging->production
// boundary): main advances only here, once per milestone. With --finalize —
// run after that PR is merged — it fast-forwards local main from origin and
// deletes milestone/<version> local+remote, mirroring `dross phase complete`.
func milestoneComplete() *cobra.Command {
	var finalize bool
	var forceUnverified bool
	c := &cobra.Command{
		Use:   "complete [version]",
		Short: "Open the milestone->main PR, or (--finalize) ff main and delete the milestone branch after merge",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			repoDir := filepath.Dir(root)
			p, _, err := loadProject()
			if err != nil {
				return err
			}
			s, err := state.Load(filepath.Join(root, state.File))
			if err != nil {
				return err
			}

			version := ""
			if len(args) == 1 {
				version = args[0]
			} else {
				version = s.CurrentMilestone
			}
			if version == "" {
				return errors.New("no milestone version given and state has no current_milestone")
			}

			mainBranch := p.Repo.GitMainBranch
			if mainBranch == "" {
				mainBranch = "main"
			}
			msBranch := "milestone/" + version

			if finalize {
				return milestoneFinalize(root, repoDir, mainBranch, msBranch, version)
			}

			// Open mode: one PR of the milestone branch into main.
			if p.Remote.URL == "" || p.Remote.Provider == "" {
				return errors.New("project has no [remote].url or .provider — run /dross-options or /dross-onboard")
			}
			target, err := milestonePRBase(root, repoDir, mainBranch, version, msBranch)
			if err != nil {
				return err
			}

			// Every phase must have verified before the integration PR
			// carries it into the base branch. Gated on open rather than on
			// --finalize: finalize runs after the merge, where a refusal
			// strands a landed milestone instead of preventing anything.
			if !forceUnverified {
				if err := milestoneVerifyGate(root, version, target); err != nil {
					return err
				}
			}

			// The PR's head is resolved by the provider, so unpushed local
			// commits are silently absent from it — and --finalize then
			// deletes the branch that held them. Publish before opening.
			head, err := routeMilestoneHead(repoDir, msBranch)
			if err != nil {
				return err
			}
			switch {
			case head.Pushed && head.Commits > 0:
				Printf("pushed %d local commit(s) on %s to origin\n", head.Commits, msBranch)
			case head.Pushed:
				Printf("pushed %s to origin\n", msBranch)
			case head.ChorePR != nil:
				Printf("%s\n", head.ChorePR.narrate())
			}

			hosts, herr := remotePolicy(root, repoDir, p)
			if herr != nil {
				return herr
			}
			opts := buildOpenOpts(p, hosts)
			// The integration PR's head is origin's milestone branch, which
			// lacks the .dross chores until their chore PR merges: opening it
			// now would land the milestone without them.
			if err := waitForMilestoneChores(p, opts, msBranch, version, head); err != nil {
				return err
			}
			opts.HeadBranch = msBranch
			opts.BaseBranch = target
			opts.Title = fmt.Sprintf("milestone %s", version)
			opts.Body = fmt.Sprintf("Integration PR for milestone %s.\n\n"+
				"Merge as a **merge commit** (not squash) to preserve per-phase history on %s.",
				version, target)

			res, err := ship.OpenPRFunc(opts)
			if err != nil {
				// Idempotent: a duplicate PR (provider rejects a second open
				// for the same head->base) is a no-op, not a failure.
				if strings.Contains(strings.ToLower(err.Error()), "already exists") {
					Printf("milestone %s PR already open (%s -> %s) — nothing to do\n", version, msBranch, target)
					return nil
				}
				return fmt.Errorf("open milestone PR: %w", err)
			}
			Printf("PR opened: %s (#%d)\n", res.URL, res.Number)
			// milestone_main_merge: the milestone PR must land as a merge commit,
			// never a squash — and dross doesn't drive the merge, so surface the
			// requirement regardless of repo.squash_merge.
			Printf("Merge it as a MERGE COMMIT (not squash) to keep per-phase history on %s — do not squash even if repo.squash_merge is set.\n", target)
			Printf("After it merges: `dross milestone complete %s --finalize`\n", version)
			return nil
		},
	}
	c.Flags().BoolVar(&finalize, "finalize", false,
		"after the milestone PR merges: ff main from origin and delete milestone/<version> local+remote")
	c.Flags().BoolVar(&forceUnverified, "force-unverified", false,
		"skip the 'every phase in the milestone must verify pass' gate")
	return c
}

// milestonePRBase resolves which branch the milestone's integration PR targets.
//
// It is the recorded cut base (c-2) while that parent is still unmerged and
// still on origin, so a stacked milestone's PR shows its own commits rather
// than re-listing its parent's. Once the parent has merged — or its branch is
// gone from origin, where a PR base has to exist — the target is the main
// branch, never a branch `--finalize` is about to delete.
//
// A milestone with no toml, or no recorded base, targets the main branch:
// that is every milestone shipped before v1.2 and today's behaviour, so
// back-compat needs no migration (locked absent_base_reads_main).
func milestonePRBase(root, repoDir, mainBranch, version, msBranch string) (string, error) {
	m, err := milestone.Load(milestone.FilePath(root, version))
	if err != nil {
		return mainBranch, nil
	}
	recorded := m.BaseOr(mainBranch)
	if recorded == msBranch {
		return "", fmt.Errorf("milestone %s records its own branch (%s) as its base — a PR cannot target itself; fix `base` in %s",
			version, msBranch, milestone.FilePath(root, version))
	}
	if recorded == mainBranch {
		return mainBranch, nil
	}
	merged, _, err := milestoneMergedIntoMain(repoDir, recorded, mainBranch)
	if err != nil {
		return "", fmt.Errorf("resolve PR base for %s: %w", version, err)
	}
	// A base branch has to exist on the forge. A parent deleted on origin is
	// not something to target, whatever ancestry says about it.
	if merged || !gitRefExists(repoDir, "refs/remotes/origin/"+recorded) {
		return mainBranch, nil
	}
	return recorded, nil
}

// milestoneFinalize is the post-merge counterpart to opening the milestone PR:
// it fast-forwards local main from origin and deletes milestone/<version> local
// + remote. It refuses unless origin/milestone/<version> has actually merged, so
// unmerged integration work is never destroyed.
//
// "Merged" means merged into the branch this milestone was stacked on — its
// recorded base — or into the main branch. A guard fixed on origin/<main> would
// refuse forever for a child merged into its parent but not yet into main,
// deadlocking the stacking model's second half.
func milestoneFinalize(root, repoDir, mainBranch, msBranch, version string) error {
	// Classify before anything else. An already-finalized milestone has
	// nothing to fetch, guard or delete, and making it clear a dirty-tree gate
	// just to be told there is nothing to do is a refusal wearing an answer's
	// clothes. This first pass reads only the toml's status — no network.
	if cls, err := classifyFinalize(root, repoDir, mainBranch, msBranch, version); err != nil {
		return err
	} else if cls.State == finalizeAlreadyDone {
		return reportAlreadyFinalized(repoDir, msBranch, cls.Message)
	}

	//dross:taint-cleared status --porcelain prints two status letters and a repo path per line, never file content
	status, err := gitrun.Read(repoDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if status != "" {
		return dirtyTreeError("finalizing the milestone", status)
	}

	if err := gitrun.Run(repoDir, "fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}

	// Classify again now that origin's refs are current. The first pass ran
	// against a possibly-stale remote view, and "the PR merged since the last
	// fetch" is the ordinary case this command is invoked in — answering from
	// the pre-fetch refs would refuse a milestone that has in fact landed.
	cls, err := classifyFinalize(root, repoDir, mainBranch, msBranch, version)
	if err != nil {
		return err
	}
	switch cls.State {
	case finalizeAlreadyDone:
		return reportAlreadyFinalized(repoDir, msBranch, cls.Message)
	case finalizeBranchGone, finalizeUnmerged:
		// Both refusals are the classifier's own text — there is no second
		// copy of the merge guard here to drift out of step with it.
		return errors.New(cls.Message)
	}
	target, mergedIntoMain := cls.Target, cls.MergedIntoMain

	// A stacked child that still depends on this branch outranks the cleanup:
	// deleting out from under its open PR is irreversible from its side.
	if err := guardMilestoneBranchDelete(root, repoDir, msBranch, mainBranch); err != nil {
		return err
	}

	// Where HEAD lands. Only the merge-into-main case advances local main; a
	// child merged into its parent leaves main exactly where it was.
	landing := mainBranch
	if !mergedIntoMain && gitRefExists(repoDir, "refs/heads/"+target) {
		landing = target
	}
	cur, err := gitrun.Trim(repoDir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("git symbolic-ref failed (read current branch): %w", err)
	}
	if cur != landing {
		if err := checkoutBranch(repoDir, landing); err != nil {
			return err
		}
	}
	if mergedIntoMain {
		if err := guardedFF(repoDir, "origin/"+mainBranch); err != nil {
			return fmt.Errorf("fast-forward of %s from origin failed — local %s has diverged (git's abort is printed above)", mainBranch, mainBranch)
		}
	}

	// Record completion BEFORE the teardown, mirroring `dross phase complete`
	// (its state save precedes its branch deletes for the same reason). The
	// deletes are the step that can fail halfway — a protected branch, a
	// pre-receive hook, an offline origin — and they are also the step that
	// erases git's own evidence that this milestone merged. Writing the marker
	// afterwards would leave the one state nothing can recover from: branch
	// gone, status still active, and every later run refusing with a merge
	// question no ref can answer.
	if m, err := milestone.Load(milestone.FilePath(root, version)); err == nil {
		m.Milestone.Status = milestoneStatusComplete
		if err := m.Save(milestone.FilePath(root, version)); err != nil {
			return fmt.Errorf("record milestone %s complete: %w", version, err)
		}
	}

	// Delete the local milestone branch (only if it exists).
	if err := gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify"}, "refs/heads/"+msBranch)...); err == nil {
		if err := gitrun.Run(repoDir, gitRefArgs("branch", []string{"-D"}, msBranch)...); err != nil {
			return fmt.Errorf("git branch -D %s: %w", msBranch, err)
		}
	}
	// Delete the remote milestone branch (idempotent — only if origin has it).
	remoteRef, err := gitrun.Trim(repoDir, gitRefArgs("ls-remote", []string{"--heads"}, "origin", msBranch)...)
	if err != nil {
		return fmt.Errorf("git ls-remote origin %s: %w", msBranch, err)
	}
	if remoteRef != "" {
		if err := gitrun.Run(repoDir, gitRefArgs("push", []string{"--delete"}, "origin", msBranch)...); err != nil {
			return fmt.Errorf("git push origin --delete %s: %w", msBranch, err)
		}
	}

	if mergedIntoMain {
		Printf("milestone %s finalized — %s is at origin, %s deleted\n", version, mainBranch, msBranch)
	} else {
		// Claiming main advanced when it did not is exactly the false
		// completion state this milestone exists to stop.
		Printf("milestone %s finalized — merged into %s (%s not advanced), %s deleted\n", version, target, mainBranch, msBranch)
	}
	return nil
}

// reportAlreadyFinalized is the exit-0 arm: say so, and if a milestone branch
// is still sitting in the repo, name it and hand over the verb that removes it.
//
// It deletes nothing. A re-run of finalize is a question ("is this done?"), and
// the answer arriving with a side-effect is how a safe-to-repeat command turns
// into one people are afraid to type. `dross milestone prune` is the typed,
// destructive counterpart (locked prune_surface).
func reportAlreadyFinalized(repoDir, msBranch, message string) error {
	Printf("%s\n", message)
	if gitRefExists(repoDir, "refs/heads/"+msBranch) {
		Printf("leftover branch %s is still here — `dross milestone prune` removes branches whose milestone is done\n", msBranch)
	}
	return nil
}

func milestoneList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List milestones",
		RunE: func(_ *cobra.Command, _ []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			versions, err := milestone.List(root)
			if err != nil {
				return err
			}
			if len(versions) == 0 {
				Print("(no milestones)")
				return nil
			}
			for _, v := range versions {
				Print(v)
			}
			return nil
		},
	}
}

func milestoneCreate() *cobra.Command {
	var baseFlag string
	c := &cobra.Command{
		Use:   "create <version>",
		Short: "Create a new milestone (e.g. v0.1)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			path := milestone.FilePath(root, args[0])
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists", path)
			}

			mainBranch := "main"
			if p, _, err := loadProject(); err == nil && p.Repo.GitMainBranch != "" {
				mainBranch = p.Repo.GitMainBranch
			}
			// Resolved before anything is written, so a bad --base leaves
			// neither a toml nor a branch behind.
			repoDir := filepath.Dir(root)
			cutFrom, localOnly, err := resolveMilestoneCutPoint(root, repoDir, mainBranch, args[0], baseFlag)
			if err != nil {
				return err
			}

			m := &milestone.Milestone{
				Milestone: milestone.Meta{
					Version: args[0],
					Status:  "planning",
					Started: time.Now().UTC().Format("2006-01-02"),
					// Recorded now and read back verbatim ever after — the
					// stacking parent is a stored fact, never re-inferred.
					Base: cutFrom,
				},
			}
			if err := m.Save(path); err != nil {
				return err
			}
			Printf("created %s\n", path)

			// Cut + push the milestone integration branch as an
			// unconditional side effect of scoping (the v0.7 branch
			// topology). Skips silently in a non-git dir so `dross init`
			// flows and non-git usage keep working.
			base := cutFrom
			if base == "" {
				base = mainBranch
			}
			// Narrated before the cut, not after: the caveat is about the
			// resolution, which is already settled, and the eager push to an
			// unreachable origin is exactly the step that fails here.
			if localOnly {
				Printf("origin unreachable — cut point resolved from local refs; origin may have moved on\n")
			}
			branch, created, pushed, err := ensureMilestoneBranch(repoDir, base, args[0])
			if err != nil {
				return err
			}
			if created {
				Printf("cut %s from %s\n", branch, base)
			}
			if pushed {
				Printf("pushed %s to origin\n", branch)
			}
			return nil
		},
	}
	c.Flags().StringVar(&baseFlag, "base", "",
		"force the branch to cut from (default: the current milestone's branch while unmerged, else the main branch)")
	return c
}

// resolveMilestoneCutPoint answers which branch milestone/<version> should be
// cut from, and whether that answer came from local refs alone.
//
// A new milestone stacks on the current one while its integration branch is
// still unmerged, so its PR shows its own commits rather than inheriting the
// parent's. Once the parent has merged — or its branch is gone entirely, the
// ordinary state after `milestone complete --finalize` — the cut goes back to
// the main branch.
//
// The parent is whatever state.current_milestone names (locked
// stacking_parent): one authority, no scanning refs for the newest unmerged
// branch, which would reintroduce exactly the topology-guessing v1.2 exists to
// kill. `forced` (--base) wins over all of it, per locked base_override.
//
// An empty return means there is nothing to cut from — no git dir, or no main
// ref yet — and the caller records no base.
func resolveMilestoneCutPoint(root, repoDir, mainBranch, version, forced string) (cutFrom string, localOnly bool, err error) {
	if !isDir(filepath.Join(repoDir, ".git")) {
		return "", false, nil
	}
	// Both sources of the cut point, before the first ref probe: mainBranch is
	// [repo].git_main_branch straight out of committed config, and forced is
	// --base.
	if err := validateGitRef("repo.git_main_branch", mainBranch); err != nil {
		return "", false, err
	}
	if forced != "" {
		if err := validateGitRef("--base", forced); err != nil {
			return "", false, err
		}
		if !gitRefExists(repoDir, "refs/heads/"+forced) {
			return "", false, fmt.Errorf("--base %s: no such local branch", forced)
		}
		return forced, false, nil
	}
	// A repo with no commits has no main ref, so there is nothing to cut.
	if !gitRefExists(repoDir, "refs/heads/"+mainBranch) {
		return "", false, nil
	}
	s, err := state.Load(filepath.Join(root, state.File))
	if err != nil {
		// No readable state means no current milestone to stack on.
		return mainBranch, false, nil
	}
	cur := s.CurrentMilestone
	if cur == "" || cur == version {
		return mainBranch, false, nil
	}
	parent := "milestone/" + cur
	merged, localOnly, err := milestoneMergedIntoMain(repoDir, parent, mainBranch)
	if err != nil {
		return "", false, fmt.Errorf("resolve cut point for %s: %w", parent, err)
	}
	if merged {
		return mainBranch, localOnly, nil
	}
	return parent, localOnly, nil
}

// ensureMilestoneBranch cuts milestone/<version> from baseBranch (without
// checking it out — HEAD stays put) and pushes it to origin, so the integration
// branch exists as an unconditional side effect of scoping. Idempotent: an
// existing local ref is left as-is (re-scope no-ops rather than erroring) and
// the push is a no-op when origin already carries the ref at the same commit.
// Skips silently when the repo has no git, no base ref to cut from yet, or no
// origin remote — scoping must still succeed in those cases.
func ensureMilestoneBranch(repoDir, baseBranch, version string) (branch string, created, pushed bool, err error) {
	branch = "milestone/" + version
	if !isDir(filepath.Join(repoDir, ".git")) {
		return branch, false, false, nil
	}
	// baseBranch lands as a bare positional in `git branch <new> <base>` below,
	// which is one of the two sites where a leading dash stops being a name and
	// becomes an option.
	if err := validateGitRef("milestone base branch", baseBranch); err != nil {
		return branch, false, false, err
	}
	// Need a base ref to cut from; a repo with no commits has none.
	if gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify"}, "refs/heads/"+baseBranch)...) != nil {
		return branch, false, false, nil
	}
	// Idempotent create: only when the local ref is absent.
	if gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify"}, "refs/heads/"+branch)...) != nil {
		if e := gitrun.Run(repoDir, gitRefArgs("branch", nil, branch, baseBranch)...); e != nil {
			return branch, false, false, fmt.Errorf("git branch %s %s: %w", branch, baseBranch, e)
		}
		created = true
	}
	// Push only when an origin remote exists.
	if gitrun.Quiet(repoDir, "remote", "get-url", "origin") == nil {
		if e := gitrun.Run(repoDir, gitRefArgs("push", nil, "origin", branch)...); e != nil {
			return branch, created, false, fmt.Errorf("git push origin %s: %w", branch, e)
		}
		pushed = true
	}
	return branch, created, pushed, nil
}

func milestoneShow() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show [version]",
		Short: "Print a milestone toml (defaults to state.current_milestone)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			version := ""
			if len(args) == 1 {
				version = args[0]
			} else {
				s, err := state.Load(filepath.Join(root, state.File))
				if err != nil {
					return fmt.Errorf("no version given and load state: %w", err)
				}
				version = s.CurrentMilestone
				if version == "" {
					return errors.New("no version given and state has no current_milestone; run `dross milestone list` to see options")
				}
			}
			path := milestone.FilePath(root, version)
			m, err := milestone.Load(path)
			if err != nil {
				return err
			}
			if asJSON {
				return emitJSON(m)
			}
			Printf("# %s\n", path)
			return render.TOML(os.Stdout, m)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return c
}

// milestoneGet prints one or more dotted-path fields (e.g. milestone.title,
// scope.success_criteria). One path prints its bare value — a list one entry
// per line — while two or more emit a single keyed JSON object
// (locked multi_get_shape).
func milestoneGet() *cobra.Command {
	return &cobra.Command{
		Use:   "get [version] <dotted.path>...",
		Short: "Read one or more milestone fields by dotted path (version defaults to state.current_milestone)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			version, paths := "", args
			// Recognise a leading version by its SHAPE, never by elimination
			// against the known-field list: by-elimination reads a typo'd
			// path as a version and reports "no such milestone" instead of
			// naming the bad path.
			if len(args) > 1 && looksLikeMilestoneVersion(args[0]) {
				version, paths = args[0], args[1:]
			}
			m, _, err := loadMilestone(version)
			if err != nil {
				return err
			}
			return renderMultiGet(paths, func(path string) (any, error) {
				path, err := resolveBareMilestoneField(path, milestoneReadablePaths)
				if err != nil {
					return nil, err
				}
				val, ok, list := readMilestoneDotted(m, path)
				if !ok {
					return nil, fmt.Errorf("unknown milestone field: %s", path)
				}
				if isMilestoneListPath(path) {
					return list, nil
				}
				return val, nil
			})
		},
	}
}

// looksLikeMilestoneVersion reports whether s has the shape of a milestone
// version — a leading `v` followed by a digit. Every milestone on disk is
// vN.N, and no dotted path can start that way.
func looksLikeMilestoneVersion(s string) bool {
	return len(s) >= 2 && (s[0] == 'v' || s[0] == 'V') && s[1] >= '0' && s[1] <= '9'
}

// isMilestoneListPath reports whether a path reads a list, so an empty list
// serialises as [] rather than collapsing into an empty scalar.
func isMilestoneListPath(path string) bool {
	switch path {
	case "scope.success_criteria", "scope.non_goals", "phases":
		return true
	}
	return false
}

// milestoneSet writes a scalar dotted-path field. Use `add` for list fields.
func milestoneSet() *cobra.Command {
	return &cobra.Command{
		Use:   "set [version] <dotted.path> <value>",
		Short: "Write a single scalar milestone field (version defaults to state.current_milestone)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			version, dotted, value := "", args[0], args[1]
			if len(args) == 3 {
				version, dotted, value = args[0], args[1], args[2]
			}
			dotted, err := resolveBareMilestoneField(dotted, milestoneSettablePaths)
			if err != nil {
				return err
			}
			m, path, err := loadMilestone(version)
			if err != nil {
				return err
			}
			if err := writeMilestoneDotted(m, dotted, value); err != nil {
				return err
			}
			return m.Save(path)
		},
	}
}

// milestoneAdd appends a value to a list field (success_criteria, non_goals,
// phases). Idempotent — silently skips if the value is already present so the
// slash command can re-run safely.
func milestoneAdd() *cobra.Command {
	return &cobra.Command{
		Use:   "add [version] <list.path> <value>",
		Short: "Append a value to a list field (version defaults to state.current_milestone)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			version, dotted, value := "", args[0], args[1]
			if len(args) == 3 {
				version, dotted, value = args[0], args[1], args[2]
			}
			m, path, err := loadMilestone(version)
			if err != nil {
				return err
			}
			if err := appendMilestoneList(m, dotted, value); err != nil {
				return err
			}
			return m.Save(path)
		},
	}
}

// loadMilestone resolves the milestone version and loads it. An empty version
// falls back to state.current_milestone, mirroring `milestone show [version]`
// so get/set/add don't force the caller to repeat the milestone they're on.
func loadMilestone(version string) (*milestone.Milestone, string, error) {
	root, err := FindRoot()
	if err != nil {
		return nil, "", err
	}
	if version == "" {
		s, err := state.Load(filepath.Join(root, state.File))
		if err != nil {
			return nil, "", fmt.Errorf("no version given and load state: %w", err)
		}
		version = s.CurrentMilestone
		if version == "" {
			return nil, "", errors.New("no version given and state has no current_milestone; run `dross milestone list` to see options")
		}
	}
	path := milestone.FilePath(root, version)
	m, err := milestone.Load(path)
	if err != nil {
		return nil, "", err
	}
	return m, path, nil
}

// readMilestoneDotted returns either a scalar string (val, true, nil) or a
// list ("", true, slice). Unknown path returns ("", false, nil).
func readMilestoneDotted(m *milestone.Milestone, path string) (string, bool, []string) {
	switch path {
	case "milestone.version":
		return m.Milestone.Version, true, nil
	case "milestone.title":
		return m.Milestone.Title, true, nil
	case "milestone.status":
		return m.Milestone.Status, true, nil
	case "milestone.started":
		return m.Milestone.Started, true, nil
	case "milestone.shipped":
		return m.Milestone.Shipped, true, nil
	case "milestone.base":
		return m.Milestone.Base, true, nil
	case "scope.success_criteria":
		return "", true, m.Scope.SuccessCriteria
	case "scope.non_goals":
		return "", true, m.Scope.NonGoals
	case "phases":
		return "", true, m.Phases
	}
	return "", false, nil
}

// milestoneReadablePaths is the dotted surface readMilestoneDotted answers —
// the candidate list the bare-name resolver expands against on the read side.
// It is deliberately wider than milestoneSettablePaths: `milestone.base` is
// readable but not settable, because `milestone create` is its sole writer.
var milestoneReadablePaths = []string{
	"milestone.version",
	"milestone.title",
	"milestone.status",
	"milestone.started",
	"milestone.shipped",
	"milestone.base",
	"scope.success_criteria",
	"scope.non_goals",
}

// milestoneSettablePaths is the scalar surface writeMilestoneDotted accepts —
// the candidate list the bare-name resolver expands against. List fields are
// deliberately absent: `milestone add` owns those.
var milestoneSettablePaths = []string{
	"milestone.version",
	"milestone.title",
	"milestone.status",
	"milestone.started",
	"milestone.shipped",
}

// resolveBareMilestoneField expands an unambiguous bare field name — `status`
// — to its full dotted path, so the shape people reach for first works. It is
// purely additive: an already-dotted path passes through untouched, and a name
// matching no candidate is returned unchanged so writeMilestoneDotted still
// produces its own unknown-field error rather than a nearest-guess.
func resolveBareMilestoneField(name string, candidates []string) (string, error) {
	if strings.Contains(name, ".") {
		return name, nil
	}
	var matches []string
	for _, c := range candidates {
		if _, leaf, _ := strings.Cut(c, "."); leaf == name {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return name, nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous field name %q: matches %s — use the full dotted path", name, strings.Join(matches, ", "))
	}
}

func writeMilestoneDotted(m *milestone.Milestone, path, value string) error {
	switch path {
	case "milestone.version":
		m.Milestone.Version = value
	case "milestone.title":
		m.Milestone.Title = value
	case "milestone.status":
		// Rejected before the caller reaches Save, so an out-of-set status
		// leaves the milestone toml byte-identical.
		if !configenum.MilestoneStatuses.Has(value) {
			return fmt.Errorf("invalid milestone status: %q (want %s)", value, configenum.MilestoneStatuses.List())
		}
		m.Milestone.Status = configenum.Normalize(value)
	case "milestone.started":
		m.Milestone.Started = value
	case "milestone.shipped":
		m.Milestone.Shipped = value
	case "scope.success_criteria", "scope.non_goals", "phases":
		return fmt.Errorf("%s is a list — use `dross milestone add`", path)
	default:
		return fmt.Errorf("unknown or unsettable milestone field: %s", path)
	}
	return nil
}

func appendMilestoneList(m *milestone.Milestone, path, value string) error {
	switch normalizeListField(path) {
	case "scope.success_criteria":
		m.Scope.SuccessCriteria = appendUnique(m.Scope.SuccessCriteria, value)
	case "scope.non_goals":
		m.Scope.NonGoals = appendUnique(m.Scope.NonGoals, value)
	case "phases":
		m.Phases = appendUnique(m.Phases, value)
	default:
		return fmt.Errorf("not a list field %q — valid: scope.success_criteria, scope.non_goals, phases", path)
	}
	return nil
}

// normalizeListField canonicalizes the list-field path so `dross milestone
// add` tolerates the naming inconsistency: two fields live under scope.* but
// phases is bare. Accept both the bare and scope-prefixed spellings of each
// so a wrong-but-reasonable guess doesn't fail.
func normalizeListField(path string) string {
	switch path {
	case "success_criteria", "scope.success_criteria":
		return "scope.success_criteria"
	case "non_goals", "scope.non_goals":
		return "scope.non_goals"
	case "phases", "scope.phases":
		return "phases"
	}
	return path
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// routeMilestoneHead publishes local commits on milestone/<version> before
// the integration PR is opened against them.
//
// The PR's head is a branch name the provider resolves on ITS side, so
// anything committed locally but not pushed is simply absent from the PR —
// silently. That is how a v1.5 close-out shipped a milestone missing a commit:
// the branch was opened at the sha origin happened to hold, `--finalize` then
// deleted the branch local+remote, and the unpushed commit was left orphaned
// and unreachable by any ref. `dross ship` has always pushed phase/<id> before
// opening a phase PR (ship.go step 7); this is the same guarantee for the
// milestone head, which never got it.
//
// With commits ahead on a GitHub or Azure DevOps remote it first asks whether
// origin's milestone branch refuses direct pushes (readBaseProtection),
// routing as routeBaseChores does: protected sends a .dross-only ahead set
// through a chore PR and refuses a code commit, naming the phase-PR route;
// unknown pushes nothing and names the reason and the fix. Unprotected, and
// every other forge, keep the policy below.
//
// Three no-ops, one refusal, one push:
//   - no local refs/heads/<msBranch> — this machine only has origin's copy,
//     so there is nothing local that could be missing from the PR
//   - no origin/<msBranch> yet — `milestone create` pushes at scope time, so
//     this is a re-scoped or hand-made branch; push it, there is no remote
//     state to conflict with
//   - nothing ahead — already published
//   - ahead AND behind — a real divergence. Refuse by name and push nothing;
//     force-pushing a shared integration branch to open a PR is never the
//     right call, and the user needs to reconcile before the milestone lands.
//   - purely ahead — push it, and say how many commits were published
//
// A failed push is a hard error, mirroring pushBaseIfAheadDrossOnly's
// push_failure posture: continuing past it opens the PR against stale content,
// which is the exact bug this exists to prevent.
// milestoneHead is what routeMilestoneHead did with milestone/<version>.
type milestoneHead struct {
	Pushed  bool
	Commits int // commits pushed onto an existing origin branch
	ChorePR *chorePR
}

// pushMilestoneHeadIfAhead is routeMilestoneHead for a caller that reports
// only a direct push.
func pushMilestoneHeadIfAhead(repoDir, msBranch string) (pushed bool, commits int, err error) {
	h, err := routeMilestoneHead(repoDir, msBranch)
	return h.Pushed, h.Commits, err
}

func routeMilestoneHead(repoDir, msBranch string) (milestoneHead, error) {
	if gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify", "--quiet"}, "refs/heads/"+msBranch)...) != nil {
		return milestoneHead{}, nil
	}
	if ferr := gitrun.Run(repoDir, "fetch", "origin"); ferr != nil {
		return milestoneHead{}, fmt.Errorf("git fetch: %w", ferr)
	}
	if gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify", "--quiet"}, "refs/remotes/origin/"+msBranch)...) != nil {
		if perr := gitrun.Run(repoDir, gitRefArgs("push", []string{"-u"}, "origin", msBranch)...); perr != nil {
			return milestoneHead{}, fmt.Errorf("push %s to origin: %w", msBranch, perr)
		}
		return milestoneHead{Pushed: true}, nil
	}
	ahead, err := gitrun.Trim(repoDir, gitRefArgs("rev-list", nil, "origin/"+msBranch+".."+msBranch)...)
	if err != nil {
		return milestoneHead{}, fmt.Errorf("git rev-list origin/%s..%s: %w", msBranch, msBranch, err)
	}
	if ahead == "" {
		return milestoneHead{}, nil
	}
	n := len(strings.Fields(ahead))

	if c, routed, err := routeProtectedMilestone(repoDir, msBranch, strings.Fields(ahead)); err != nil || routed {
		return milestoneHead{ChorePR: c}, err
	}

	behind, err := gitrun.Trim(repoDir, gitRefArgs("rev-list", nil, msBranch+"..origin/"+msBranch)...)
	if err != nil {
		return milestoneHead{}, fmt.Errorf("git rev-list %s..origin/%s: %w", msBranch, msBranch, err)
	}
	if behind != "" {
		return milestoneHead{}, fmt.Errorf("local %s is ahead of origin/%s by %d commit(s) AND behind it by %d — "+
			"the integration PR would be opened against content that is neither. "+
			"Reconcile %s with origin before closing the milestone; refusing to force-push it for you",
			msBranch, msBranch, n, len(strings.Fields(behind)), msBranch)
	}
	if perr := gitrun.Run(repoDir, gitRefArgs("push", nil, "origin", msBranch)...); perr != nil {
		return milestoneHead{}, fmt.Errorf("push of %d local commit(s) on %s failed: %w\n"+
			"Refusing to open the PR — it would carry stale content and the unpushed commits would be lost at --finalize.",
			n, msBranch, perr)
	}
	return milestoneHead{Pushed: true, Commits: n}, nil
}

// routeProtectedMilestone handles commits ahead on a milestone branch that
// origin refuses direct pushes to. routed is false — and nothing happened —
// on a forge dross reads no protection from (anything but GitHub and Azure
// DevOps) or when the branch is unprotected, leaving the caller's own policy
// to run.
func routeProtectedMilestone(repoDir, msBranch string, ahead []string) (c *chorePR, routed bool, err error) {
	root := filepath.Join(repoDir, RootDirName)
	p, err := project.Load(filepath.Join(root, project.File))
	if err != nil {
		return nil, true, err
	}
	if !protectionReadable(p.Remote.Provider) {
		return nil, false, nil
	}
	hosts, err := remotePolicy(root, repoDir, p)
	if err != nil {
		return nil, true, err
	}
	opts := buildOpenOpts(p, hosts)
	prot := readBaseProtection(p, opts, msBranch)
	switch {
	case !prot.Known:
		return nil, true, fmt.Errorf("can't tell whether origin's %s refuses direct pushes (%s), so its %d local commit(s) were not pushed — "+
			"fix: %s; then re-run", msBranch, prot.Reason, len(ahead), prot.Fix)
	case !prot.Protected:
		return nil, false, nil
	}
	if f, sha, err := firstNonDross(repoDir, ahead); err != nil {
		return nil, true, err
	} else if f != "" {
		return nil, true, fmt.Errorf("local %s is ahead of origin/%s with commits touching non-.dross paths (e.g. %s in %.7s), and origin's %s refuses direct pushes — "+
			"code reaches a milestone branch through phase PRs: move the commits onto a phase branch and `dross ship` it, "+
			"then reset %s to origin/%s; refusing to push them for you",
			msBranch, msBranch, f, sha, msBranch, msBranch, msBranch)
	}
	main := p.Repo.GitMainBranch
	if main == "" {
		main = "main"
	}
	pr, err := publishChorePR(repoDir, opts, msBranch, main)
	if err != nil {
		return nil, true, err
	}
	return &pr, true, nil
}

// waitForMilestoneChores refuses to open the integration PR while a chore PR
// into the milestone branch is still open: this run's, or one an earlier run
// left. Only a protected milestone branch on a forge dross reads protection
// from (GitHub, Azure DevOps) can have one.
func waitForMilestoneChores(p *project.Project, opts ship.OpenOpts, msBranch, version string, head milestoneHead) error {
	if !protectionReadable(p.Remote.Provider) {
		return nil
	}
	waiting, armed := 0, false
	if c := head.ChorePR; c != nil && !c.Merged {
		waiting, armed = c.Number, c.AutoMerge
	} else if c == nil {
		if !readBaseProtection(p, opts, msBranch).Protected {
			return nil
		}
		open, err := ship.FindOpenPRByHeadFunc(opts, choreBranch(msBranch))
		if err != nil {
			return fmt.Errorf("look up an open chore PR into %s: %w", msBranch, err)
		}
		if open != nil {
			waiting = open.Number
		}
	}
	if waiting == 0 {
		return nil
	}
	return fmt.Errorf("chore PR %s into %s is still open, and the milestone PR would land without its .dross commits — "+
		"%s, then re-run `dross milestone complete %s`",
		pullURL(p.Remote.URL, waiting), msBranch, choreWaitHint(p.Remote.Provider, armed), version)
}

// choreWaitHint says how a waiting chore PR lands, in the forge's own words.
// armed is true only for this run's PR with auto-merge / auto-complete set;
// a PR this run could not arm, or one an earlier run left open, gets no
// promise that it lands by itself. GitHub keeps its existing wording.
func choreWaitHint(provider string, armed bool) string {
	if configenum.Normalize(provider) != "azure-devops" {
		return "merge it (it merges itself once allowed)"
	}
	if armed {
		return "it completes itself once the branch policies pass (auto-complete is armed)"
	}
	return "complete it in Azure Repos (by hand, unless auto-complete is set on it)"
}
