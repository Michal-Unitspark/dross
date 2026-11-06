package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/project"
	"github.com/Rivil/dross/internal/ship"
)

// chorePR is how a batch of .dross bookkeeping commits reached a protected
// base: the dross-chores/<base> branch it was pushed to and the PR into base
// that carries it (locked decision chore_push).
type chorePR struct {
	Base   string
	Branch string // dross-chores/<base with "/" → "-">
	Number int
	URL    string
	// Opened: this batch opened the PR; false means it joined one already open.
	Opened bool
	// AutoMerge: auto-merge (GitHub) or auto-complete (Azure DevOps) is
	// armed; the forge merges the PR once base's checks or policies pass.
	AutoMerge bool
	// Merged: the PR merged at once (nothing on base to wait for).
	Merged bool
	// Manual: auto-merge or auto-complete is unavailable, so the PR waits
	// for a human.
	Manual bool
	// ado: the PR is on Azure Repos, so narration says auto-complete. Zero
	// on GitHub, which keeps a GitHub chorePR exactly what it was.
	ado bool
}

// narrate is the one line ship and complete print for a published batch.
func (c chorePR) narrate() string {
	verb := "joined"
	if c.Opened {
		verb = "opened"
	}
	state := "auto-merge armed — it merges once the required checks pass"
	switch {
	case c.Merged:
		state = "merged"
	case c.Manual && c.ado:
		state = "auto-complete could not be set — complete it by hand"
	case c.Manual:
		state = "auto-merge is unavailable on this repo — merge it by hand"
	case c.ado:
		state = "auto-complete armed — it completes with a merge commit once the branch policies pass"
	}
	return fmt.Sprintf(".dross chores on %s go through a PR (%s is protected): %s chore PR #%d %s — %s",
		c.Base, c.Base, verb, c.Number, c.URL, state)
}

// choreBranch is the branch a base's .dross chores are pushed to. The "/" →
// "-" swap keeps it a single path segment, so it never matches a ruleset
// include like refs/heads/milestone/*.
func choreBranch(base string) string {
	return "dross-chores/" + strings.ReplaceAll(base, "/", "-")
}

// publishChorePR sends the .dross-only commits local base holds and
// origin/<base> lacks to origin through a PR, never a push to base itself.
// The caller has just fetched origin and checked that every such commit
// touches only .dross/.
//
// It pushes local base's exact tip to dross-chores/<base>: a fast-forward
// while that branch's PR is open (the batch joins it, opening nothing); a
// stale chore branch with no open PR is recreated with --force-with-lease and
// gets a new PR. Then it arms auto-merge with the merge-commit method, so the
// merged base carries these exact commits and local base fast-forwards to it.
// Auto-merge being unavailable still succeeds: the PR is open and its URL is
// reported for a human to merge.
//
// On mainBranch a batch that would change the release tag is refused before
// any ref moves: auto-merge would make that merge — and the release
// release.yml cuts from it — hands-free. The basis is the batch's own change,
// the tag at merge-base(base, origin/<base>) against the tag at base's tip;
// never base's tip against origin/<base>, which a phase squash may already
// have bumped.
func publishChorePR(repoDir string, opts ship.OpenOpts, base, mainBranch string) (chorePR, error) {
	c := chorePR{Base: base, Branch: choreBranch(base), ado: configenum.Normalize(opts.Provider) == "azure-devops"}
	arm := "Auto-merge is armed"
	if c.ado {
		arm = "Auto-complete is armed"
	}
	tip, err := gitrun.Trim(repoDir, gitRefArgs("rev-parse", []string{"--verify"}, "refs/heads/"+base)...)
	if err != nil {
		return c, fmt.Errorf("git rev-parse %s: %w", base, err)
	}

	if base == mainBranch {
		if err := releaseGuard(repoDir, base, tip); err != nil {
			return c, err
		}
	}

	open, err := ship.FindOpenPRByHeadFunc(opts, c.Branch)
	if err != nil {
		return c, fmt.Errorf("look up an open chore PR on %s: %w", c.Branch, err)
	}

	if err := pushChoreBranch(repoDir, c.Branch, tip, open, opts.URL); err != nil {
		return c, err
	}

	if open != nil {
		c.Number, c.URL = open.Number, open.URL
	} else {
		o := opts
		o.HeadBranch, o.BaseBranch = c.Branch, base
		o.Title = "chore(dross): .dross bookkeeping for " + base
		o.Body = "Bookkeeping commits dross made under `.dross/` on `" + base + "`. `" + base +
			"` refuses direct pushes, so they arrive through this PR.\n\n" +
			arm + " with a merge commit, so the commits land on `" + base +
			"` unchanged and every machine's local `" + base + "` fast-forwards to the result. " +
			"Nothing outside `.dross/` is in this PR.\n"
		o.Reviewers = nil
		res, err := ship.OpenPRFunc(o)
		if err != nil {
			return c, fmt.Errorf("open a chore PR %s → %s: %w", c.Branch, base, err)
		}
		c.Number, c.URL, c.Opened = res.Number, res.URL, true
	}
	if c.URL == "" {
		c.URL = pullURL(opts.URL, c.Number)
	}

	am, err := ship.AutoMergePRFunc(opts, c.Number, "merge")
	switch {
	case errors.Is(err, ship.ErrAutoMergeUnavailable):
		c.Manual = true
	case err != nil:
		fmt.Fprintf(os.Stderr, "chore PR: %s\n", c.URL)
		what := "auto-merge"
		if c.ado {
			what = "auto-complete"
		}
		return c, fmt.Errorf("the chore PR for %s is open (printed above), but arming %s on it failed: %w", base, what, err)
	default:
		c.AutoMerge, c.Merged = am.AutoEnabled, am.Merged
	}
	return c, nil
}

