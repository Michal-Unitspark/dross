package ship

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
	"github.com/Rivil/dross/internal/hostallow"
)

// CommentOpts is the input shape for posting a comment to an open PR.
// Mirrors OpenOpts where it overlaps so callers configure them
// identically.
type CommentOpts struct {
	Provider   string // one of configenum.ShipProviders
	URL        string // canonical https URL of the repo
	APIBase    string // forgejo/gitea/gitlab/bitbucket/azure-devops: REST API base; ignored for github
	AuthEnv    string // env var holding the token; only forgejo/gitea/gitlab/bitbucket/azure-devops
	AuthUser   string // bitbucket: account user for HTTP Basic auth (user:token)
	AuthScheme string // gitlab: "private-token" (default) | "bearer"; azure-devops: "bearer" or Basic :PAT
	ProjectID  string // gitlab: numeric project-id override; empty = derive from URL
	APIVersion string // azure-devops: api-version sent on every call; empty = 7.1
	PRNumber   int    // PR / MR / issue number to comment on (gitlab: MR iid)
	Body       string // comment body, markdown

	// Hosts is the API host allowlist APIBase is checked against before the
	// token is read. Zero value = SaaS defaults, never unrestricted.
	Hosts hostallow.Policy
}

// PostComment dispatches to the right provider and posts a single
// comment to an existing PR. Used by /dross-review to publish the
// aggregated subagent panel findings as one consolidated comment.
func PostComment(opts CommentOpts) error {
	if opts.Body == "" {
		return errors.New("comment body is empty")
	}
	if opts.PRNumber <= 0 {
		return errors.New("PRNumber must be set")
	}
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return postGitHubComment(opts)
	case "forgejo", "gitea":
		return postForgejoComment(opts)
	case "gitlab":
		return postGitLabComment(opts)
	case "bitbucket":
		return postBitbucketComment(opts)
	default:
		return fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

func postGitHubComment(opts CommentOpts) error {
	// Repeated here rather than left to PostComment's dispatch check: this
	// function is one refactor away from being reachable without it, and the
	// guard is what keeps a derived number out of the argv entirely.
	if opts.PRNumber <= 0 {
		return fmt.Errorf("gh pr comment: PR number %d is not a valid number", opts.PRNumber)
	}
	// --body stays AHEAD of the separator; the number goes behind it. Demoting
	// --body past `--` would not crash — cobra would read the body text as a
	// second positional and the comment would post with the wrong content,
	// which is the worse failure of the two.
	cmd, err := screenedGH("pr", "comment", "--body", opts.Body, "--", fmt.Sprint(opts.PRNumber))
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Surface the missing-gh case with the original install pointer
		// rather than the raw exec error — but key it on the LOOKUP having
		// failed, not on gh being absent from PATH at the moment some other
		// failure happened.
		//
		// This used to call exec.LookPath for every error, which cost two
		// things. In production it reported any failure as "gh is not
		// installed" whenever gh happened to be missing, hiding the real one.
		// And it silently overrode the ghCommand seam a caller had stubbed, so
		// the test asserting that gh's own output is surfaced passed on a
		// machine with gh and reddened on one without — which is exactly how
		// this was found.
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("github backend needs the `gh` CLI on PATH (https://cli.github.com)")
		}
		return ghFailed("gh pr comment", err, out)
	}
	return nil
}

func postForgejoComment(opts CommentOpts) error {
	if opts.APIBase == "" {
		return errors.New("forgejo backend needs APIBase (set [remote].api_base)")
	}
	if opts.AuthEnv == "" {
		return errors.New("forgejo backend needs AuthEnv (set [remote].auth_env)")
	}
	token, err := resolveToken(opts.APIBase, opts.AuthEnv, opts.Hosts)
	if err != nil {
		return err
	}
	owner, repo, err := splitOwnerRepo(opts.URL)
	if err != nil {
		return err
	}
	// Forgejo / Gitea reuses the issues endpoint for PR comments; the
	// number space for issues and PRs is shared.
	endpoint := strings.TrimRight(opts.APIBase, "/") +
		fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, opts.PRNumber)
	if _, err := jsonPost(endpoint, opts.AuthEnv, token, map[string]any{
		"body": opts.Body,
	}); err != nil {
		return fmt.Errorf("post comment: %w", err)
	}
	return nil
}

func postGitLabComment(opts CommentOpts) error {
	if opts.APIBase == "" {
		return errors.New("gitlab backend needs APIBase (set [remote].api_base)")
	}
	if opts.AuthEnv == "" {
		return errors.New("gitlab backend needs AuthEnv (set [remote].auth_env)")
	}
	token, err := resolveToken(opts.APIBase, opts.AuthEnv, opts.Hosts)
	if err != nil {
		return err
	}
	owner, repo, err := splitOwnerRepo(opts.URL)
	if err != nil {
		return err
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(opts.ProjectID))
	ref := gitlabProjectRef(owner, repo, pid)
	// GitLab MR comments are "notes" on the merge request; PRNumber is the iid.
	endpoint := strings.TrimRight(opts.APIBase, "/") +
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", ref, opts.PRNumber)
	rb, status, err := gitlabReq("POST", endpoint, opts.AuthEnv, opts.AuthScheme, token, map[string]any{"body": opts.Body})
	if err != nil {
		return fmt.Errorf("post note: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("post note: HTTP %d: %s", status, string(rb))
	}
	return nil
}

// postBitbucketComment posts a PR comment on Bitbucket Cloud.
//
// Two shapes differ from every other backend here: the body nests under
// content.raw rather than being a flat "body", and PR comments live under the
// pullrequests endpoint — Bitbucket has no shared issue/PR number space to
// reuse the way Forgejo does.
func postBitbucketComment(opts CommentOpts) error {
	user, token, err := bbCredentials(opts.APIBase, opts.AuthEnv, opts.AuthUser, opts.Hosts)
	if err != nil {
		return err
	}
	workspace, slug, err := bbRepoRef(opts.URL)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(opts.APIBase, "/") +
		fmt.Sprintf("/repositories/%s/%s/pullrequests/%d/comments", workspace, slug, opts.PRNumber)
	rb, status, err := bbRequest("POST", endpoint, opts.AuthEnv, user, token, map[string]any{
		"content": map[string]any{"raw": opts.Body},
	})
	if err != nil {
		return fmt.Errorf("post comment: %w", err)
	}
	if status >= 300 {
		return fmt.Errorf("post comment: HTTP %d: %s", status, string(rb))
	}
	return nil
}
