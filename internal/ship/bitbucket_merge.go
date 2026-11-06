package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Rivil/dross/internal/redact"
)

// --- Merging a Bitbucket Cloud PR ---
//
// dross ship merge squash-merges the PR at the head CI was watched at and
// never closes the source branch: dross phase complete owns teardown. A merge
// Bitbucket cannot finish within its request answers 202 with a task-status
// link, which is polled until it says the PR merged.

// bbMergeInterval, bbMergeWait and bbMergeSleep pace the wait for an
// asynchronous merge. Tests lower them.
var (
	bbMergeInterval = 3 * time.Second
	bbMergeWait     = 90 * time.Second
	bbMergeSleep    = time.Sleep
)

// bitbucketMergePR squash-merges PR opts.PRNumber at sha. A merged PR is done
// with no write; a declined or superseded PR, or one whose head is not sha, is
// refused before any. A merge Bitbucket refuses on its merge checks wraps
// ErrMergeBlocked; anything else that is not a confirmed merge is an error.
func bitbucketMergePR(opts OpenOpts, sha string) (MergeResult, error) {
	n := opts.PRNumber
	if n <= 0 {
		return MergeResult{}, fmt.Errorf("PR number %d is not a valid number", n)
	}
	if !bbCommitShape.MatchString(sha) {
		return MergeResult{}, fmt.Errorf("bitbucket merge: %q is not a commit id", sha)
	}
	user, token, err := bbCredentials(opts.APIBase, opts.AuthEnv, opts.AuthUser, opts.Hosts)
	if err != nil {
		return MergeResult{}, err
	}
	pull, err := bbPullBase(opts, n)
	if err != nil {
		return MergeResult{}, err
	}

	type pr struct {
		State  string  `json:"state"`
		Links  bbLinks `json:"links"`
		Source struct {
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
		} `json:"source"`
	}
	before, err := bbGetJSON[pr](opts, user, token, pull)
	if err != nil {
		return MergeResult{}, fmt.Errorf("read PR #%d: %w", n, err)
	}
	result := MergeResult{URL: before.Links.HTML.Href}
	switch before.State {
	case "MERGED":
		return result, nil
	case "OPEN":
	case "DECLINED", "SUPERSEDED":
		return MergeResult{}, fmt.Errorf("PR #%d is %s — there is nothing to merge", n, strings.ToLower(before.State))
	default:
		return MergeResult{}, fmt.Errorf("read PR #%d: unrecognised state %q — not merging it", n, before.State)
	}
	// Bitbucket abbreviates the head: it is the watched one when it prefixes it.
	hash := strings.ToLower(before.Source.Commit.Hash)
	if hash == "" || !strings.HasPrefix(strings.ToLower(sha), hash) {
		return MergeResult{}, fmt.Errorf("PR #%d's head is %q, not the watched %s — run dross ship ci again", n, before.Source.Commit.Hash, sha)
	}

	endpoint := pull + "/merge"
	var hdr http.Header
	body, status, err := bbRequest("POST", endpoint, opts.AuthEnv, user, token, map[string]any{
		"type":                "pullrequest",
		"merge_strategy":      "squash",
		"close_source_branch": false,
	}, &hdr)
	if err != nil {
		return MergeResult{}, fmt.Errorf("merge PR #%d: %w", n, err)
	}
	switch {
	case status == http.StatusOK:
		var merged pr
		if json.Unmarshal(body, &merged) != nil || merged.State != "MERGED" {
			return MergeResult{}, fmt.Errorf("merge PR #%d: Bitbucket answered 200 but the PR is not merged", n)
		}
		return result, nil
	case status == http.StatusAccepted:
		poll, err := bbTaskStatusURL(opts.APIBase, endpoint, redact.Scrub(hdr.Get("Location"), opts.AuthEnv, token))
		if err != nil {
			return MergeResult{}, fmt.Errorf("merge PR #%d: %w", n, err)
		}
		if err := bbAwaitMerge(opts, user, token, n, poll); err != nil {
			return MergeResult{}, err
		}
		return result, nil
	case status == http.StatusBadRequest && bbMergeCheckRefusal(body):
		return MergeResult{}, fmt.Errorf("merge PR #%d: %w — %s", n, ErrMergeBlocked, string(body))
	}
	return MergeResult{}, fmt.Errorf("merge PR #%d: HTTP %d: %s", n, status, string(body))
}

// bbMergeCheckRefusal is a 400 Bitbucket gives when the PR's merge checks —
// required approvals, passing builds, resolved tasks — are not met.
func bbMergeCheckRefusal(body []byte) bool {
	low := strings.ToLower(string(body))
	return strings.Contains(low, "merge check") || strings.Contains(low, "approv")
}

// bbTaskStatusURL vets a 202's Location before the credential goes to it: it
// must be on api_base's scheme and host, and under this merge's own
// .../merge/task-status/ path — compared without case, since Bitbucket may
// spell the workspace or slug differently from the remote. Anything else is
// refused unrequested. location arrives scrubbed: bbRequest scrubs bodies, not
// headers, and it is quoted in the refusal.
func bbTaskStatusURL(apiBase, endpoint, location string) (string, error) {
	if location == "" {
		return "", errors.New("Bitbucket accepted the merge but gave no task-status link")
	}
	base, err := url.Parse(apiBase)
	if err != nil {
		return "", err
	}
	from, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	loc, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("the task-status link %q does not parse", location)
	}
	loc = from.ResolveReference(loc)
	prefix := from.Path + "/task-status/"
	switch {
	case loc.Scheme != base.Scheme || !strings.EqualFold(loc.Host, base.Host):
		return "", fmt.Errorf("the task-status link %q is not on %s://%s — not following it", location, base.Scheme, base.Host)
	case len(loc.Path) <= len(prefix) || !strings.EqualFold(loc.Path[:len(prefix)], prefix) || path.Clean(loc.Path) != loc.Path:
		return "", fmt.Errorf("the task-status link %q is not this merge's task — not following it", location)
	}
	return loc.String(), nil
}

// bbAwaitMerge polls an asynchronous merge's task status until it reports the
// PR merged, bounded by bbMergeWait.
func bbAwaitMerge(opts OpenOpts, user, token string, n int, poll string) error {
	type task struct {
		TaskStatus  string `json:"task_status"`
		MergeResult *struct {
			State string `json:"state"`
		} `json:"merge_result"`
	}
	for waited := time.Duration(0); waited < bbMergeWait; waited += bbMergeInterval {
		bbMergeSleep(bbMergeInterval)
		t, err := bbGetJSON[task](opts, user, token, poll)
		if err != nil {
			return fmt.Errorf("merge PR #%d: read its task status: %w", n, err)
		}
		switch t.TaskStatus {
		case "PENDING":
			continue
		case "SUCCESS":
			if t.MergeResult == nil || t.MergeResult.State != "MERGED" {
				return fmt.Errorf("merge PR #%d: the merge task finished but the PR is not merged", n)
			}
			return nil
		default:
			return fmt.Errorf("merge PR #%d: the merge task reports %q", n, t.TaskStatus)
		}
	}
	return fmt.Errorf("merge PR #%d: merge not confirmed after %s — re-run dross ship merge", n, bbMergeWait)
}
