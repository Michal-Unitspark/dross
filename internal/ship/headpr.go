package ship

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Rivil/dross/internal/configenum"
)

// ErrHeadPRLookupUnsupported is the sentinel FindOpenPRByHead returns for a
// provider whose open-PR-by-head query isn't wired. Ship announces the skip
// and falls through to opening a PR — an unwired backend is a supported shape,
// distinguishable from a lookup that failed. Every shipped provider answers
// today, azure-devops included; the sentinel stays declared so a provider
// added without a lookup has a shape ship already knows how to skip.
var ErrHeadPRLookupUnsupported = errors.New("open-PR-by-head lookup is not supported for this provider")

// FindOpenPRByHead returns the open PR whose head branch is exactly head, or
// (nil, nil) when there is none. It is the existing_pr_source fallback: when
// the phase record carries no PR number, ship asks the provider before
// creating one, so a ship that died between opening the PR and writing the
// record never opens a second.
//
// A lookup failure is always a non-nil error, never (nil, nil): nil-nil reads
// as "no PR yet" and authorizes exactly the duplicate this exists to prevent.
func FindOpenPRByHead(opts OpenOpts, head string) (*OpenResult, error) {
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubOpenPRByHead(head)
	case "forgejo", "gitea":
		return forgejoOpenPRByHead(opts, head)
	case "gitlab":
		return gitlabOpenMRBySource(opts, head)
	case "bitbucket":
		return bbOpenPRBySource(opts, head)
	case "azure-devops":
		return adoOpenPRBySource(opts, head)
	default:
		return nil, fmt.Errorf("unsupported provider %q (expected %s)", opts.Provider, configenum.ShipProviders.List())
	}
}

// FindOpenPRByHeadFunc is the exported, overridable seam that cmd-package
// callers use (and that cmd-package tests stub) to look up an open PR without
// a `gh` binary or network — the unexported ghCommand seam is unreachable from
// package cmd. Production code calls FindOpenPRByHeadFunc, not FindOpenPRByHead.
var FindOpenPRByHeadFunc = FindOpenPRByHead

func gitHubOpenPRByHead(head string) (*OpenResult, error) {
	if head == "" {
		return nil, errors.New("github open-PR lookup needs a head branch")
	}
	// head rides the value slot of its own flag — see openGitHubPR — so a
	// head of "--state" is a branch name, not a flag overriding the filter.
	cmd, err := screenedGH("pr", "list", "--head", head, "--state", "open", "--json", "number,url")
	if err != nil {
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, ghFailed("gh pr list --head "+head, err, out)
	}
	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, ghUnparseable("parse gh pr list --head "+head, out)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &OpenResult{Number: prs[0].Number, URL: prs[0].URL}, nil
}
