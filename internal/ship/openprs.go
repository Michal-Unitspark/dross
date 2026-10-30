package ship

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Rivil/dross/internal/configenum"
)

// ErrOpenPRListUnsupported is what ListOpenPRs returns for every provider but
// GitHub and Azure DevOps, without spawning or sending anything (forge_scope).
var ErrOpenPRListUnsupported = errors.New("open-PR listing is not supported for this provider")

// CheckRollup is one PR's checks reduced to a single verdict.
type CheckRollup string

const (
	ChecksPassing CheckRollup = "passing"
	ChecksFailing CheckRollup = "failing"
	ChecksPending CheckRollup = "pending"
	ChecksNone    CheckRollup = "none"
)

// PRAuthor is a PR's author as gh reports it. IsBot is the forge's own flag —
// gh's `is_bot` — never a guess from the login; Azure DevOps has none, so it is
// always false there.
type PRAuthor struct {
	Login string `json:"login"`
	IsBot bool   `json:"is_bot"`
}

// PRCheck is one entry of gh's statusCheckRollup: a CheckRun carries Status
// and, once COMPLETED, Conclusion; a StatusContext carries State.
type PRCheck struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// OpenPRRecord is one open pull request, decoded from gh's --json record or
// mapped from an Azure DevOps PR. Checks is not gh's: it is StatusCheckRollup
// reduced by checkRollup, or ship ci's verdict on Azure DevOps.
type OpenPRRecord struct {
	Number            int         `json:"number"`
	Title             string      `json:"title"`
	URL               string      `json:"url"`
	Author            PRAuthor    `json:"author"`
	CreatedAt         time.Time   `json:"createdAt"`
	HeadRefName       string      `json:"headRefName"`
	IsCrossRepository bool        `json:"isCrossRepository"`
	StatusCheckRollup []PRCheck   `json:"statusCheckRollup"`
	Checks            CheckRollup `json:"-"`
}

// prListLimit is the --limit the lister asks for. gh truncates at whatever
// limit it is given — 30 when none is — and says nothing, so a page that comes
// back full cannot be told from a truncated one and is refused as incomplete.
const prListLimit = 100

// prListFields is the --json field set. createdAt, not updatedAt: both bots
// rebase or force-push in place, which would reset an updated-at age (pr_age).
const prListFields = "number,title,url,author,createdAt,headRefName,isCrossRepository,statusCheckRollup"

// prListTimeout bounds the one gh spawn: generous for a 100-PR
// statusCheckRollup query, still bounding a stalled network. Tests override it.
var prListTimeout = 20 * time.Second

// prListWaitDelay bounds how long Wait waits for gh's stdout to close once gh
// has exited or been killed — a grandchild still holding the pipe would
// otherwise hold Wait with it.
const prListWaitDelay = time.Second

// ListOpenPRs lists the repository's open PRs with their check rollups.
// GitHub and Azure DevOps answer; every other provider gets
// ErrOpenPRListUnsupported without a spawn or a request.
//
// A failure is always (nil, err), never an empty list: an empty list says the
// forge was reached and nothing is open. The failure is also QUIET — nothing
// goes to ghStderr and gh's own stderr is never inherited. Its caller is a
// read-only digest that shows nothing when the forge cannot be asked, and a gh
// auth prompt on every tick would be noise that digest exists to spare.
func ListOpenPRs(opts OpenOpts) ([]OpenPRRecord, error) {
	switch configenum.Normalize(opts.Provider) {
	case "github":
		return gitHubOpenPRs()
	case "azure-devops":
		return azureDevOpsOpenPRs(opts)
	default:
		return nil, fmt.Errorf("provider %q: %w", opts.Provider, ErrOpenPRListUnsupported)
	}
}

// ListOpenPRsFunc is the exported, overridable seam that cmd-package callers
// use (and that cmd-package tests stub) to list open PRs without a `gh` binary
// or network — the unexported ghCommand seam is unreachable from package cmd.
// Production code calls ListOpenPRsFunc, not ListOpenPRs.
var ListOpenPRsFunc = ListOpenPRs

func gitHubOpenPRs() ([]OpenPRRecord, error) {
	// One spawn for every PR's checks: statusCheckRollup rides the same list
	// query, so nothing fans out per PR.
	cmd, err := screenedGH("pr", "list", "--state", "open", "--limit", strconv.Itoa(prListLimit), "--json", prListFields)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	// stdout alone is decoded; stderr stays nil, which exec wires to the null
	// device, so gh's warnings neither corrupt the JSON nor reach the terminal.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.WaitDelay = prListWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(prListTimeout, func() {
		timedOut.Store(true)
		_ = cmd.Process.Kill()
	})
	err = cmd.Wait()
	timer.Stop()
	if timedOut.Load() {
		return nil, fmt.Errorf("gh pr list: no answer within %s", prListTimeout)
	}
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var raw []OpenPRRecord
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, errors.New("gh pr list: gh's output is not the JSON it promises")
	}
	//dross:taint-cleared OpenPRRecord is gh's decoded --json record of each open PR: forge metadata, not gh's prose; its CreatedAt feeds the (time.Time).Sub age computation in internal/watch
	prs := raw
	if len(prs) >= prListLimit {
		return nil, fmt.Errorf("gh pr list: the page is full at the --limit of %d open PRs, so the list may be truncated", prListLimit)
	}
	for i := range prs {
		if prs[i].Number <= 0 {
			return nil, errors.New("gh pr list: a record carries no PR number")
		}
		if prs[i].CreatedAt.IsZero() {
			return nil, fmt.Errorf("gh pr list: PR #%d carries no createdAt", prs[i].Number)
		}
		prs[i].Checks = checkRollup(prs[i].StatusCheckRollup)
	}
	if prs == nil {
		prs = []OpenPRRecord{}
	}
	return prs, nil
}

// checkRollup reduces a PR's checks the way `gh pr checks` buckets them: any
// failure fails the PR; otherwise anything unfinished — or a conclusion this
// code does not know, STALE included — keeps it pending; only a set that is
// all success, neutral or skipped passes. No checks at all is none.
func checkRollup(checks []PRCheck) CheckRollup {
	if len(checks) == 0 {
		return ChecksNone
	}
	pending := false
	for _, c := range checks {
		switch checkState(c) {
		case ChecksFailing:
			return ChecksFailing
		case ChecksPending:
			pending = true
		}
	}
	if pending {
		return ChecksPending
	}
	return ChecksPassing
}

// checkState is one check's verdict. A StatusContext's State is final; a
// CheckRun reads its Conclusion once COMPLETED and its Status until then.
func checkState(c PRCheck) CheckRollup {
	state := c.State
	if state == "" {
		state = c.Status
		if c.Status == "COMPLETED" {
			state = c.Conclusion
		}
	}
	switch state {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return ChecksPassing
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return ChecksFailing
	default:
		return ChecksPending
	}
}
