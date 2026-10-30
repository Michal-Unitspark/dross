package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// --- Completing an Azure Repos PR ---
//
// dross ship merge completes the PR as a squash pinned to the head CI was
// watched at, never deleting the source branch (dross phase complete owns
// teardown) and never bypassing a policy or arming auto-complete (the
// blocked_completion lock). Azure DevOps completes asynchronously: the PATCH
// is accepted, and the PR reports completed once the merge has landed.

// adoMergeInterval, adoMergeWait and adoMergeSleep pace the wait for a
// completion to land. Tests lower them.
var (
	adoMergeInterval = 3 * time.Second
	adoMergeWait     = 90 * time.Second
	adoMergeSleep    = time.Sleep
)

// adoPullRequest is the part of a PR the merge and CI reads use. Head is
// lastMergeSourceCommit — the commit the PR would merge — or "" when it names
// none.
type adoPullRequest struct {
	Status        string
	IsDraft       bool
	MergeStatus   string
	SourceRefName string
	Head          string
	RepoID        string
	ProjectID     string
}

// adoReadPR reads PR n.
func adoReadPR(c adoConn, n int) (adoPullRequest, error) {
	rb, _, err := adoRequest(c, "GET", fmt.Sprintf("%s/pullRequests/%d", c.repoAPI(), n), nil)
	if err != nil {
		return adoPullRequest{}, fmt.Errorf("read PR #%d: %w", n, err)
	}
	var pr struct {
		Status                string `json:"status"`
		IsDraft               bool   `json:"isDraft"`
		MergeStatus           string `json:"mergeStatus"`
		SourceRefName         string `json:"sourceRefName"`
		LastMergeSourceCommit *struct {
			CommitID string `json:"commitId"`
		} `json:"lastMergeSourceCommit"`
		Repository struct {
			ID      string `json:"id"`
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(rb, &pr); err != nil {
		return adoPullRequest{}, fmt.Errorf("read PR #%d: the response is not the JSON it promises", n)
	}
	out := adoPullRequest{
		Status:        pr.Status,
		IsDraft:       pr.IsDraft,
		MergeStatus:   pr.MergeStatus,
		SourceRefName: pr.SourceRefName,
		RepoID:        pr.Repository.ID,
		ProjectID:     pr.Repository.Project.ID,
	}
	if pr.LastMergeSourceCommit != nil {
		out.Head = pr.LastMergeSourceCommit.CommitID
	}
	return out, nil
}

// adoPolicyEvaluations reads PR n's policy evaluations — the per-PR results,
// never the branch's policy configuration. The artifact id names the project
// by GUID; a project name matches nothing. A build evaluation links its build,
// any other the PR.
func adoPolicyEvaluations(c adoConn, projectID string, n int, prURL string) ([]adoEvalObs, error) {
	if projectID == "" {
		return nil, fmt.Errorf("read PR #%d policies: the PR names no project id", n)
	}
	project := url.PathEscape(c.repo.Project)
	q := url.Values{"artifactId": {fmt.Sprintf("vstfs:///CodeReview/CodeReviewId/%s/%d", projectID, n)}}
	rb, _, err := adoRequest(c.withPreview(), "GET", c.apiBase+"/"+project+"/_apis/policy/evaluations?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("read PR #%d policies: %w", n, err)
	}
	var page struct {
		Value *[]struct {
			Status        string `json:"status"`
			Configuration struct {
				IsBlocking bool `json:"isBlocking"`
				Type       struct {
					ID          string `json:"id"`
					DisplayName string `json:"displayName"`
				} `json:"type"`
			} `json:"configuration"`
			Context struct {
				BuildID   int  `json:"buildId"`
				IsExpired bool `json:"isExpired"`
			} `json:"context"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &page); err != nil || page.Value == nil {
		return nil, fmt.Errorf("read PR #%d policies: the response is not the evaluation list it promises", n)
	}
	out := make([]adoEvalObs, 0, len(*page.Value))
	for _, e := range *page.Value {
		link := prURL
		if e.Context.BuildID > 0 {
			link = fmt.Sprintf("%s/%s/_build/results?buildId=%d", c.apiBase, project, e.Context.BuildID)
		}
		out = append(out, adoEvalObs{
			TypeID:   e.Configuration.Type.ID,
			Name:     e.Configuration.Type.DisplayName,
			Status:   e.Status,
			Blocking: e.Configuration.IsBlocking,
			Expired:  e.Context.IsExpired,
			Link:     link,
		})
	}
	return out, nil
}

// adoCompletePR completes PR opts.PRNumber as a squash at sha and waits until
// the PR reports completed. A completed PR is merged with no write. An
// abandoned or draft PR, or one whose head is not sha, is refused before any
// write. A merge that conflicts, fails or is still unconfirmed at the bound is
// an error; one a non-CI policy holds wraps ErrMergeBlocked.
func adoCompletePR(opts OpenOpts, sha string) (MergeResult, error) {
	n := opts.PRNumber
	if n <= 0 {
		return MergeResult{}, fmt.Errorf("PR number %d is not a valid number", n)
	}
	if sha == "" {
		return MergeResult{}, errors.New("azure devops merge needs the watched head commit")
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return MergeResult{}, err
	}
	prURL := fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, n)
	pr, err := adoReadPR(c, n)
	if err != nil {
		return MergeResult{}, err
	}
	switch strings.ToLower(pr.Status) {
	case "completed":
		return MergeResult{URL: prURL}, nil
	case "abandoned":
		return MergeResult{}, fmt.Errorf("PR #%d is abandoned — there is nothing to merge", n)
	case "active":
	default:
		return MergeResult{}, fmt.Errorf("read PR #%d: unrecognised status %q — not merging it", n, pr.Status)
	}
	if pr.IsDraft {
		return MergeResult{}, fmt.Errorf("PR #%d is a draft — publish it before merging", n)
	}
	if !strings.EqualFold(pr.Head, sha) {
		return MergeResult{}, fmt.Errorf("PR #%d would merge %q, not the watched head %s — run dross ship ci again", n, pr.Head, sha)
	}
	projectID := pr.ProjectID
	if projectID == "" {
		return MergeResult{}, fmt.Errorf("read PR #%d: the PR names no project id", n)
	}

	body := map[string]any{
		"status":                "completed",
		"lastMergeSourceCommit": map[string]any{"commitId": sha},
		"completionOptions": map[string]any{
			"mergeStrategy":       "squash",
			"squashMerge":         true,
			"deleteSourceBranch":  false,
			"transitionWorkItems": false,
		},
	}
	if _, status, err := adoRequest(c, "PATCH", fmt.Sprintf("%s/pullrequests/%d", c.repoAPI(), n), body); err != nil {
		if status == 400 || status == 409 {
			return MergeResult{}, adoCompletionRefused(c, projectID, n, prURL, err)
		}
		return MergeResult{}, fmt.Errorf("complete PR #%d: %w", n, err)
	}

	for waited := time.Duration(0); waited < adoMergeWait; waited += adoMergeInterval {
		adoMergeSleep(adoMergeInterval)
		now, err := adoReadPR(c, n)
		if err != nil {
			return MergeResult{}, err
		}
		switch strings.ToLower(now.Status) {
		case "completed":
			return MergeResult{URL: prURL}, nil
		case "abandoned":
			return MergeResult{}, fmt.Errorf("PR #%d was abandoned while it completed", n)
		}
		// While the PR is still active, mergeStatus says how the merge went:
		// queued and succeeded are on their way.
		switch strings.ToLower(now.MergeStatus) {
		case "conflicts":
			return MergeResult{}, fmt.Errorf("complete PR #%d: the merge has conflicts — resolve them on %s and run dross ship ci again",
				n, strings.TrimPrefix(now.SourceRefName, "refs/heads/"))
		case "failure":
			return MergeResult{}, fmt.Errorf("complete PR #%d: Azure DevOps reports the merge failed", n)
		case "rejectedbypolicy":
			return MergeResult{}, fmt.Errorf("complete PR #%d: %w (mergeStatus rejectedByPolicy)", n, ErrMergeBlocked)
		}
	}
	return MergeResult{}, adoCompletionRefused(c, projectID, n, prURL,
		errors.New("completion not confirmed — re-run dross ship merge"))
}

// adoCompletionRefused explains a completion that was refused or never
// landed. A blocking non-CI policy that has not approved is the block, by
// name; anything else is cause — never a block it cannot name.
func adoCompletionRefused(c adoConn, projectID string, n int, prURL string, cause error) error {
	evals, err := adoPolicyEvaluations(c, projectID, n, prURL)
	if err != nil {
		return fmt.Errorf("complete PR #%d: %v — and its policies could not be read: %w", n, cause, err)
	}
	var held []string
	for _, e := range evals {
		kind, name := adoPolicyClass(e.TypeID)
		switch strings.ToLower(e.Status) {
		case "approved", "notapplicable":
			continue
		}
		if kind == adoPolicyBlocker && e.Blocking && !slices.Contains(held, name) {
			held = append(held, name)
		}
	}
	if len(held) > 0 {
		// Azure DevOps' own answer rides along: it may name a reason the
		// policies do not.
		return fmt.Errorf("complete PR #%d: %w — %s not approved (%v)", n, ErrMergeBlocked, strings.Join(held, ", "), cause)
	}
	return fmt.Errorf("complete PR #%d: %w", n, cause)
}
