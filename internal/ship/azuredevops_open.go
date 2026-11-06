package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/Rivil/dross/internal/hostallow"
)

// adoDescriptionLimit is Azure Repos' cap on a PR description. The service
// counts .NET string length — UTF-16 code units — so that is what is counted
// here; a body within it is within the rune count too.
const adoDescriptionLimit = 4000

// adoTruncationNote ends a description cut to the limit, so a reader of the PR
// knows the body is not whole.
const adoTruncationNote = "\n\n_(truncated: Azure DevOps caps a pull request description at 4000 characters)_"

// adoGUID is the shape of an Azure DevOps identity id: a reviewer written this
// way is used as-is, with no lookup.
var adoGUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// openAzureDevOpsPR creates an Azure Repos pull request, then adds each
// configured reviewer as optional.
//
// Draft is a real boolean here (isDraft), so the title carries no "Draft:"
// prefix. Reviewers are not part of the create body: each is resolved and
// added on its own PUT, so a reviewer that fails costs neither the open PR nor
// the reviewers after it — the result comes back with an error naming what
// failed, as the Forgejo, GitLab and Bitbucket backends do.
func openAzureDevOpsPR(opts OpenOpts) (*OpenResult, error) {
	c, err := adoOpenConn(opts)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"sourceRefName": adoBranchRef(opts.HeadBranch),
		"targetRefName": adoBranchRef(opts.BaseBranch),
		"title":         opts.Title,
		"description":   adoDescription(opts.Body),
	}
	if opts.Draft {
		body["isDraft"] = true
	}
	respBody, _, err := adoRequest(c, "POST", c.repoAPI()+"/pullrequests", body)
	if err != nil {
		return nil, fmt.Errorf("create PR: %w", err)
	}
	var pr struct {
		PullRequestID int `json:"pullRequestId"`
	}
	_ = json.Unmarshal(respBody, &pr)
	if pr.PullRequestID <= 0 {
		return nil, fmt.Errorf("azure devops response missing pullRequestId: %s", string(respBody))
	}
	// The browser URL, not the API one: it is what gets printed and recorded.
	result := &OpenResult{
		Number: pr.PullRequestID,
		URL:    fmt.Sprintf("%s/pullrequest/%d", c.repo.URL, pr.PullRequestID),
	}

	var failed []error
	for _, reviewer := range opts.Reviewers {
		reviewer = strings.TrimSpace(reviewer)
		if reviewer == "" {
			continue
		}
		if err := adoAddReviewer(c, opts.Hosts, pr.PullRequestID, reviewer); err != nil {
			failed = append(failed, fmt.Errorf("reviewer %q: %w", reviewer, err))
		}
	}
	if len(failed) > 0 {
		return result, fmt.Errorf("PR opened (#%d) but reviewer assignment failed: %w", pr.PullRequestID, errors.Join(failed...))
	}
	return result, nil
}

// adoBranchRef is the full ref Azure Repos addresses a branch by. A head that
// is already a refs/heads/ ref is not prefixed twice.
func adoBranchRef(branch string) string {
	if strings.HasPrefix(branch, "refs/heads/") {
		return branch
	}
	return "refs/heads/" + branch
}

// adoDescription caps body at adoDescriptionLimit, ending a cut body in the
// truncation note and never splitting a rune.
func adoDescription(body string) string {
	if utf16Len(body) <= adoDescriptionLimit {
		return body
	}
	budget := adoDescriptionLimit - utf16Len(adoTruncationNote)
	var b strings.Builder
	n := 0
	for _, r := range body {
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1 // an invalid rune is written as U+FFFD, one unit
		}
		if n+w > budget {
			break
		}
		b.WriteRune(r)
		n += w
	}
	return b.String() + adoTruncationNote
}

// utf16Len is s's length in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if w := utf16.RuneLen(r); w > 0 {
			n += w
		} else {
			n++
		}
	}
	return n
}

// adoAddReviewer adds one reviewer to PR number as optional (isRequired
// false). A GUID-shaped entry is already an identity id; anything else — an
// email or UPN — is resolved through the Identities API first.
func adoAddReviewer(c adoConn, hosts hostallow.Policy, number int, reviewer string) error {
	id := reviewer
	if !adoGUID.MatchString(reviewer) {
		var err error
		if id, err = adoResolveIdentity(c, hosts, reviewer); err != nil {
			return err
		}
	}
	endpoint := fmt.Sprintf("%s/pullRequests/%d/reviewers/%s", c.repoAPI(), number, url.PathEscape(id))
	_, _, err := adoRequest(c, "PUT", endpoint, map[string]any{"isRequired": false})
	return err
}

// adoResolveIdentity resolves an email or UPN to the one identity id it
// names. No match and more than one are both errors: guessing between two
// people would put the wrong reviewer on the PR.
func adoResolveIdentity(c adoConn, hosts hostallow.Policy, who string) (string, error) {
	base := adoIdentityBase(c.apiBase)
	// On Services the identities host is not api_base, so it is checked on its
	// own before the credential is sent there.
	if err := hosts.Check("[remote].api_base (identities)", base); err != nil {
		return "", err
	}
	q := url.Values{"searchFilter": {"General"}, "filterValue": {who}}
	rb, _, err := adoRequest(c, "GET", base+"/_apis/identities?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("identity lookup: %w", err)
	}
	var found struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &found); err != nil {
		return "", errors.New("identity lookup: the response is not the JSON it promises")
	}
	switch len(found.Value) {
	case 0:
		return "", errors.New("no Azure DevOps identity matches it")
	case 1:
		if found.Value[0].ID == "" {
			return "", errors.New("identity lookup: the match carries no id")
		}
		return found.Value[0].ID, nil
	default:
		return "", fmt.Errorf("%d Azure DevOps identities match it — configure the identity id (a GUID) instead", len(found.Value))
	}
}

// adoIdentityBase is where identities resolve. On Services that is the org on
// vssps.dev.azure.com, for dev.azure.com/<org> and <org>.visualstudio.com
// alike; on a Server it is the collection itself.
func adoIdentityBase(apiBase string) string {
	base := strings.TrimRight(apiBase, "/")
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	host := strings.ToLower(u.Host)
	if host == "dev.azure.com" {
		if org, _, _ := strings.Cut(strings.TrimPrefix(u.EscapedPath(), "/"), "/"); org != "" {
			return "https://vssps.dev.azure.com/" + org
		}
	}
	if org, ok := strings.CutSuffix(host, ".visualstudio.com"); ok && org != "" && !strings.Contains(org, ".") {
		return "https://vssps.dev.azure.com/" + org
	}
	return base
}
