package ship

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Rivil/dross/internal/hostallow"
	"github.com/Rivil/dross/internal/redact"
	"github.com/Rivil/dross/internal/secretscan"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Bitbucket Cloud transport.
//
// Bitbucket is the odd one out on both axes this file has to bridge. Its
// credential is HTTP Basic user:token — neither Forgejo's "Authorization: token"
// nor GitLab's PRIVATE-TOKEN — which is why [remote].auth_user exists and why
// every entry point here checks it before opening a socket. And its PR payload
// nests the branch names (source.branch.name) where every other backend keeps
// them flat, so a GitHub-shaped body silently creates the wrong PR.

// bbBasicAuth sets Bitbucket's credential header and nothing else. Exactly one
// auth header is set: a stray PRIVATE-TOKEN or "Authorization: token" alongside
// this one is what a copy-paste from the GitLab/Forgejo paths would produce.
func bbBasicAuth(req *http.Request, user, token string) {
	cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	req.Header.Set("Authorization", "Basic "+cred)
}

// bbRepoRef splits a canonical https://bitbucket.org/workspace/slug URL into its
// workspace and repo-slug path segments, which every Bitbucket endpoint is keyed
// by. Bitbucket calls them workspace/repo_slug; they occupy the same two URL
// positions as owner/repo elsewhere.
func bbRepoRef(repoURL string) (workspace, slug string, err error) {
	workspace, slug, err = splitOwnerRepo(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("bitbucket: %w", err)
	}
	return workspace, slug, nil
}

// bbRequest performs a Bitbucket REST request with Basic auth, returning the raw
// body and status. body is JSON-encoded when non-nil. Endpoints are relative to
// api_base, which project.DetectRemote autodetects as
// https://api.bitbucket.org/2.0 for a bitbucket.org remote. A caller that needs
// the response headers — an async merge's Location — passes hdr, and it is
// filled once the response arrives.
func bbRequest(method, endpoint, authEnv, user, token string, body any, hdr ...*http.Header) ([]byte, int, error) {
	// Screened before the encoder runs (criterion c-2 of secret-detection).
	// A nil body — every GET here — passes untouched.
	if err := secretscan.ScanPayload(method+" "+endpoint, body); err != nil {
		return nil, 0, err
	}
	var buf io.Reader
	if body != nil {
		b := new(bytes.Buffer)
		if err := json.NewEncoder(b).Encode(body); err != nil {
			return nil, 0, err
		}
		buf = b
	}
	req, err := http.NewRequest(method, endpoint, buf)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	bbBasicAuth(req, user, token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	for _, h := range hdr {
		if h != nil {
			*h = resp.Header.Clone()
		}
	}
	respBody, _ := io.ReadAll(resp.Body)
	// Scrubbed HERE, at the one place the body enters the package, rather than at
	// each Errorf that interpolates it. Every caller's `string(respBody)` is then
	// safe by construction — including the ones that are not about HTTP status at
	// all ("response missing iid"), which a per-error-site scrub would miss.
	respBody = []byte(redact.Scrub(string(respBody), authEnv, token))
	return respBody, resp.StatusCode, nil
}

// bbCredentials resolves the Basic-auth pair for a Bitbucket call, erroring
// before any request is made when a piece is missing. auth_user is checked here
// rather than at the transport: sending Basic base64(:token) would 401 with a
// message that names nothing the user can act on.
func bbCredentials(apiBase, authEnv, authUser string, policy hostallow.Policy) (user, token string, err error) {
	if apiBase == "" {
		return "", "", errors.New("bitbucket backend needs APIBase (set [remote].api_base)")
	}
	if authEnv == "" {
		return "", "", errors.New("bitbucket backend needs AuthEnv (set [remote].auth_env)")
	}
	if strings.TrimSpace(authUser) == "" {
		return "", "", errors.New("bitbucket backend needs AuthUser (set [remote].auth_user) — its credential is HTTP Basic user:token")
	}
	token, err = resolveToken(apiBase, authEnv, policy)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(authUser), token, nil
}

// bitbucketPRStatus reads the PR's authoritative state and destination branch
// from Bitbucket Cloud.
//
// The state is one of OPEN, MERGED, DECLINED or SUPERSEDED — so merged is
// state == MERGED, never state != OPEN. A declined or superseded PR is closed
// without ever having landed, and reporting it merged would false-complete a
// phase whose work was thrown away.
func bitbucketPRStatus(opts OpenOpts) (PRStatus, error) {
	if opts.PRNumber <= 0 {
		return PRStatus{}, errors.New("bitbucket merged-status lookup needs a PR number")
	}
	user, token, err := bbCredentials(opts.APIBase, opts.AuthEnv, opts.AuthUser, opts.Hosts)
	if err != nil {
		return PRStatus{}, err
	}
	workspace, slug, err := bbRepoRef(opts.URL)
	if err != nil {
		return PRStatus{}, err
	}
	endpoint := strings.TrimRight(opts.APIBase, "/") +
		fmt.Sprintf("/repositories/%s/%s/pullrequests/%d", workspace, slug, opts.PRNumber)
	rb, status, err := bbRequest("GET", endpoint, opts.AuthEnv, user, token, nil)
	if err != nil {
		return PRStatus{}, fmt.Errorf("get PR #%d: %w", opts.PRNumber, err)
	}
	if status >= 300 {
		return PRStatus{}, fmt.Errorf("get PR #%d: HTTP %d: %s", opts.PRNumber, status, string(rb))
	}
	var pr struct {
		State       string `json:"state"`
		Destination struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(rb, &pr); err != nil {
		return PRStatus{}, fmt.Errorf("parse bitbucket PR #%d: %w", opts.PRNumber, err)
	}
	return PRStatus{Merged: strings.EqualFold(pr.State, "MERGED"), BaseRef: pr.Destination.Branch.Name}, nil
}

// bbOpenPRBySource returns the open PR whose source branch is head, or
// (nil, nil) when there is none. Bitbucket Cloud filters server-side through
// its q= query language; both the branch name and the state are quoted so a
// head carrying a space or an operator stays a literal.
func bbOpenPRBySource(opts OpenOpts, head string) (*OpenResult, error) {
	user, token, err := bbCredentials(opts.APIBase, opts.AuthEnv, opts.AuthUser, opts.Hosts)
	if err != nil {
		return nil, err
	}
	workspace, slug, err := bbRepoRef(opts.URL)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`source.branch.name=%q AND state="OPEN"`, head)
	endpoint := strings.TrimRight(opts.APIBase, "/") +
		fmt.Sprintf("/repositories/%s/%s/pullrequests?q=%s", workspace, slug, url.QueryEscape(q))
	rb, status, err := bbRequest("GET", endpoint, opts.AuthEnv, user, token, nil)
	if err != nil {
		return nil, fmt.Errorf("list open PRs from %s: %w", head, err)
	}
	if status >= 300 {
		return nil, fmt.Errorf("list open PRs from %s: HTTP %d: %s", head, status, string(rb))
	}
	var page struct {
		Values []struct {
			ID    int `json:"id"`
			Links struct {
				HTML struct {
					Href string `json:"href"`
				} `json:"html"`
			} `json:"links"`
		} `json:"values"`
	}
	if err := json.Unmarshal(rb, &page); err != nil {
		return nil, fmt.Errorf("parse open PRs from %s: %w", head, err)
	}
	if len(page.Values) == 0 {
		return nil, nil
	}
	return &OpenResult{Number: page.Values[0].ID, URL: page.Values[0].Links.HTML.Href}, nil
}

// bbReviewerRefs maps configured reviewer strings to Bitbucket user objects.
// Bitbucket dropped username-based references, so a reviewer is identified by
// its UUID (the "{...}" form) or its account id; the shape is chosen per entry
// so a config holding either kind works.
func bbReviewerRefs(reviewers []string) []map[string]any {
	refs := make([]map[string]any, 0, len(reviewers))
	for _, r := range reviewers {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "{") && strings.HasSuffix(r, "}") {
			refs = append(refs, map[string]any{"uuid": r})
			continue
		}
		refs = append(refs, map[string]any{"account_id": r})
	}
	return refs
}

