package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/localstore"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/state"
)

// BaseBranch prints the branch that new phase/quick work should fork off (and
// that ship should target): the active milestone's integration branch when it
// exists, else the configured main branch. stdout carries only the bare branch
// name so callers can consume `$(dross base-branch)`; the scope-a-milestone
// nudge (no active milestone) goes to stderr to keep stdout clean.
func BaseBranch() *cobra.Command {
	return &cobra.Command{
		Use:   "base-branch",
		Short: "Print the branch new phase/quick work should fork off",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			root, err := FindRoot()
			if err != nil {
				return err
			}
			base, milestoneActive, err := resolveNewWorkBase(filepath.Dir(root), root)
			if err != nil {
				return err
			}
			Print(base)
			// Nudge only when there's no active milestone at all — never in
			// the cutover case (a milestone is set but predates the branch
			// model), matching phase create's fallback nudge.
			if !milestoneActive {
				if s, err := state.Load(filepath.Join(root, state.File)); err == nil && s.CurrentMilestone == "" {
					fmt.Fprintf(os.Stderr, "no milestone active — rooted on %s; scope one with `dross milestone <version>` for integration branching\n", base)
				}
			}
			return nil
		},
	}
}

// baseChores is what the base safety net did with the .dross chores ahead on a
// base branch.
type baseChores struct {
	// Pushed: pushed straight to origin/<base> — an unprotected base, or a
	// forge dross doesn't read protection from.
	Pushed bool
	// ChorePR: sent through a chore PR, because origin refuses direct pushes
	// to the base (locked decision chore_push).
	ChorePR *chorePR
}

// pushBaseIfAheadDrossOnly is routeBaseChores for a caller that reports only a
// direct push.
func pushBaseIfAheadDrossOnly(repoDir, base string) (pushed bool, err error) {
	r, err := routeBaseChores(repoDir, base)
	return r.Pushed, err
}

// routeBaseChores is the ship / phase-complete pre-flight safety net (the
// chore_push locked decision): .dross-only chores committed to the local base
// branch — a pause auto-snapshot, a gate auto-commit, a recovery restore — sit
// unpushed and re-seed base divergence at the next squash-merge. Local-only
// writers like pause never push; the commands already doing network absorb it.
//
// It reads the shared origin comparison (compareWithOrigin — the
// shared_origin_gate locked decision: the same reading gates the PR-record
// push, and neither carries its own copy). Nothing ahead, or no origin/<base>,
// is a no-op. With commits ahead on a GitHub or Azure DevOps remote it asks
// the forge whether the base refuses direct pushes (readBaseProtection — a
// GitHub ruleset requiring a PR, or an enabled blocking Azure Repos branch
// policy), and routes on the answer:
//
//   - protected: .dross-only commits — purely ahead or ahead and behind — go
//     through a chore PR (publishChorePR); a code commit ahead is refused,
//     naming the quick/ branch and PR that can carry it
//   - unknown: nothing is pushed, and the error names the reason and the fix —
//     a push that might be refused, or might go through a bypass, is neither
//     safe to make nor to skip silently
//   - unprotected, or any other forge: today's policy, below
//
// Today's policy: a base also behind origin is left to the ff-only / --recover
// machinery downstream; .dross-only commits are pushed with
// `git push origin <base>`; a code commit ahead is refused, since unpushed code
// on the base is a real divergence the user must reconcile.
//
// A failed push is a hard error (the push_failure locked decision): proceeding
// past it would re-seed the exact divergence this exists to kill.
func routeBaseChores(repoDir, base string) (baseChores, error) {
	d, err := compareWithOrigin(repoDir, base)
	if err != nil {
		return baseChores{}, err
	}
	if d.Missing || len(d.Ahead) == 0 {
		return baseChores{}, nil // no origin/<base> to be ahead of, or level with it
	}

	root := filepath.Join(repoDir, RootDirName)
	p, err := project.Load(filepath.Join(root, project.File))
	if err != nil {
		return baseChores{}, err
	}
	if protectionReadable(p.Remote.Provider) {
		hosts, err := remotePolicy(root, repoDir, p)
		if err != nil {
			return baseChores{}, err
		}
		opts := buildOpenOpts(p, hosts)
		prot := readBaseProtection(p, opts, base)
		switch {
		case !prot.Known:
			return baseChores{}, fmt.Errorf("can't tell whether origin's %s refuses direct pushes (%s), so the .dross chores ahead on it were not pushed — "+
				"fix: %s; then re-run", base, prot.Reason, prot.Fix)
		case prot.Protected:
			if f, sha, err := firstNonDross(repoDir, d.Ahead); err != nil {
				return baseChores{}, err
			} else if f != "" {
				return baseChores{}, fmt.Errorf("local %s is ahead of origin/%s with commits touching non-.dross paths (e.g. %s in %.7s), and origin's %s refuses direct pushes — "+
					"they can only reach it through a PR: move them to a branch (`git switch -c quick/<name>`), push it and open a PR into %s, "+
					"then reset %s to origin/%s; refusing to push them for you",
					base, base, f, sha, base, base, base, base)
			}
			main := p.Repo.GitMainBranch
			if main == "" {
				main = "main"
			}
			c, err := publishChorePR(repoDir, opts, base, main)
			if err != nil {
				return baseChores{}, err
			}
			return baseChores{ChorePR: &c}, nil
		}
	}

	if d.Behind {
		// True divergence (ahead AND behind): a push can't fast-forward, and
		// the ff-only / --recover machinery downstream owns this state with
		// its own guided errors — the safety net stays out of it. It only
		// handles the purely-ahead base, where origin/<base> is an ancestor.
		return baseChores{}, nil
	}
	f, sha, err := firstNonDross(repoDir, d.Ahead)
	if err != nil {
		return baseChores{}, err
	}
	if f != "" {
		return baseChores{}, fmt.Errorf("local %s is ahead of origin/%s with commits touching non-.dross paths (e.g. %s in %.7s) — "+
			"push or reconcile %s manually before continuing; refusing to push it for you",
			base, base, f, sha, base)
	}
	if err := gitrun.Run(repoDir, gitRefArgs("push", nil, "origin", base)...); err != nil {
		return baseChores{}, fmt.Errorf("safety-net push of .dross chores on %s failed: %w\n"+
			"Refusing to continue — proceeding would leave %s diverged from origin again.",
			base, err, base)
	}
	return baseChores{Pushed: true}, nil
}

