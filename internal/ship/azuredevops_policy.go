package ship

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// --- Azure DevOps branch policies, read as push protection ---
//
// A branch is protected on azure-devops when an enabled, blocking policy
// configuration is scoped to it (locked ado_branch_protection): any blocking
// policy is what makes Azure Repos reject a direct push with TF402455. dross
// only READS policy configurations — every request here is a GET — and never
// creates or edits one; `dross protect` stays GitHub-only.

// adoPolicyListTop is the $top the policy-configuration read asks for. The list
// is project-wide, across every repository, so a page this full may be
// truncated, and a truncated list is unknown, never "unprotected".
const adoPolicyListTop = 1000

// adoMergeStrategyPolicyType is the built-in "Require a merge strategy" policy
// type id, the same on Services and every Server release.
const adoMergeStrategyPolicyType = "fa4e907d-c16b-4a4c-9dfa-4916e5d171ab"

// ADOProtection is whether a branch carries a blocking policy, or why that
// could not be read. The zero value is unknown, never "unprotected": a caller
// that forgets to fill it in reads as unknown.
type ADOProtection struct {
	Known     bool
	Protected bool
	// Policies names the blocking policies scoped to the branch ("Minimum
	// number of reviewers #12"), for the message that routes around them.
	Policies []string
	// Reason and Err are why the policies could not be read, when !Known. Err
	// wraps hostallow.ErrRefused for a refused api_base.
	Reason string
	Err    error
}

// ADOBranchProtectionFunc is the seam cmd-package callers use, and cmd-package
// tests stub, to read a branch's protection without a network.
var ADOBranchProtectionFunc = ADOBranchProtection

// adoRepoIdentity is the repository the policies are matched against.
type adoRepoIdentity struct {
	id            string // repository GUID
	defaultBranch string // full ref, e.g. refs/heads/main
}

// adoPolicy is one policy configuration, as the protection rules read it.
type adoPolicy struct {
	id                 int
	typeID, typeName   string
	enabled, blocking  bool
	deleted            bool
	scopes             []adoPolicyScope
	allowNoFastForward *bool // merge-strategy policies: merge commits allowed
	modernStrategy     bool  // merge-strategy policies: any allow* key is present
	useSquashMerge     *bool // merge-strategy policies, legacy form: squash only
}

// adoPolicyScope is one entry of a policy's settings.scope.
type adoPolicyScope struct {
	refName   string // "" when the scope names no ref
	matchKind string // Exact | Prefix | DefaultBranch
	repoID    string // "" for repositoryId null: every repository in the project
}

// ADOBranchProtection reads whether branch is protected on the azure-devops
// remote opts names: GET the repository (its id and default branch), then GET
// the project's policy configurations. Any failure to read either — a refused
// host, a 401, a sign-in page, a 5xx, a body that is not the promised list, a
// list that may be truncated — is unknown with the reason, never a known
// "unprotected".
func ADOBranchProtection(opts OpenOpts, branch string) ADOProtection {
	unknown := func(err error) ADOProtection { return ADOProtection{Reason: err.Error(), Err: err} }
	ref, err := adoPolicyRef(branch)
	if err != nil {
		return unknown(err)
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return unknown(err)
	}
	repo, policies, err := adoBranchPolicies(c)
	if err != nil {
		return unknown(err)
	}
	res := ADOProtection{Known: true}
	for _, p := range policies {
		if p.inForce() && p.scopedTo(repo, ref) {
			res.Policies = append(res.Policies, p.label())
		}
	}
	res.Protected = len(res.Policies) > 0
	return res
}

// adoMergeCommitAllowed reports whether target's blocking "Require a merge
// strategy" policies allow a merge commit (Azure DevOps' noFastForward), which
// is how a chore PR auto-completes. No such policy allows it; one that leaves
// allowNoFastForward false — or the legacy useSquashMerge true — forbids it.
// known is false, with the reason, when the policies cannot be read.
func adoMergeCommitAllowed(c adoConn, target string) (allowed, known bool, reason string) {
	ref, err := adoPolicyRef(target)
	if err != nil {
		return false, false, err.Error()
	}
	repo, policies, err := adoBranchPolicies(c)
	if err != nil {
		return false, false, err.Error()
	}
	for _, p := range policies {
		if !p.inForce() || !p.isMergeStrategy() || !p.scopedTo(repo, ref) {
			continue
		}
		if !p.allowsMergeCommit() {
			return false, true, ""
		}
	}
	return true, true, ""
}

// adoPolicyRef is branch as the full ref policies are scoped by (adoBranchRef),
// refusing an empty name: there is no branch to read the protection of.
func adoPolicyRef(branch string) (string, error) {
	b := strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if b == "" {
		return "", errors.New("read branch policies: no branch name")
	}
	return adoBranchRef(b), nil
}

