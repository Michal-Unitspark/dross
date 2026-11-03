package ship

import (
	"encoding/json"
	"fmt"
	"strings"
)

// --- Arming auto-complete on an Azure Repos chore PR ---
//
// A chore PR — dross's .dross bookkeeping, routed around a protected base —
// is armed to auto-complete with a merge commit (Azure DevOps' noFastForward),
// so every local base fast-forwards to it, exactly as GitHub's armed
// `gh pr merge --auto --merge` chore flow does (locked chore_pr_completion).
// It never bypasses a policy: the branch's policies still decide when, and
// whether, it lands. Phase PRs are never armed — ship-loop's
// blocked_completion lock governs those, and adoCompletePR's body still bans
// autoCompleteSetBy.

// adoAutoCompletePR is the part of a PR the arm reads.
type adoAutoCompletePR struct {
	status        string
	targetRefName string
	armedBy       string // autoCompleteSetBy.id, "" when not armed
}

// adoReadAutoComplete reads PR n's status, target and auto-complete state.
func adoReadAutoComplete(c adoConn, n int) (adoAutoCompletePR, error) {
	rb, _, err := adoRequest(c, "GET", fmt.Sprintf("%s/pullrequests/%d", c.repoAPI(), n), nil)
	if err != nil {
		return adoAutoCompletePR{}, fmt.Errorf("read PR #%d: %w", n, err)
	}
	return parseAutoCompletePR(rb, n)
}

// parseAutoCompletePR decodes a PR answer.
func parseAutoCompletePR(rb []byte, n int) (adoAutoCompletePR, error) {
	var pr struct {
		Status            string `json:"status"`
		TargetRefName     string `json:"targetRefName"`
		AutoCompleteSetBy *struct {
			ID string `json:"id"`
		} `json:"autoCompleteSetBy"`
	}
	if err := json.Unmarshal(rb, &pr); err != nil {
		return adoAutoCompletePR{}, fmt.Errorf("read PR #%d: the response is not the JSON it promises", n)
	}
	out := adoAutoCompletePR{status: strings.ToLower(pr.Status), targetRefName: pr.TargetRefName}
	if pr.AutoCompleteSetBy != nil {
		out.armedBy = strings.TrimSpace(pr.AutoCompleteSetBy.ID)
	}
	return out, nil
}

// adoArmAutoComplete arms auto-complete on chore PR n with a merge commit.
//
// A merge commit the target's "Require a merge strategy" policy forbids, or a
// policy list that cannot be read, is ErrAutoMergeUnavailable before anything
// is written: the PR stays open and the caller says to complete it by hand. So
// is a 400 or 403 on the arm itself — Azure DevOps refusing it. Any other
// failure is a plain error. What the arm achieved is read back from the PR,
// never assumed from a 200.
func adoArmAutoComplete(opts OpenOpts, n int) (AutoMergeResult, error) {
	c, err := adoOpenConn(opts)
	if err != nil {
		return AutoMergeResult{}, err
	}
	pr, err := adoReadAutoComplete(c, n)
	if err != nil {
		return AutoMergeResult{}, err
	}
	switch pr.status {
	case "completed":
		return AutoMergeResult{Merged: true}, nil
	case "abandoned":
		return AutoMergeResult{}, fmt.Errorf("PR #%d is abandoned — there is nothing to complete", n)
	case "active":
	default:
		return AutoMergeResult{}, fmt.Errorf("read PR #%d: unrecognised status %q — not arming it", n, pr.status)
	}
	target := strings.TrimPrefix(pr.targetRefName, "refs/heads/")
	if target == "" {
		return AutoMergeResult{}, fmt.Errorf("read PR #%d: the PR names no target branch", n)
	}

	allowed, known, reason := adoMergeCommitAllowed(c, target)
	if !known {
		return AutoMergeResult{}, fmt.Errorf("%w: %s's merge-strategy policies could not be read (%s)", ErrAutoMergeUnavailable, target, reason)
	}
	if !allowed {
		return AutoMergeResult{}, fmt.Errorf("%w: a blocking \"Require a merge strategy\" policy on %s does not allow a merge commit", ErrAutoMergeUnavailable, target)
	}

	me, err := azureDevOpsAuthenticatedUser(opts)
	if err != nil {
		return AutoMergeResult{}, fmt.Errorf("arm auto-complete on PR #%d: %w", n, err)
	}
	body := map[string]any{
		"autoCompleteSetBy": map[string]any{"id": me.ID},
		"completionOptions": map[string]any{
			"mergeStrategy":       "noFastForward",
			"bypassPolicy":        false,
			"deleteSourceBranch":  false,
			"transitionWorkItems": false,
		},
	}
	if _, status, err := adoRequest(c, "PATCH", fmt.Sprintf("%s/pullrequests/%d", c.repoAPI(), n), body); err != nil {
		if status == 400 || status == 403 {
			return AutoMergeResult{}, fmt.Errorf("%w: Azure DevOps refused to arm auto-complete on PR #%d (%v)", ErrAutoMergeUnavailable, n, err)
		}
		return AutoMergeResult{}, fmt.Errorf("arm auto-complete on PR #%d: %w", n, err)
	}
	// The PATCH's own answer is not the outcome — a 200 can come back without
	// autoCompleteSetBy — so the PR is read back.
	now, err := adoReadAutoComplete(c, n)
	if err != nil {
		return AutoMergeResult{}, err
	}
	switch {
	case now.status == "completed":
		return AutoMergeResult{Merged: true}, nil
	case now.status == "active" && now.armedBy != "":
		return AutoMergeResult{AutoEnabled: true}, nil
	case now.status == "active":
		// Accepted, yet not armed: auto-complete could not be set, so the PR
		// stays open for a human to complete (locked chore_pr_completion).
		return AutoMergeResult{}, fmt.Errorf("%w: Azure DevOps accepted the arm on PR #%d, but the PR reads back without auto-complete set", ErrAutoMergeUnavailable, n)
	}
	return AutoMergeResult{}, fmt.Errorf("arm auto-complete on PR #%d: the PR is %s after the arm", n, now.status)
}