// firstNonDross returns the first path outside .dross/ that any of shas
// touches, and the commit touching it; "" when every commit is .dross-only.
func firstNonDross(repoDir string, shas []string) (file, sha string, err error) {
	for _, sha := range shas {
		files, err := gitrun.Read(repoDir, gitRefArgs("diff-tree", []string{"--no-commit-id", "--name-only", "-r", "--root"}, sha)...)
		if err != nil {
			return "", "", fmt.Errorf("git diff-tree %s: %w", sha, err)
		}
		//dross:taint-cleared diff-tree --name-only prints one repo path per line, and f is one of them
		for _, f := range strings.Split(files, "\n") {
			if f != "" && !underDross(unquotePath(f)) {
				return f, sha, nil
			}
		}
	}
	return "", "", nil
}

// pushQuickBaseIfRecorded is routeQuickBaseChores for a caller that reports
// only a direct push.
func pushQuickBaseIfRecorded(repoDir, root, phaseBase string) (pushed bool, branch string, err error) {
	r, branch, err := routeQuickBaseChores(repoDir, root, phaseBase)
	return r.Pushed, branch, err
}

// routeQuickBaseChores extends the chore_push safety net to the OTHER branch
// a repo can accumulate unpushed .dross chores on: the one a standalone
// `/dross-quick` committed to, recorded as quick_base in .dross/local.toml.
// The phase base is handled by the caller's own routeBaseChores; this covers a
// quick task that landed on a different branch (main, while the phase was
// forked off a milestone branch), whose chores would otherwise sit unpushed
// and re-seed divergence at the next squash-merge. It routes exactly as
// routeBaseChores does, protection included.
//
// It is driven by the record, never by inference — the whole point of storing
// the branch quick actually used. Three no-ops: no record, a record equal to
// the phase base (already handled, and doing it twice is pointless), and a
// record naming a branch with no local ref (a stale machine-local value, which
// is expected: the store is gitignored and is simply overwritten by the next
// standalone quick rather than cleared on success).
//
// A refusal from the underlying net is wrapped so it names the record as the
// source — otherwise the user sees a branch they never mentioned to this
// command and has no idea where it came from.
func routeQuickBaseChores(repoDir, root, phaseBase string) (baseChores, string, error) {
	qb := localstore.ReadKey(root, "quick_base")
	if qb == "" || qb == phaseBase {
		return baseChores{}, "", nil
	}
	if gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify"}, "refs/heads/"+qb)...) != nil {
		return baseChores{}, "", nil
	}
	r, err := routeBaseChores(repoDir, qb)
	if err != nil {
		return baseChores{}, qb, fmt.Errorf("%w\n(%s is the quick_base recorded in .dross/local.toml — the branch a standalone quick task committed to)", err, qb)
	}
	return r, qb, nil
}

// resolveNewWorkBase decides the branch that new phase/quick work should fork
// off (and that ship should target). It returns the active milestone's
// integration branch — milestone/<current_milestone> — but only when that ref
// actually exists in repoDir; otherwise it falls back to the configured main
// branch.
//
// This single existence check is where two locked v0.7 decisions live:
//   - rollout_cutover: a milestone scoped before the branch model shipped has
//     no milestone/<version> ref, so it transparently falls back to main. The
//     branch's existence IS the switch — no retrofit, no stored flag/date.
//   - no_milestone_fallback: with no current_milestone at all, work roots on
//     main.
//
// milestoneActive reports whether the milestone branch was chosen, so callers
// can tailor messaging (e.g. the scope-a-milestone nudge on the fallback path).
func resolveNewWorkBase(repoDir, root string) (base string, milestoneActive bool, err error) {
	p, err := project.Load(filepath.Join(root, project.File))
	if err != nil {
		return "", false, err
	}
	main := p.Repo.GitMainBranch
	if main == "" {
		main = "main"
	}

	s, err := state.Load(filepath.Join(root, state.File))
	if err != nil {
		return "", false, err
	}
	if s.CurrentMilestone == "" {
		return main, false, nil
	}

	branch := "milestone/" + s.CurrentMilestone
	// The ref-existence probe is the cutover mechanism: a pre-cutover
	// milestone (or a non-git dir) has no such ref, so we fall back to main.
	if gitrun.Quiet(repoDir, gitRefArgs("rev-parse", []string{"--verify"}, "refs/heads/"+branch)...) != nil {
		return main, false, nil
	}
	return branch, true, nil
}