// adoBranchPolicies reads the repository identity and every policy
// configuration in its project. Both are GETs through adoRequest.
func adoBranchPolicies(c adoConn) (adoRepoIdentity, []adoPolicy, error) {
	rb, _, err := adoRequest(c, "GET", c.repoAPI(), nil)
	if err != nil {
		return adoRepoIdentity{}, nil, fmt.Errorf("read branch policies: read the repository: %w", err)
	}
	var repo struct {
		ID            string `json:"id"`
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := json.Unmarshal(rb, &repo); err != nil || strings.TrimSpace(repo.ID) == "" {
		return adoRepoIdentity{}, nil, errors.New("read branch policies: the repository answer carries no id")
	}

	q := url.Values{"$top": {strconv.Itoa(adoPolicyListTop)}}
	endpoint := strings.TrimRight(c.apiBase, "/") + "/" + url.PathEscape(c.repo.Project) + "/_apis/policy/configurations?" + q.Encode()
	pb, _, err := adoRequest(c, "GET", endpoint, nil)
	if err != nil {
		return adoRepoIdentity{}, nil, fmt.Errorf("read branch policies: read the policy configurations: %w", err)
	}
	var page struct {
		Value *[]struct {
			ID         int  `json:"id"`
			IsEnabled  bool `json:"isEnabled"`
			IsBlocking bool `json:"isBlocking"`
			IsDeleted  bool `json:"isDeleted"`
			Type       struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"type"`
			Settings struct {
				Scope []struct {
					RefName      *string `json:"refName"`
					MatchKind    string  `json:"matchKind"`
					RepositoryID *string `json:"repositoryId"`
				} `json:"scope"`
				AllowNoFastForward *bool `json:"allowNoFastForward"`
				AllowSquash        *bool `json:"allowSquash"`
				AllowRebase        *bool `json:"allowRebase"`
				AllowRebaseMerge   *bool `json:"allowRebaseMerge"`
				UseSquashMerge     *bool `json:"useSquashMerge"`
			} `json:"settings"`
		} `json:"value"`
	}
	if err := json.Unmarshal(pb, &page); err != nil || page.Value == nil {
		return adoRepoIdentity{}, nil, errors.New("read branch policies: the policy configurations answer is not the list it promises")
	}
	if len(*page.Value) >= adoPolicyListTop {
		return adoRepoIdentity{}, nil, fmt.Errorf("read branch policies: the policy configurations page is full at the $top of %d, so the list may be truncated", adoPolicyListTop)
	}
	out := make([]adoPolicy, 0, len(*page.Value))
	for _, v := range *page.Value {
		p := adoPolicy{
			id:                 v.ID,
			typeID:             v.Type.ID,
			typeName:           v.Type.DisplayName,
			enabled:            v.IsEnabled,
			blocking:           v.IsBlocking,
			deleted:            v.IsDeleted,
			allowNoFastForward: v.Settings.AllowNoFastForward,
			modernStrategy: v.Settings.AllowNoFastForward != nil || v.Settings.AllowSquash != nil ||
				v.Settings.AllowRebase != nil || v.Settings.AllowRebaseMerge != nil,
			useSquashMerge: v.Settings.UseSquashMerge,
		}
		for _, s := range v.Settings.Scope {
			sc := adoPolicyScope{matchKind: s.MatchKind}
			if s.RefName != nil {
				sc.refName = *s.RefName
			}
			if s.RepositoryID != nil {
				sc.repoID = *s.RepositoryID
			}
			p.scopes = append(p.scopes, sc)
		}
		out = append(out, p)
	}
	return adoRepoIdentity{id: repo.ID, defaultBranch: repo.DefaultBranch}, out, nil
}

// inForce is a policy that can refuse a push: enabled, blocking, not deleted.
func (p adoPolicy) inForce() bool { return p.enabled && p.blocking && !p.deleted }

// isMergeStrategy is the "Require a merge strategy" policy type.
func (p adoPolicy) isMergeStrategy() bool {
	return strings.EqualFold(p.typeID, adoMergeStrategyPolicyType) ||
		strings.EqualFold(strings.TrimSpace(p.typeName), "Require a merge strategy")
}

// allowsMergeCommit reads a merge-strategy policy's settings. The current form
// lists the strategies it allows, so a merge commit is allowed only when
// allowNoFastForward is true — a list naming allowSquash alone forbids it. The
// legacy form only says squash-only or not.
func (p adoPolicy) allowsMergeCommit() bool {
	if p.modernStrategy {
		return p.allowNoFastForward != nil && *p.allowNoFastForward
	}
	return p.useSquashMerge == nil || !*p.useSquashMerge
}

// scopedTo reports whether any of the policy's scopes covers ref in repo. A
// scope counts when its repositoryId is null or this repository, and its ref
// matches: Exact on the whole ref, Prefix on its start (both case-insensitive,
// as Azure DevOps compares refs), or DefaultBranch when ref is the repository's
// default branch. A scope that names no ref is a repository-wide setting, not a
// branch policy, and does not count.
func (p adoPolicy) scopedTo(repo adoRepoIdentity, ref string) bool {
	for _, s := range p.scopes {
		if s.repoID != "" && !strings.EqualFold(s.repoID, repo.id) {
			continue
		}
		switch {
		case strings.EqualFold(s.matchKind, "DefaultBranch"):
			if repo.defaultBranch != "" && strings.EqualFold(ref, repo.defaultBranch) {
				return true
			}
		case s.refName == "":
			continue
		case strings.EqualFold(s.matchKind, "Prefix"):
			if len(ref) >= len(s.refName) && strings.EqualFold(ref[:len(s.refName)], s.refName) {
				return true
			}
		default: // Exact, and a scope that omits matchKind
			if strings.EqualFold(ref, s.refName) {
				return true
			}
		}
	}
	return false
}

// label names the policy for a message: its type and id.
func (p adoPolicy) label() string {
	name := strings.TrimSpace(p.typeName)
	if name == "" {
		name = "policy"
	}
	return fmt.Sprintf("%s #%d", name, p.id)
}
