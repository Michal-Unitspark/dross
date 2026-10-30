package ship

import (
	"fmt"
	"strings"
)

// --- Azure DevOps CI verdict ---
//
// CI on Azure DevOps is two things (the ci_source lock): the builds that ran
// on the phase branch at the watched head, and the PR's build-validation and
// status-check policy evaluations, blocking and optional alike. Reviewer,
// comment-resolution and work-item policies gate the merge but are not CI.
// The rollup here is pure: fetching is azuredevops_ci_fetch.go's.

// adoPolicyKind is what a policy type means to ship.
type adoPolicyKind int

const (
	adoPolicyOther   adoPolicyKind = iota // neither CI nor a merge gate ship names
	adoPolicyCI                           // a build-validation or status-check policy
	adoPolicyBlocker                      // a non-CI policy that can block completion
)

// adoPolicyTypes are Azure DevOps' well-known policy type ids. A type id is
// matched in full: Minimum reviewers and Require a merge strategy share their
// first three groups. Names are Azure DevOps' own display names, used when a
// blocked completion is surfaced; a policy's configured displayName is never
// what classifies it.
var adoPolicyTypes = map[string]struct {
	kind adoPolicyKind
	name string
}{
	"0609b952-1397-4640-95ec-e00a01b2c241": {adoPolicyCI, "Build"},
	"cbdc66da-9728-4af8-aada-9a5a32e4a226": {adoPolicyCI, "Status"},
	"fd2167ab-b0be-447a-8ec8-39368250530e": {adoPolicyBlocker, "Required reviewers"},
	"fa4e907d-c16b-4a4c-9dfa-4906e5d171dd": {adoPolicyBlocker, "Minimum number of reviewers"},
	"c6a1889d-b943-4856-b76f-9e46bb6b0df2": {adoPolicyBlocker, "Comment requirements"},
	"40e92b44-2fe1-4dd6-b3d8-74a9c21d0c6e": {adoPolicyBlocker, "Work item linking"},
}

// adoPolicyClass classifies a policy type id, case-insensitively, and names it.
// An unknown type id is adoPolicyOther with no name.
func adoPolicyClass(typeID string) (adoPolicyKind, string) {
	p, ok := adoPolicyTypes[strings.ToLower(typeID)]
	if !ok {
		return adoPolicyOther, ""
	}
	return p.kind, p.name
}

// adoBuildObs is one build at the watched head. Result is read only once
// Status is completed.
type adoBuildObs struct {
	Status string
	Result string
	Link   string
}

// adoEvalObs is one policy evaluation on the PR. Name is the configured
// display name and Blocking whether the policy is required; neither changes
// whether it counts — TypeID alone does.
type adoEvalObs struct {
	TypeID   string
	Name     string
	Status   string
	Blocking bool
	Expired  bool
	Link     string
}

// adoCIState is one counted item's state, in ascending precedence: the
// rollup is the highest state any item holds, so it never depends on order.
type adoCIState int

const (
	adoCIPassed adoCIState = iota
	adoCIQueued
	adoCIAsk
	adoCIRunning
	adoCIFailed
)

// adoCIItem is a counted build or evaluation with what the verdict needs from
// it.
type adoCIItem struct {
	state  adoCIState
	link   string
	reason string
}

// adoBuildItem maps a build. A partially succeeded build had a failing step
// and is a fail; a result or status dross does not know is an ask.
func adoBuildItem(b adoBuildObs) adoCIItem {
	switch strings.ToLower(b.Status) {
	case "completed":
		switch strings.ToLower(b.Result) {
		case "succeeded":
			return adoCIItem{state: adoCIPassed}
		case "failed", "canceled", "partiallysucceeded":
			return adoCIItem{state: adoCIFailed, link: b.Link}
		}
		return adoCIItem{state: adoCIAsk, reason: fmt.Sprintf("a build completed with result %q, which dross does not read either way", b.Result)}
	case "inprogress", "cancelling":
		return adoCIItem{state: adoCIRunning}
	case "notstarted", "postponed":
		return adoCIItem{state: adoCIQueued}
	}
	return adoCIItem{state: adoCIAsk, reason: fmt.Sprintf("a build reports status %q, which dross does not read either way", b.Status)}
}

// adoEvalItem maps a CI evaluation; counted is false for one that is not CI
// or does not apply to this PR.
func adoEvalItem(e adoEvalObs) (item adoCIItem, counted bool) {
	if kind, _ := adoPolicyClass(e.TypeID); kind != adoPolicyCI {
		return adoCIItem{}, false
	}
	status := strings.ToLower(e.Status)
	switch {
	case status == "notapplicable":
		return adoCIItem{}, false
	case e.Expired:
		// Its result — approved or rejected — belongs to an earlier head, so
		// it says nothing about this one, and nothing re-queues the build
		// unless someone does.
		return adoCIItem{state: adoCIAsk, reason: "a build policy's result has expired — queue its build again"}, true
	case status == "rejected":
		return adoCIItem{state: adoCIFailed, link: e.Link}, true
	case status == "approved":
		return adoCIItem{state: adoCIPassed}, true
	case status == "running":
		return adoCIItem{state: adoCIRunning}, true
	case status == "queued":
		return adoCIItem{state: adoCIQueued}, true
	case status == "broken":
		return adoCIItem{state: adoCIAsk, reason: "a CI policy evaluation is broken"}, true
	}
	return adoCIItem{state: adoCIAsk, reason: fmt.Sprintf("a CI policy evaluation reports status %q, which dross does not read either way", e.Status)}, true
}

// adoCIRollup reduces the builds at the head and the PR's evaluations to one
// verdict: fail > running > ask > queued > none > pass. Every counted item
// must succeed for a pass, as on GitHub; nothing counted is none, never a
// pass.
func adoCIRollup(builds []adoBuildObs, evals []adoEvalObs) CIResult {
	var items []adoCIItem
	for _, b := range builds {
		items = append(items, adoBuildItem(b))
	}
	for _, e := range evals {
		if item, ok := adoEvalItem(e); ok {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return CIResult{Verdict: CINone}
	}
	top := items[0]
	for _, it := range items[1:] {
		if it.state > top.state {
			top = it
		}
	}
	switch top.state {
	case adoCIFailed:
		return CIResult{Verdict: CIFail, Link: top.link}
	case adoCIRunning:
		return CIResult{Verdict: CIPending}
	case adoCIAsk:
		return CIResult{Verdict: CIAsk, Reason: top.reason}
	case adoCIQueued:
		return CIResult{Verdict: CIPending, Queued: true}
	}
	return CIResult{Verdict: CIPass}
}