// openBitbucketPR creates a pull request on Bitbucket Cloud.
//
// Unlike Forgejo and GitLab, draft is a real boolean on this API — there is no
// "Draft:" title-prefix convention to imitate here.
func openBitbucketPR(opts OpenOpts) (*OpenResult, error) {
	user, token, err := bbCredentials(opts.APIBase, opts.AuthEnv, opts.AuthUser, opts.Hosts)
	if err != nil {
		return nil, err
	}
	workspace, slug, err := bbRepoRef(opts.URL)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"title":       opts.Title,
		"description": opts.Body,
		"source":      map[string]any{"branch": map[string]any{"name": opts.HeadBranch}},
		"destination": map[string]any{"branch": map[string]any{"name": opts.BaseBranch}},
	}
	if opts.Draft {
		body["draft"] = true
	}

	base := strings.TrimRight(opts.APIBase, "/") + fmt.Sprintf("/repositories/%s/%s/pullrequests", workspace, slug)
	respBody, status, err := bbRequest("POST", base, opts.AuthEnv, user, token, body)
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	if status >= 300 {
		return nil, fmt.Errorf("create PR: HTTP %d: %s", status, string(respBody))
	}
	var pr struct {
		ID    int `json:"id"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	_ = json.Unmarshal(respBody, &pr)
	if pr.ID == 0 {
		return nil, fmt.Errorf("bitbucket response missing id: %s", string(respBody))
	}
	result := &OpenResult{Number: pr.ID, URL: pr.Links.HTML.Href}

	if refs := bbReviewerRefs(opts.Reviewers); len(refs) > 0 {
		// Reviewers go on a follow-up update, so a rejected reviewer never
		// costs the caller an already-open PR — return the result *and* the
		// error, as the Forgejo and GitLab backends do.
		updEndpoint := base + fmt.Sprintf("/%d", pr.ID)
		rb, st, err := bbRequest("PUT", updEndpoint, opts.AuthEnv, user, token, map[string]any{
			// Bitbucket's PR update wants the title alongside any mutation.
			"title":     opts.Title,
			"reviewers": refs,
		})
		if err != nil || st >= 300 {
			return result, fmt.Errorf("PR opened (#%d) but reviewer assignment failed (HTTP %d): %v %s", pr.ID, st, err, string(rb))
		}
	}
	return result, nil
}
