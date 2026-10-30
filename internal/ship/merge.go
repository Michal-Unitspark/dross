package ship

import "errors"

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
