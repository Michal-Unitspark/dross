package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// adoBuildListTop is the $top the builds read asks for. A page this full may
// have cut off a build at the head, and is refused rather than read.
const adoBuildListTop = 100

// adoCIStatus reads CI for PR opts.PRNumber at the watched head sha. A PR that
// does not carry sha yet is pending with a Reason, with no CI read: its CI is
// about another commit. Otherwise the verdict is adoCIForPR's.
func adoCIStatus(opts OpenOpts, sha string) (CIResult, error) {
	n := opts.PRNumber
	if n <= 0 {
		return CIResult{}, fmt.Errorf("PR number %d is not a valid number", n)
	}
	if sha == "" {
		return CIResult{}, errors.New("azure devops CI read needs the watched head commit")
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return CIResult{}, err
	}
	pr, err := adoReadPR(c, n)
	if err != nil {
		return CIResult{}, err
	}
	if pr.RepoID == "" || pr.ProjectID == "" {
		return CIResult{}, fmt.Errorf("read PR #%d: the PR names no repository or project id", n)
	}
	if !strings.EqualFold(pr.Head, sha) {
		head := pr.Head
		if head == "" {
			head = "(none)"
		}
		branch := strings.TrimPrefix(pr.SourceRefName, "refs/heads/")
		return CIResult{
			Verdict: CIPending,
			Reason:  fmt.Sprintf("PR head %s is not the watched %s — run dross ship %s", head, sha, strings.TrimPrefix(branch, "phase/")),
		}, nil
	}
	return adoCIForPR(c, n, pr)
}

// adoCIForPR reads PR n's CI at its own head (pr.Head): the builds that ran on
// its source branch at that commit, and its build-validation and status-check
// evaluations. Builds are matched by repository and commit, never filtered by
// definition or process, so classic pipelines count as YAML ones do; a
// pipeline run again at the same commit counts once, as its latest run.
// Branch policy configuration is never read.
func adoCIForPR(c adoConn, n int, pr adoPullRequest) (CIResult, error) {
	if pr.RepoID == "" || pr.ProjectID == "" {
		return CIResult{}, fmt.Errorf("read PR #%d: the PR names no repository or project id", n)
	}
	// With no head there is no commit to match builds against, and the
	// evaluations alone could roll up to a pass.
	if pr.Head == "" {
		return CIResult{}, fmt.Errorf("read PR #%d: the PR names no head commit", n)
	}
	project := url.PathEscape(c.repo.Project)
	q := url.Values{
		"repositoryId":   {pr.RepoID},
		"repositoryType": {"TfsGit"},
		"branchName":     {pr.SourceRefName},
		"queryOrder":     {"queueTimeDescending"},
		"$top":           {strconv.Itoa(adoBuildListTop)},
	}
	rb, _, err := adoRequest(c, "GET", c.apiBase+"/"+project+"/_apis/build/builds?"+q.Encode(), nil)
	if err != nil {
		return CIResult{}, fmt.Errorf("read PR #%d builds: %w", n, err)
	}
	var page struct {
		Value *[]struct {
			Status        string `json:"status"`
			Result        string `json:"result"`
			QueueTime     string `json:"queueTime"`
			SourceVersion string `json:"sourceVersion"`
			Definition    struct {
				ID int `json:"id"`
			} `json:"definition"`
			Repository struct {
				ID string `json:"id"`
			} `json:"repository"`
			Links struct {
				Web struct {
					Href string `json:"href"`
				} `json:"web"`
			} `json:"_links"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &page); err != nil || page.Value == nil {
		return CIResult{}, fmt.Errorf("read PR #%d builds: the response is not the build list it promises", n)
	}
	if len(*page.Value) >= adoBuildListTop {
		return CIResult{}, fmt.Errorf("read PR #%d builds: a full page of %d may hide a build at the head", n, adoBuildListTop)
	}
	var builds []adoBuildObs
	var queued []time.Time
	latest := map[int]int{} // definition id -> index in builds
	for _, b := range *page.Value {
		// The server filter is re-checked: an older push's build, or another
		// repository's, says nothing about this head.
		if !strings.EqualFold(b.SourceVersion, pr.Head) || !strings.EqualFold(b.Repository.ID, pr.RepoID) {
			continue
		}
		obs := adoBuildObs{Status: b.Status, Result: b.Result, Link: b.Links.Web.Href}
		at, _ := time.Parse(time.RFC3339Nano, b.QueueTime)
		// A re-run supersedes the failed run it replaces. Queue time decides,
		// not the order the server listed them in.
		if i, ok := latest[b.Definition.ID]; ok && b.Definition.ID > 0 {
			if at.After(queued[i]) {
				builds[i], queued[i] = obs, at
			}
			continue
		}
		latest[b.Definition.ID] = len(builds)
		builds, queued = append(builds, obs), append(queued, at)
	}
	evals, err := adoPolicyEvaluations(c, pr.ProjectID, n, fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, n))
	if err != nil {
		return CIResult{}, err
	}
	return adoCIRollup(builds, evals), nil
}
