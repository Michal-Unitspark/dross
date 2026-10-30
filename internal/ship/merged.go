package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
)

// ErrMergeStatusUnsupported is returned by GetPRStatus for providers without a
// wired authoritative status lookup. It is a forward seam, not a live gap:
// every provider in configenum.ShipProviders answers PRStatus authoritatively
// today, so production dispatch can never reach this sentinel. It stays wired
// for a future provider added before its PRStatus lookup lands — callers treat
// it as "the provider can't answer" and fall back to a git-ancestry check
// rather than blocking.
var ErrMergeStatusUnsupported = errors.New("PR merged-status lookup is not supported for this provider")

// PRStatus is a PR/MR's authoritative merged state and current base branch, as
// reported by the provider. BaseRef lets callers detect a retarget: a base
// recorded when the PR was opened that no longer matches what the provider
// reports now.
type PRStatus struct {
	Merged  bool
	BaseRef string
}

// GetPRStatus reports the merged state and current base ref of the PR/MR
// identified by opts.PRNumber, using each provider's authoritative source. It
// mirrors OpenPR's provider dispatch.
func GetPRStatus(opts OpenOpts) (PRStatus, error) {
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubPRStatus(opts)
	case "bitbucket":
		return bitbucketPRStatus(opts)
	case "gitlab":
		return gitlabPRStatus(opts)
	case "forgejo", "gitea":
		return forgejoPRStatus(opts)
	case "azure-devops":
		return azureDevOpsPRStatus(opts)
	default:
		return PRStatus{}, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// PRStatusFunc is the exported, overridable seam that cmd-package callers use
// (and that cmd-package tests stub) to check PR status without a `gh` binary
// or network — the unexported ghCommand seam is unreachable from package cmd.
// Production code calls PRStatusFunc, not GetPRStatus directly.
var PRStatusFunc = GetPRStatus

// gitHubPRStatus asks `gh pr view <n> --json state,mergedAt,baseRefName` and
// reports merged == (state == "MERGED") plus the PR's current base branch.
// Any lookup or parse failure is returned so the caller can fall back to git
// ancestry rather than trust a stale signal.
func gitHubPRStatus(opts OpenOpts) (PRStatus, error) {
	if opts.PRNumber <= 0 {
		return PRStatus{}, errors.New("github merged-status lookup needs a PR number")
	}
	// The PR number goes behind `--`, and the flags stay AHEAD of it.
	//
	// Both halves matter and the second is the counter-intuitive one: gh is
	// cobra, and cobra's `--` ends flag parsing entirely rather than fencing
	// only the token after it. `pr view -- 12 --json state` would make --json
	// and its value the 2nd and 3rd positionals, and `gh pr view` takes at most
	// one — so it exits "accepts at most 1 arg(s), received 3". Because
	// ghCommand is a test double, that shape passes its unit test while ship's
	// merge-status path is dead against the real binary.
	cmd, err := screenedGH("pr", "view", "--json", "state,mergedAt,baseRefName", "--", strconv.Itoa(opts.PRNumber))
	if err != nil {
		return PRStatus{}, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return PRStatus{}, ghFailed(fmt.Sprintf("gh pr view #%d", opts.PRNumber), err, out)
	}
	var view struct {
		State       string `json:"state"`
		MergedAt    string `json:"mergedAt"`
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal(out, &view); err != nil {
		return PRStatus{}, ghUnparseable(fmt.Sprintf("parse gh pr view #%d", opts.PRNumber), out)
	}
	//dross:taint-cleared baseRefName is the branch name the forge records for the PR, decoded from gh's --json state,mergedAt,baseRefName
	return PRStatus{Merged: strings.EqualFold(view.State, "MERGED"), BaseRef: view.BaseRefName}, nil
}
