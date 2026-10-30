package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// azureDevOpsPRStatus reads the PR's authoritative status and current target
// branch from Azure Repos.
//
// The status is active, abandoned or completed — so merged is status ==
// completed, never status != active. An abandoned PR is closed without ever
// having landed, and reporting it merged would false-complete a phase whose
// work was thrown away. Any other status, or none, is an error rather than a
// guess.
func azureDevOpsPRStatus(opts OpenOpts) (PRStatus, error) {
	if opts.PRNumber <= 0 {
		return PRStatus{}, errors.New("azure devops merged-status lookup needs a PR number")
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return PRStatus{}, err
	}
	rb, _, err := adoRequest(c, "GET", fmt.Sprintf("%s/pullRequests/%d", c.repoAPI(), opts.PRNumber), nil)
	if err != nil {
		return PRStatus{}, fmt.Errorf("read PR #%d: %w", opts.PRNumber, err)
	}
	var pr struct {
		Status        string `json:"status"`
		TargetRefName string `json:"targetRefName"`
	}
	if err := json.Unmarshal(rb, &pr); err != nil {
		return PRStatus{}, fmt.Errorf("read PR #%d: the response is not the JSON it promises", opts.PRNumber)
	}
	switch strings.ToLower(pr.Status) {
	case "active", "abandoned", "completed":
	default:
		return PRStatus{}, fmt.Errorf("read PR #%d: unrecognised status %q — not reporting it either way", opts.PRNumber, pr.Status)
	}
	if pr.TargetRefName == "" {
		return PRStatus{}, fmt.Errorf("read PR #%d: the response carries no targetRefName", opts.PRNumber)
	}
	return PRStatus{
		Merged:  strings.EqualFold(pr.Status, "completed"),
		BaseRef: strings.TrimPrefix(pr.TargetRefName, "refs/heads/"),
	}, nil
}

// adoOpenPRBySource returns the active PR whose source branch is head, or
// (nil, nil) when there is none.
//
// The filter runs server-side, and every hit is re-checked here — source ref,
// active status, not from a fork — so a server that ignored the filter can
// never pass off another branch's PR, an abandoned one, or a fork's
// same-named branch as this phase's. A response without a value list is an
// error: (nil, nil) would authorize exactly the duplicate this lookup prevents.
func adoOpenPRBySource(opts OpenOpts, head string) (*OpenResult, error) {
	if head == "" {
		return nil, errors.New("azure devops open-PR lookup needs a head branch")
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return nil, err
	}
	ref := adoBranchRef(head)
	q := url.Values{"searchCriteria.sourceRefName": {ref}, "searchCriteria.status": {"active"}}
	rb, _, err := adoRequest(c, "GET", c.repoAPI()+"/pullrequests?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("list active PRs from %s: %w", head, err)
	}
	var page struct {
		Value *[]struct {
			PullRequestID int       `json:"pullRequestId"`
			Status        string    `json:"status"`
			SourceRefName string    `json:"sourceRefName"`
			ForkSource    *struct{} `json:"forkSource"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &page); err != nil || page.Value == nil {
		return nil, fmt.Errorf("list active PRs from %s: the response is not the PR list it promises", head)
	}
	for _, pr := range *page.Value {
		if pr.SourceRefName != ref || !strings.EqualFold(pr.Status, "active") || pr.ForkSource != nil {
			continue
		}
		// This phase's PR, but no number to record: skipping it would read as
		// "none" and open the duplicate.
		if pr.PullRequestID <= 0 {
			return nil, fmt.Errorf("list active PRs from %s: a matching PR carries no pullRequestId", head)
		}
		return &OpenResult{
			Number: pr.PullRequestID,
			URL:    fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, pr.PullRequestID),
		}, nil
	}
	return nil, nil
}
