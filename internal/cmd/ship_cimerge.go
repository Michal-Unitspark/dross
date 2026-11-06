package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Rivil/dross/internal/changes"
	"github.com/Rivil/dross/internal/gitrun"
	"github.com/Rivil/dross/internal/pathfence"
	"github.com/Rivil/dross/internal/ship"
)

// `dross ship ci` and `dross ship merge` are the provider half of ship.md §5
// and §6 for the forges whose CI and merge dross drives itself (Azure DevOps,
// Bitbucket). Both act on the PR changes.json records for the phase, at the
// head origin last told this repo about — refs/remotes/origin/phase/<id> — so
// the verdict ci reports and the commit merge pins are the same one.

// ship ci exit statuses. 1 stays "the read failed": it is never a verdict.
const (
	exitCIFail    = 2
	exitCIPending = 3
	exitCINone    = 4
	exitCIAsk     = 5
)

// exitMergeBlocked is ship merge's "a branch policy holds it" status.
const exitMergeBlocked = 2

// shaHexLen is the length of a SHA-1 object id in hex.
const shaHexLen = 40

// shipTarget is what both verbs act on: the phase's recorded PR and the head
// it is watched at, with the opts that reach its forge.
type shipTarget struct {
	phaseID string
	sha     string
	opts    ship.OpenOpts
}

// loadShipTarget validates phaseID, then reads its PR number from changes.json
// and its watched head from origin's tracking ref. Every refusal lands before
// the forge seam is called.
func loadShipTarget(phaseID string) (shipTarget, error) {
	if err := pathfence.Segment("phase id", phaseID); err != nil || phaseID != strings.TrimSpace(phaseID) {
		return shipTarget{}, fmt.Errorf("invalid phase id %q: pass the bare phase slug, e.g. `dross ship ci auth`", phaseID)
	}
	if err := validateGitRef("phase id argument", phaseID); err != nil {
		return shipTarget{}, err
	}
	root, err := FindRoot()
	if err != nil {
		return shipTarget{}, err
	}
	repoDir := filepath.Dir(root)
	p, _, err := loadProject()
	if err != nil {
		return shipTarget{}, err
	}
	if p.Remote.URL == "" || p.Remote.Provider == "" {
		return shipTarget{}, errors.New("project has no [remote].url or .provider — run /dross-options or /dross-onboard")
	}
	ch, err := changes.Load(changes.FilePath(root, phaseID), phaseID)
	if err != nil {
		return shipTarget{}, err
	}
	if ch.PR <= 0 {
		return shipTarget{}, fmt.Errorf("phase %s has no PR recorded in changes.json — open it with `dross ship %[1]s` first", phaseID)
	}
	sha, err := watchedHead(repoDir, phaseID)
	if err != nil {
		return shipTarget{}, err
	}
	hosts, err := remotePolicy(root, repoDir, p)
	if err != nil {
		return shipTarget{}, err
	}
	opts := buildOpenOpts(p, hosts)
	opts.PRNumber = ch.PR
	return shipTarget{phaseID: phaseID, sha: sha, opts: opts}, nil
}

// watchedHead is the commit refs/remotes/origin/phase/<id> names: the head
// `dross ship` last pushed, as this repo last saw origin.
func watchedHead(repoDir, phaseID string) (string, error) {
	ref := "refs/remotes/origin/phase/" + phaseID
	out, err := gitrun.Trim(repoDir, gitRefArgs("rev-parse", []string{"--verify", "--quiet"}, ref+"^{commit}")...)
	if err != nil || out == "" {
		return "", fmt.Errorf("%s does not name a commit — push the phase with `dross ship %s` (or `git fetch origin`), then re-run", ref, phaseID)
	}
	return checkWatchedSHA(ref, out)
}

// checkWatchedSHA admits only a full 40-hex commit id, lower-cased: the value
// goes into a forge request and is pinned as the commit a merge lands, so
// anything else git printed is refused rather than passed along.
func checkWatchedSHA(ref, out string) (string, error) {
	if len(out) != shaHexLen || !isHexRun(out) {
		return "", fmt.Errorf("%s resolved to %q, which is not a %d-hex commit id — refusing to watch it", ref, out, shaHexLen)
	}
	return strings.ToLower(out), nil
}

