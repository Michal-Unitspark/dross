package ship

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// --- Azure DevOps open PRs for dross watch ---
//
// One list read for the repository's active PRs; checks are read only for the
// PRs dross ships — a phase/ or milestone/ branch in this repository — through
// the same adoCIForPR that dross ship ci uses, so the digest and ship ci never
// disagree. Every other PR gets ChecksNone with no CI read: the digest does not
// fan out over PRs it never acts on. ADO gives no bot flag, so IsBot is always
// false rather than guessed from a creator's name.

// azureDevOpsOpenPRs lists the repository's active PRs with their check
// rollups. A failure — the list or any one PR's checks — is (nil, err), never
// a list where an unread PR reads none.
func azureDevOpsOpenPRs(opts OpenOpts) ([]OpenPRRecord, error) {
	c, err := adoOpenConn(opts)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"searchCriteria.status": {"active"},
		"$top":                  {strconv.Itoa(adoPRListTop)},
	}
	rb, _, err := adoRequest(c, "GET", c.repoAPI()+"/pullrequests?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("list active PRs: %w", err)
	}
	var page struct {
		Value *[]struct {
			PullRequestID int    `json:"pullRequestId"`
			Title         string `json:"title"`
			Status        string `json:"status"`
			CreationDate  string `json:"creationDate"`
			SourceRefName string `json:"sourceRefName"`
			CreatedBy     struct {
				UniqueName string `json:"uniqueName"`
			} `json:"createdBy"`
			ForkSource            *json.RawMessage `json:"forkSource"`
			LastMergeSourceCommit *struct {
				CommitID string `json:"commitId"`
			} `json:"lastMergeSourceCommit"`
			Repository struct {
				ID      string `json:"id"`
				Project struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"repository"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &page); err != nil || page.Value == nil {
		return nil, fmt.Errorf("list active PRs: the response is not the PR list it promises")
	}
	if len(*page.Value) >= adoPRListTop {
		return nil, fmt.Errorf("list active PRs: the page is full at the $top of %d, so the list may be truncated", adoPRListTop)
	}
	out := []OpenPRRecord{}
	for _, pr := range *page.Value {
		n := pr.PullRequestID
		if n <= 0 {
			return nil, fmt.Errorf("list active PRs: a PR carries no pullRequestId")
		}
		if !strings.EqualFold(pr.Status, "active") {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, pr.CreationDate)
		if err != nil {
			return nil, fmt.Errorf("list active PRs: PR #%d carries no creationDate", n)
		}
		head, ok := strings.CutPrefix(pr.SourceRefName, "refs/heads/")
		if !ok || head == "" {
			return nil, fmt.Errorf("list active PRs: PR #%d's source %q is not a branch", n, pr.SourceRefName)
		}
		rec := OpenPRRecord{
			Number:            n,
			Title:             pr.Title,
			URL:               fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, n),
			Author:            PRAuthor{Login: pr.CreatedBy.UniqueName},
			CreatedAt:         created,
			HeadRefName:       head,
			IsCrossRepository: adoForked(pr.ForkSource),
			Checks:            ChecksNone,
		}
		if !rec.IsCrossRepository && (strings.HasPrefix(head, "phase/") || strings.HasPrefix(head, "milestone/")) {
			read := adoPullRequest{
				Status:        pr.Status,
				SourceRefName: pr.SourceRefName,
				RepoID:        pr.Repository.ID,
				ProjectID:     pr.Repository.Project.ID,
			}
			if pr.LastMergeSourceCommit != nil {
				read.Head = pr.LastMergeSourceCommit.CommitID
			}
			ci, err := adoCIForPR(c, n, read)
			if err != nil {
				return nil, fmt.Errorf("read PR #%d's checks: %w", n, err)
			}
			rec.Checks = adoWatchChecks(ci.Verdict)
		}
		out = append(out, rec)
	}
	return out, nil
}

// adoWatchChecks maps a ship ci verdict onto the digest's rollup. An ask is
// pending: only a pass is ever passing.
func adoWatchChecks(v CIVerdict) CheckRollup {
	switch v {
	case CIPass:
		return ChecksPassing
	case CIFail:
		return ChecksFailing
	case CINone:
		return ChecksNone
	default:
		return ChecksPending
	}
}
