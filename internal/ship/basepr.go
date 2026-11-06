package ship

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Rivil/dross/internal/configenum"
)

// ErrBasePRLookupUnsupported is returned by OpenPRsTargeting for providers
// whose open-PR-by-base query isn't wired yet. Callers announce the skip and
// fall back to the offline record scan rather than blocking — the locked
// dependent_detection decision makes an announced skip a first-class outcome,
// so an unwired backend is a supported shape rather than a gap.
var ErrBasePRLookupUnsupported = errors.New("open-PR-by-base lookup is not supported for this provider")

// BasePR is one open pull request that targets a given base branch — the
// evidence that deleting that branch would orphan somebody's review.
type BasePR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
}

// OpenPRsTargeting lists the open PRs whose base is `base`. It mirrors
// GetPRStatus's provider dispatch: GitHub answers via the overridable
// ghCommand seam, GitLab, Forgejo/Gitea and Azure DevOps answer via their REST
// APIs, Bitbucket returns ErrBasePRLookupUnsupported, and an outright unknown
// provider returns a plain error so a misconfiguration stays distinguishable
// from a missing backend.
//
// A lookup failure is always an error, never an empty slice: an empty slice
// reads as "no dependents" and authorizes an irreversible branch delete.
func OpenPRsTargeting(opts OpenOpts, base string) ([]BasePR, error) {
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubOpenPRsTargeting(base)
	case "gitlab":
		return gitlabOpenMRsTargeting(opts, base)
	case "forgejo", "gitea":
		return forgejoOpenPRsTargeting(opts, base)
	case "azure-devops":
		return azureDevOpsOpenPRsTargeting(opts, base)
	case "bitbucket":
		return nil, ErrBasePRLookupUnsupported
	default:
		return nil, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// OpenPRsTargetingFunc is the exported, overridable seam that cmd-package
// callers use (and that cmd-package tests stub) to query open PRs without a
// `gh` binary or network — the unexported ghCommand seam is unreachable from
// package cmd. Production code calls OpenPRsTargetingFunc, not OpenPRsTargeting.
var OpenPRsTargetingFunc = OpenPRsTargeting

func gitHubOpenPRsTargeting(base string) ([]BasePR, error) {
	if base == "" {
		return nil, errors.New("github open-PR lookup needs a base branch")
	}
	// base rides the value slot of its own flag — see openGitHubPR. A base of
	// "--state" is then a base, not a second --state flag silently overriding
	// the "open" filter and returning closed PRs as live dependents.
	cmd, err := screenedGH("pr", "list", "--base", base, "--state", "open", "--json", "number,title,url,headRefName")
	if err != nil {
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, ghFailed("gh pr list --base "+base, err, out)
	}
	var prs []BasePR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, ghUnparseable("parse gh pr list --base "+base, out)
	}
	//dross:taint-cleared BasePR is gh's decoded --json number,title,url,headRefName record: forge metadata about each PR, not gh's prose
	decoded := prs
	return decoded, nil
}