// ciLine renders one CI read as the single `ci:` line ship.md §5 reads, and
// the exit status that carries the same verdict. A pending with no Reason is
// about CI, so it says whether it is queued or running; a pending with one is
// not (the PR does not carry the watched head yet), so the Reason stands in
// for that and no CI state is claimed. An unknown verdict is an error.
func ciLine(r ship.CIResult) (string, int, error) {
	var code int
	switch r.Verdict {
	case ship.CIPass:
		code = 0
	case ship.CIFail:
		code = exitCIFail
	case ship.CIPending:
		code = exitCIPending
	case ship.CINone:
		code = exitCINone
	case ship.CIAsk:
		code = exitCIAsk
	default:
		return "", 1, fmt.Errorf("unrecognised CI verdict %q", r.Verdict)
	}
	line := "ci: " + string(r.Verdict)
	if r.Verdict == ship.CIPending && r.Reason == "" {
		if r.Queued {
			line += " (queued)"
		} else {
			line += " (running)"
		}
	}
	if r.Verdict == ship.CIFail && r.Link != "" {
		line += " " + r.Link
	}
	if r.Reason != "" {
		line += " - " + r.Reason
	}
	return line, code, nil
}

func shipCI() *cobra.Command {
	return &cobra.Command{
		Use:   "ci <phase-id>",
		Short: "Read the CI verdict on a phase PR's watched head, once (azure-devops, bitbucket)",
		Long: "Reads CI for the PR changes.json records for <phase-id>, at the head " +
			"refs/remotes/origin/phase/<phase-id> names, once — it never waits. Prints one line, " +
			"`ci: <verdict>`: a pending says (queued) or (running), a fail names the failing check's " +
			"link, and a verdict with a reason appends ` - <reason>`.\n\n" +
			"Exit status: 0 pass, 2 fail, 3 pending, 4 none (nothing registered yet), 5 ask " +
			"(a state dross will not read either way), 1 the read itself failed (no verdict).\n\n" +
			"Other providers watch CI through the ship.md recipe instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			tg, err := loadShipTarget(args[0])
			if err != nil {
				return err
			}
			res, err := ship.CIStatusFunc(tg.opts, tg.sha)
			if err != nil {
				return fmt.Errorf("read CI for PR #%d: %w", tg.opts.PRNumber, err)
			}
			line, code, err := ciLine(res)
			if err != nil {
				return fmt.Errorf("read CI for PR #%d: %w", tg.opts.PRNumber, err)
			}
			Printf("%s\n", line)
			if code == 0 {
				return nil
			}
			return &ExitCodeError{Code: code, Err: fmt.Errorf("PR #%d CI at %s: %s", tg.opts.PRNumber, tg.sha, res.Verdict)}
		},
	}
}

func shipMerge() *cobra.Command {
	return &cobra.Command{
		Use:   "merge <phase-id>",
		Short: "Squash-merge a phase PR at its watched head, keeping phase/<id> (azure-devops, bitbucket)",
		Long: "Squash-merges the PR changes.json records for <phase-id>, pinned to the head " +
			"refs/remotes/origin/phase/<phase-id> names, keeps the source branch, never bypasses a " +
			"branch policy, and waits until the forge reports the PR merged.\n\n" +
			"Exit status: 0 merged (prints the PR URL), 2 a branch policy holds it (the phase stays " +
			"at awaiting-merge), 1 any other failure — never read as merged.\n\n" +
			"Other providers merge through the ship.md recipe instead.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			tg, err := loadShipTarget(args[0])
			if err != nil {
				return err
			}
			res, err := ship.MergePRFunc(tg.opts, tg.sha)
			if errors.Is(err, ship.ErrMergeBlocked) {
				return &ExitCodeError{Code: exitMergeBlocked, Err: fmt.Errorf(
					"PR #%d not merged — it holds at awaiting-merge until the policy is satisfied: %w", tg.opts.PRNumber, err)}
			}
			if err != nil {
				return fmt.Errorf("merge PR #%d: %w", tg.opts.PRNumber, err)
			}
			Printf("merged: %s\n", res.URL)
			return nil
		},
	}
}