// pushChoreBranch puts tip on origin's chore branch. With origin's copy
// absent or an ancestor of tip, a plain push fast-forwards it. Otherwise the
// branch has moved somewhere tip doesn't reach: an open PR there may hold
// someone's commits, so refuse naming it; with no open PR the branch is a
// leftover, recreated with --force-with-lease against the SHA last fetched.
func pushChoreBranch(repoDir, branch, tip string, open *ship.OpenResult, repoURL string) error {
	remote := "refs/remotes/origin/" + branch
	refspec := tip + ":refs/heads/" + branch
	old, err := gitrun.Trim(repoDir, gitRefArgs("rev-parse", []string{"--verify", "--quiet"}, remote)...)
	if err != nil || old == "" {
		return pushChore(repoDir, branch, nil, refspec)
	}
	if old == tip {
		return nil
	}
	if gitrun.Quiet(repoDir, gitRefArgs("merge-base", []string{"--is-ancestor"}, old, tip)...) == nil {
		return pushChore(repoDir, branch, nil, refspec)
	}
	if open != nil {
		return fmt.Errorf("%s has moved somewhere local commits don't reach, and chore PR %s is still open on it — "+
			"merge or close that PR, then re-run; refusing to push over it", branch, pullURL(repoURL, open.Number))
	}
	return pushChore(repoDir, branch, []string{"--force-with-lease=refs/heads/" + branch + ":" + old}, refspec)
}

func pushChore(repoDir, branch string, opts []string, refspec string) error {
	if err := gitrun.Run(repoDir, gitRefArgs("push", opts, "origin", refspec)...); err != nil {
		return fmt.Errorf("push .dross chores to %s: %w", branch, err)
	}
	return nil
}

// releaseGuard refuses a main batch that changes the release tag project.toml
// projects (amended chore_push): the tag at merge-base(base, origin/<base>)
// against the tag at tip.
func releaseGuard(repoDir, base, tip string) error {
	mb, err := gitrun.Trim(repoDir, gitRefArgs("merge-base", nil, tip, "refs/remotes/origin/"+base)...)
	if err != nil {
		return fmt.Errorf("git merge-base %s origin/%s: %w", base, base, err)
	}
	before, after := releaseTagAt(repoDir, mb), releaseTagAt(repoDir, tip)
	if before == after {
		return nil
	}
	fmt.Fprintf(os.Stderr, "release tag projected from %s/%s: %s before these chores, %s after\n", RootDirName, project.File, orNone(before), orNone(after))
	return fmt.Errorf("the .dross chores on %s change the release tag (printed above), and merging them into %s would cut that release with no one watching — "+
		"refusing to publish them. Put the version bump on the milestone branch instead, then reset %s to origin/%s "+
		"(`git switch %s && git reset --keep origin/%s`); nothing was pushed",
		base, base, base, base, base, base)
}

// releaseTagAt is the release tag project.toml projects at commit sha; "" when
// it has none there.
func releaseTagAt(repoDir, sha string) string {
	src, err := gitrun.Raw(repoDir, gitRefArgs("show", nil, sha+":"+RootDirName+"/"+project.File)...)
	if err != nil {
		return ""
	}
	tag, err := project.ReleaseTag([]byte(src))
	if err != nil {
		return ""
	}
	return tag
}

// pullURL is the forge's URL for PR n of the repo at repoURL — built from
// config rather than quoted from a forge answer, so it can ride an error. An
// Azure Repos URL (it carries /_git/) gets <repo>/pullrequest/<n>, without the
// user@ Azure DevOps puts in a clone URL; any other is GitHub's <repo>/pull/<n>.
func pullURL(repoURL string, n int) string {
	trimmed := strings.TrimSuffix(repoURL, "/")
	if strings.Contains(trimmed, "/_git/") {
		if u, err := url.Parse(trimmed); err == nil && u.User != nil {
			u.User = nil
			trimmed = u.String()
		}
		return trimmed + "/pullrequest/" + strconv.Itoa(n)
	}
	return strings.TrimSuffix(trimmed, ".git") + "/pull/" + strconv.Itoa(n)
}

func orNone(tag string) string {
	if tag == "" {
		return "(none)"
	}
	return tag
}
