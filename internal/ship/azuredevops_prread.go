package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// adoPRListTop is the $top every Azure DevOps PR list asks for. A page this
// full may not be the whole list.
const adoPRListTop = 100

// azureDevOpsPRHead reads PR n's source branch, whether it comes from a fork,
// and whether it is still open. Open is status active; completed and abandoned
// are both closed, and any other status is an error rather than a guess.
func azureDevOpsPRHead(opts OpenOpts, n int) (PRHead, error) {
	if n <= 0 {
		return PRHead{}, fmt.Errorf("PR number %d is not a valid number", n)
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return PRHead{}, err
	}
	rb, _, err := adoRequest(c, "GET", fmt.Sprintf("%s/pullRequests/%d", c.repoAPI(), n), nil)
	if err != nil {
		return PRHead{}, fmt.Errorf("read PR #%d: %w", n, err)
	}
	var pr struct {
		Status        string           `json:"status"`
		SourceRefName string           `json:"sourceRefName"`
		ForkSource    *json.RawMessage `json:"forkSource"`
	}
	if err := json.Unmarshal(rb, &pr); err != nil {
		return PRHead{}, fmt.Errorf("read PR #%d: the response is not the JSON it promises", n)
	}
	var open bool
	switch strings.ToLower(pr.Status) {
	case "active":
		open = true
	case "completed", "abandoned":
	default:
		return PRHead{}, fmt.Errorf("read PR #%d: unrecognised status %q — not reporting it either way", n, pr.Status)
	}
	ref, ok := strings.CutPrefix(pr.SourceRefName, "refs/heads/")
	if !ok || ref == "" {
		return PRHead{}, fmt.Errorf("read PR #%d: source %q is not a branch", n, pr.SourceRefName)
	}
	return PRHead{
		Ref:       ref,
		CrossRepo: adoForked(pr.ForkSource),
		Open:      open,
		URL:       fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, n),
	}, nil
}

// adoForked is a forkSource that is set: an absent key and a JSON null both
// decode to no fork.
func adoForked(fork *json.RawMessage) bool {
	return fork != nil && string(*fork) != "null"
}

// azureDevOpsOpenPRsTargeting lists the active PRs whose target is base. The
// filter runs server-side and is re-checked here, so a server that ignored it
// cannot pass off a PR into another branch, or an abandoned one, as a
// dependent. A fork's PR into base is a dependent too. A failed or untrusted
// read is an error, never an empty list: an empty list authorizes a delete.
func azureDevOpsOpenPRsTargeting(opts OpenOpts, base string) ([]BasePR, error) {
	if base == "" {
		return nil, errors.New("azure devops open-PR lookup needs a base branch")
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return nil, err
	}
	target := adoBranchRef(base)
	q := url.Values{
		"searchCriteria.targetRefName": {target},
		"searchCriteria.status":        {"active"},
		"$top":                         {strconv.Itoa(adoPRListTop)},
	}
	rb, _, err := adoRequest(c, "GET", c.repoAPI()+"/pullrequests?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("list active PRs into %s: %w", base, err)
	}
	var page struct {
		Value *[]struct {
			PullRequestID int    `json:"pullRequestId"`
			Title         string `json:"title"`
			Status        string `json:"status"`
			SourceRefName string `json:"sourceRefName"`
			TargetRefName string `json:"targetRefName"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &page); err != nil || page.Value == nil {
		return nil, fmt.Errorf("list active PRs into %s: the response is not the PR list it promises", base)
	}
	out := []BasePR{}
	for _, pr := range *page.Value {
		if pr.PullRequestID <= 0 {
			return nil, fmt.Errorf("list active PRs into %s: a PR carries no pullRequestId", base)
		}
		if pr.TargetRefName != target || !strings.EqualFold(pr.Status, "active") {
			continue
		}
		out = append(out, BasePR{
			Number:      pr.PullRequestID,
			Title:       pr.Title,
			URL:         fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, pr.PullRequestID),
			HeadRefName: strings.TrimPrefix(pr.SourceRefName, "refs/heads/"),
		})
	}
	// A full page may have cut the dependents off. One found is enough to
	// refuse the delete; none found proves nothing.
	if len(*page.Value) >= adoPRListTop && len(out) == 0 {
		return nil, fmt.Errorf("list active PRs into %s: a full page of %d holds none of them, so the list may be truncated", base, adoPRListTop)
	}
	return out, nil
}
