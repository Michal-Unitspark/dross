package ship

import (
	"errors"
	"fmt"

	"github.com/Rivil/dross/internal/configenum"
)

// MergeResult is a merge the forge confirmed. URL is the merged PR's page.
type MergeResult struct {
	URL string
}

// ErrMergeBlocked is wrapped when the forge refuses the merge on a policy the
// user configured — required reviewers, comment resolution, work-item linking,
// unmet merge checks. ship holds at awaiting-merge: dross never bypasses one.
var ErrMergeBlocked = errors.New("the merge is blocked by a branch policy")

// ErrMergeUnsupported is returned for a provider dross ship merge does not
// merge on: its ship.md §6 recipe merges instead.
var ErrMergeUnsupported = errors.New("dross ship merge is not supported for this provider — use the ship.md recipe")

// MergePR squash-merges PR opts.PRNumber at the watched head sha, keeping the
// source branch. Azure DevOps and Bitbucket merge; every other ship provider
// gets ErrMergeUnsupported with nothing sent, and an unknown provider a plain
// error.
func MergePR(opts OpenOpts, sha string) (MergeResult, error) {
	switch configenum.Normalize(opts.Provider) {
	case "azure-devops":
		return adoCompletePR(opts, sha)
	case "bitbucket":
		return bitbucketMergePR(opts, sha)
	case "github", "gitlab", "forgejo", "gitea":
		return MergeResult{}, fmt.Errorf("provider %q: %w", opts.Provider, ErrMergeUnsupported)
	default:
		return MergeResult{}, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// MergePRFunc is the exported, overridable seam cmd-package callers use (and
// cmd-package tests stub) to merge without a forge. Production code calls
// MergePRFunc, not MergePR.
var MergePRFunc = MergePR
