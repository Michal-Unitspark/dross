package ship

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// --- CI verdict on Bitbucket Cloud ---
//
// Bitbucket's CI is the commit statuses on the PR's head: Pipelines and any
// external CI report there. Every status must be SUCCESSFUL for a pass.

// bbStatusPage is the pagelen every statuses read asks for.
const bbStatusPage = 100

// bbCommitShape is a commit id that may be spliced into a statuses path.
var bbCommitShape = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// bitbucketCIStatus reads the commit statuses on PR opts.PRNumber's head at
// the watched sha. A PR whose head is not sha is pending with a Reason, with
// no status read. Pages are requested by number, up to prThreadMaxPages;
// `next` is read only as "there is more", never requested, since bbRequest
// would carry the credential to whatever URL it names.
func bitbucketCIStatus(opts OpenOpts, sha string) (CIResult, error) {
	n := opts.PRNumber
	if n <= 0 {
		return CIResult{}, fmt.Errorf("PR number %d is not a valid number", n)
	}
	if !bbCommitShape.MatchString(sha) {
		return CIResult{}, fmt.Errorf("bitbucket CI read: %q is not a commit id", sha)
	}
	user, token, err := bbCredentials(opts.APIBase, opts.AuthEnv, opts.AuthUser, opts.Hosts)
	if err != nil {
		return CIResult{}, err
	}
	workspace, slug, err := bbRepoRef(opts.URL)
	if err != nil {
		return CIResult{}, err
	}
	repoAPI := strings.TrimRight(opts.APIBase, "/") + "/repositories/" + workspace + "/" + slug

	type pull struct {
		Source struct {
			Branch struct {
				Name string `json:"name"`
			} `json:"branch"`
			Commit struct {
				Hash string `json:"hash"`
			} `json:"commit"`
		} `json:"source"`
	}
	pr, err := bbGetJSON[pull](opts, user, token, repoAPI+"/pullrequests/"+strconv.Itoa(n))
	if err != nil {
		return CIResult{}, fmt.Errorf("read PR #%d: %w", n, err)
	}
	// Bitbucket reports the head abbreviated: it is the watched head when it
	// prefixes it.
	hash := strings.ToLower(pr.Source.Commit.Hash)
	if hash == "" || !strings.HasPrefix(strings.ToLower(sha), hash) {
		if hash == "" {
			hash = "(none)"
		}
		return CIResult{
			Verdict: CIPending,
			Reason:  fmt.Sprintf("PR head %s is not the watched %s — run dross ship %s", hash, sha, strings.TrimPrefix(pr.Source.Branch.Name, "phase/")),
		}, nil
	}

	type status struct {
		State string `json:"state"`
		URL   string `json:"url"`
	}
	type page struct {
		Values *[]status `json:"values"`
		Next   string    `json:"next"`
	}
	var all []status
	for p := 1; ; p++ {
		if p > prThreadMaxPages {
			return CIResult{}, fmt.Errorf("read %s statuses: still more after %d pages of %d, so the list may be truncated", sha, prThreadMaxPages, bbStatusPage)
		}
		pg, err := bbGetJSON[page](opts, user, token, fmt.Sprintf("%s/commit/%s/statuses?pagelen=%d&page=%d", repoAPI, sha, bbStatusPage, p))
		if err != nil {
			return CIResult{}, fmt.Errorf("read %s statuses: %w", sha, err)
		}
		if pg.Values == nil {
			return CIResult{}, errors.New("read " + sha + " statuses: the answer is not the status list it promises")
		}
		all = append(all, *pg.Values...)
		if pg.Next == "" {
			break
		}
	}

	// fail > pending > ask > pass; nothing reported is none.
	if len(all) == 0 {
		return CIResult{Verdict: CINone}, nil
	}
	var running bool
	var unknown string
	for _, s := range all {
		switch s.State {
		case "SUCCESSFUL":
		case "FAILED", "STOPPED":
			return CIResult{Verdict: CIFail, Link: s.URL}, nil
		case "INPROGRESS":
			running = true
		default:
			if unknown == "" {
				unknown = s.State
			}
		}
	}
	switch {
	case running:
		return CIResult{Verdict: CIPending}, nil
	case unknown != "":
		return CIResult{Verdict: CIAsk, Reason: fmt.Sprintf("a commit status reports state %q, which dross does not read either way", unknown)}, nil
	}
	return CIResult{Verdict: CIPass}, nil
}
