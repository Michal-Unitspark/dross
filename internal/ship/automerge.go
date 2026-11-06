package ship

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Rivil/dross/internal/configenum"
)

// ErrAutoMergeUnavailable is AutoMergePR's answer when GitHub won't merge the
// PR on dross's behalf: auto-merge is switched off for the repo, or the PR was
// mergeable at once and the direct merge was refused. The PR is still open;
// the caller reports its URL and leaves the merge to a human. The wrapped text
// carries gh's reason.
var ErrAutoMergeUnavailable = errors.New("auto-merge unavailable")

// AutoMergeResult is what arming auto-merge achieved.
type AutoMergeResult struct {
	// AutoEnabled: auto-merge is armed; GitHub merges the PR when the base
	// branch's required checks pass.
	AutoEnabled bool
	// Merged: the PR is merged already. Either gh merged it outright, or it
	// was mergeable at once and AutoMergePR merged it directly.
	Merged bool
}

// mergeMethods are the gh pr merge method flags AutoMergePR accepts.
var mergeMethods = map[string]bool{"merge": true, "squash": true, "rebase": true}

// AutoMergePR arms GitHub auto-merge on PR n with method ("merge" or
// "squash"): `gh pr merge --auto --<method> -- <n>`. It never passes --admin,
// so the base branch's ruleset still decides when, and whether, the PR lands.
// When gh succeeds, what it achieved is read back from the PR (see
// autoMergeOutcome), never from gh's prose.
//
// GitHub refuses to arm auto-merge on a PR that is already mergeable ("clean
// status") — on a branch with no required checks, every PR is. Then it merges
// directly with `gh pr merge --<method> -- <n>`, which the server still holds
// to the ruleset. Auto-merge switched off for the repo, or a refused direct
// merge, is ErrAutoMergeUnavailable; any other refusal is a plain error and
// makes no direct-merge attempt.
//
// On azure-devops it arms the chore PR's auto-complete instead
// (adoArmAutoComplete), and only with method "merge": a chore PR lands as a
// merge commit so every local base fast-forwards to it.
func AutoMergePR(opts OpenOpts, n int, method string) (AutoMergeResult, error) {
	switch p := configenum.Normalize(opts.Provider); p {
	case "github":
	case "azure-devops":
		if method != "merge" {
			return AutoMergeResult{}, fmt.Errorf("auto-complete on azure-devops arms a merge commit only; method %q is not offered", method)
		}
		if n <= 0 {
			return AutoMergeResult{}, errors.New("auto-complete needs a PR number")
		}
		return adoArmAutoComplete(opts, n)
	default:
		return AutoMergeResult{}, fmt.Errorf("auto-merge is GitHub and Azure DevOps only; provider is %q", opts.Provider)
	}
	if n <= 0 {
		return AutoMergeResult{}, errors.New("auto-merge needs a PR number")
	}
	if !mergeMethods[method] {
		return AutoMergeResult{}, fmt.Errorf("auto-merge: unknown merge method %q (want merge, squash or rebase)", method)
	}
	pr := strconv.Itoa(n)
	what := fmt.Sprintf("gh pr merge --auto #%d", n)

	// Flags ahead of the separator, the PR number behind it: cobra's `--` ends
	// flag parsing, so a flag after it would become a positional.
	cmd, err := screenedGH("pr", "merge", "--auto", "--"+method, "--", pr)
	if err != nil {
		return AutoMergeResult{}, err
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return autoMergeOutcome(n, what)
	}

	text := string(out)
	if mentions(text, "clean status") {
		return mergeDirectly(n, method)
	}
	if mentions(text, "auto merge is not allowed", "auto-merge is not allowed") {
		return AutoMergeResult{}, ghUnavailable(what, out, "auto-merge is not allowed for this repository — turn on its allow_auto_merge setting (`dross protect --apply` does)")
	}
	return AutoMergeResult{}, ghFailed(what, err, out)
}

// autoMergeOutcome reads what a successful `gh pr merge --auto` did from the
// PR itself. gh prints its "will be automatically merged" line only on a
// terminal; under dross its stdout is a pipe, so a successful arm exits 0
// having printed nothing (seen live on chore PR #140). The exit status says gh
// succeeded; the PR says at what.
func autoMergeOutcome(n int, what string) (AutoMergeResult, error) {
	view := fmt.Sprintf("gh pr view #%d", n)
	// Flags ahead of the separator, the PR number behind it, as in
	// gitHubPRStatus.
	cmd, err := screenedGH("pr", "view", "--json", "state,autoMergeRequest", "--", strconv.Itoa(n))
	if err != nil {
		return AutoMergeResult{}, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return AutoMergeResult{}, ghFailed(view, err, out)
	}
	var pr struct {
		State            string    `json:"state"`
		AutoMergeRequest *struct{} `json:"autoMergeRequest"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return AutoMergeResult{}, ghUnparseable("parse "+view, out)
	}
	switch {
	case strings.EqualFold(pr.State, "MERGED"):
		return AutoMergeResult{Merged: true}, nil
	case pr.AutoMergeRequest != nil:
		return AutoMergeResult{AutoEnabled: true}, nil
	}
	return AutoMergeResult{}, fmt.Errorf("%s exited 0, but PR #%d is neither merged nor armed for auto-merge", what, n)
}

// mergeDirectly merges PR n now. Only reached when GitHub said the PR is
// already mergeable, so the ruleset has nothing left to wait for; the server
// still checks it.
func mergeDirectly(n int, method string) (AutoMergeResult, error) {
	cmd, err := screenedGH("pr", "merge", "--"+method, "--", strconv.Itoa(n))
	if err != nil {
		return AutoMergeResult{}, err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return AutoMergeResult{}, ghUnavailable(fmt.Sprintf("gh pr merge #%d", n), out,
			fmt.Sprintf("PR #%d was mergeable at once, but GitHub refused the direct merge", n))
	}
	return AutoMergeResult{Merged: true}, nil
}

// ghUnavailable reports a refusal the way ghFailed reports a failure — gh's
// own words on stderr, where the user is already looking — and returns
// ErrAutoMergeUnavailable with fixed prose saying why. gh's output never
// rides the error: it can carry an API response body.
func ghUnavailable(what string, out []byte, why string) error {
	if len(out) > 0 {
		fmt.Fprintf(ghStderr, "%s: gh said:\n%s\n", what, bytes.TrimRight(out, "\n"))
	}
	return fmt.Errorf("%w: %s", ErrAutoMergeUnavailable, why)
}

// mentions reports whether text contains any of phrases (lower-case),
// ignoring case.
func mentions(text string, phrases ...string) bool {
	lower := strings.ToLower(text)
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// AutoMergePRFunc is the overridable seam cmd-package callers use, and
// cmd-package tests stub, to arm auto-merge without gh or a network — the
// unexported ghCommand seam is unreachable from package cmd.
var AutoMergePRFunc = AutoMergePR
