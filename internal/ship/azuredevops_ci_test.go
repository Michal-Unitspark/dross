package ship

import (
	"strings"
	"testing"
)

const (
	adoTypeBuild    = "0609b952-1397-4640-95ec-e00a01b2c241"
	adoTypeStatus   = "cbdc66da-9728-4af8-aada-9a5a32e4a226"
	adoTypeRequired = "fd2167ab-b0be-447a-8ec8-39368250530e"
	adoTypeMinimum  = "fa4e907d-c16b-4a4c-9dfa-4906e5d171dd"
	adoTypeComments = "c6a1889d-b943-4856-b76f-9e46bb6b0df2"
	adoTypeWorkItem = "40e92b44-2fe1-4dd6-b3d8-74a9c21d0c6e"
	adoTypeStrategy = "fa4e907d-c16b-4a4c-9dfa-4916e5d171ab"
)

// rollupRow is one rollup fixture: the items, and the verdict every ordering
// of them must give.
type rollupRow struct {
	name   string
	builds []adoBuildObs
	evals  []adoEvalObs
	want   CIResult
}

func obsBuild(status, result, link string) adoBuildObs {
	return adoBuildObs{Status: status, Result: result, Link: link}
}

func obsEval(typeID, status, link string) adoEvalObs {
	return adoEvalObs{TypeID: typeID, Name: "policy", Status: status, Blocking: true, Link: link}
}

// orderings is every ordering of xs.
func orderings[T any](xs []T) [][]T {
	if len(xs) <= 1 {
		return [][]T{append([]T(nil), xs...)}
	}
	var out [][]T
	for i := range xs {
		rest := append(append([]T(nil), xs[:i]...), xs[i+1:]...)
		for _, p := range orderings(rest) {
			out = append(out, append([]T{xs[i]}, p...))
		}
	}
	return out
}

// checkADORollup runs each row under every ordering of its builds and of its
// evaluations, and wants the same verdict — and, where the row names them,
// the same Link, Queued and Reason — from each.
func checkADORollup(t *testing.T, rows []rollupRow) {
	t.Helper()
	for _, r := range rows {
		for _, bs := range orderings(r.builds) {
			for _, es := range orderings(r.evals) {
				got := adoCIRollup(bs, es)
				if got.Verdict != r.want.Verdict || got.Queued != r.want.Queued || got.Link != r.want.Link {
					t.Errorf("%s: got %+v, want %+v (builds %v, evals %v)", r.name, got, r.want, bs, es)
				}
				if r.want.Reason != "" && !strings.Contains(got.Reason, r.want.Reason) {
					t.Errorf("%s: reason %q, want it to mention %q", r.name, got.Reason, r.want.Reason)
				}
				if got.Verdict == CIAsk && got.Reason == "" {
					t.Errorf("%s: an ask without a reason", r.name)
				}
			}
		}
	}
}

// TestADORollupFail: any failed build or rejected CI evaluation is a fail,
// whatever else is green, running or queued — and links that item.
func TestADORollupFail(t *testing.T) {
	ok, running, queued := obsBuild("completed", "succeeded", "ok"), obsBuild("inProgress", "", "run"), obsBuild("notStarted", "", "q")
	approved := obsEval(adoTypeBuild, "approved", "ev")
	var rows []rollupRow
	for _, result := range []string{"failed", "canceled", "partiallySucceeded"} {
		rows = append(rows, rollupRow{
			name:   "build " + result,
			builds: []adoBuildObs{ok, running, queued, obsBuild("completed", result, "bad")},
			evals:  []adoEvalObs{approved},
			want:   CIResult{Verdict: CIFail, Link: "bad"},
		})
	}
	for _, typeID := range []string{adoTypeBuild, adoTypeStatus} {
		rows = append(rows, rollupRow{
			name:   "rejected " + typeID,
			builds: []adoBuildObs{ok, running, queued},
			evals:  []adoEvalObs{approved, obsEval(typeID, "rejected", "bad")},
			want:   CIResult{Verdict: CIFail, Link: "bad"},
		})
	}
	optional := obsEval(adoTypeBuild, "rejected", "bad")
	optional.Blocking = false
	rows = append(rows, rollupRow{
		name:   "rejected optional build policy",
		builds: []adoBuildObs{ok},
		evals:  []adoEvalObs{optional},
		want:   CIResult{Verdict: CIFail, Link: "bad"},
	})
	// An expired rejection is about an earlier head — but a fresh failure
	// beside it still outranks the ask, and links the fresh one.
	expiredRejected := obsEval(adoTypeBuild, "rejected", "stale")
	expiredRejected.Expired = true
	rows = append(rows, rollupRow{
		name:   "fresh failure beside an expired rejection",
		builds: []adoBuildObs{obsBuild("completed", "failed", "bad")},
		evals:  []adoEvalObs{expiredRejected, obsEval(adoTypeStatus, "queued", "")},
		want:   CIResult{Verdict: CIFail, Link: "bad"},
	})
	checkADORollup(t, rows)
}

// TestADORollupNonCI: reviewer, comment and work-item policies gate the merge
// but are not CI, whatever their display name says; a Build type id counts
// under any display name.
func TestADORollupNonCI(t *testing.T) {
	ok := obsBuild("completed", "succeeded", "ok")
	var rows []rollupRow
	for _, typeID := range []string{adoTypeRequired, adoTypeMinimum, adoTypeComments, adoTypeWorkItem} {
		rows = append(rows,
			rollupRow{name: "alone " + typeID, evals: []adoEvalObs{obsEval(typeID, "rejected", "x")}, want: CIResult{Verdict: CINone}},
			rollupRow{name: "beside a build " + typeID, builds: []adoBuildObs{ok}, evals: []adoEvalObs{obsEval(typeID, "rejected", "x")}, want: CIResult{Verdict: CIPass}},
		)
	}
	namedBuild := obsEval(adoTypeRequired, "rejected", "x")
	namedBuild.Name = "Build"
	compilacion := obsEval(adoTypeBuild, "rejected", "bad")
	compilacion.Name = "Compilacion"
	rows = append(rows,
		rollupRow{name: "reviewer policy named Build", builds: []adoBuildObs{ok}, evals: []adoEvalObs{namedBuild}, want: CIResult{Verdict: CIPass}},
		rollupRow{name: "build policy named Compilacion", builds: []adoBuildObs{ok}, evals: []adoEvalObs{compilacion}, want: CIResult{Verdict: CIFail, Link: "bad"}},
		rollupRow{name: "merge strategy policy", evals: []adoEvalObs{obsEval(adoTypeStrategy, "rejected", "x")}, want: CIResult{Verdict: CINone}},
	)
	checkADORollup(t, rows)
}

// TestADORollupNotApplicable: an evaluation that does not apply is not
// counted, so alone it is none and beside a green build it is a pass.
func TestADORollupNotApplicable(t *testing.T) {
	na := obsEval(adoTypeBuild, "notApplicable", "x")
	checkADORollup(t, []rollupRow{
		{name: "lone notApplicable", evals: []adoEvalObs{na}, want: CIResult{Verdict: CINone}},
		{name: "notApplicable beside a build", builds: []adoBuildObs{obsBuild("completed", "succeeded", "ok")}, evals: []adoEvalObs{na}, want: CIResult{Verdict: CIPass}},
		{name: "notApplicable status policy beside an approved one", evals: []adoEvalObs{obsEval(adoTypeStatus, "notApplicable", ""), obsEval(adoTypeBuild, "approved", "")}, want: CIResult{Verdict: CIPass}},
	})
}

// TestADORollupUnfinished: running and queued are pending — told apart by
// Queued — and an expired, broken or unknown state is an ask; none of them is
// ever a pass, and nothing counted is none.
func TestADORollupUnfinished(t *testing.T) {
	expired := obsEval(adoTypeBuild, "queued", "")
	expired.Expired = true
	expiredApproved := obsEval(adoTypeBuild, "approved", "")
	expiredApproved.Expired = true
	expiredRejected := obsEval(adoTypeBuild, "rejected", "stale")
	expiredRejected.Expired = true
	checkADORollup(t, []rollupRow{
		{name: "running build, approved evaluation", builds: []adoBuildObs{obsBuild("inProgress", "", "")}, evals: []adoEvalObs{obsEval(adoTypeBuild, "approved", "")}, want: CIResult{Verdict: CIPending}},
		{name: "queued and in progress", builds: []adoBuildObs{obsBuild("notStarted", "", ""), obsBuild("inProgress", "", "")}, want: CIResult{Verdict: CIPending}},
		{name: "queued build and running evaluation", builds: []adoBuildObs{obsBuild("notStarted", "", "")}, evals: []adoEvalObs{obsEval(adoTypeBuild, "running", "")}, want: CIResult{Verdict: CIPending}},
		{name: "cancelling build", builds: []adoBuildObs{obsBuild("cancelling", "", "")}, want: CIResult{Verdict: CIPending}},
		{name: "notStarted build", builds: []adoBuildObs{obsBuild("notStarted", "", "")}, want: CIResult{Verdict: CIPending, Queued: true}},
		{name: "postponed build", builds: []adoBuildObs{obsBuild("postponed", "", "")}, want: CIResult{Verdict: CIPending, Queued: true}},
		{name: "queued evaluation", evals: []adoEvalObs{obsEval(adoTypeBuild, "queued", "")}, want: CIResult{Verdict: CIPending, Queued: true}},
		{name: "queued beside green", builds: []adoBuildObs{obsBuild("completed", "succeeded", "")}, evals: []adoEvalObs{obsEval(adoTypeStatus, "queued", "")}, want: CIResult{Verdict: CIPending, Queued: true}},
		{name: "expired while queued", evals: []adoEvalObs{expired}, want: CIResult{Verdict: CIAsk, Reason: "expired"}},
		{name: "expired beside a queued build", builds: []adoBuildObs{obsBuild("notStarted", "", "")}, evals: []adoEvalObs{expired}, want: CIResult{Verdict: CIAsk, Reason: "expired"}},
		{name: "expired though approved", builds: []adoBuildObs{obsBuild("completed", "succeeded", "")}, evals: []adoEvalObs{expiredApproved}, want: CIResult{Verdict: CIAsk, Reason: "expired"}},
		{name: "expired rejection is not this head's fail", builds: []adoBuildObs{obsBuild("completed", "succeeded", "")}, evals: []adoEvalObs{expiredRejected}, want: CIResult{Verdict: CIAsk, Reason: "expired"}},
		{name: "broken evaluation", builds: []adoBuildObs{obsBuild("completed", "succeeded", "")}, evals: []adoEvalObs{obsEval(adoTypeBuild, "broken", "")}, want: CIResult{Verdict: CIAsk, Reason: "broken"}},
		{name: "unknown evaluation status", evals: []adoEvalObs{obsEval(adoTypeStatus, "weird", "")}, want: CIResult{Verdict: CIAsk, Reason: "weird"}},
		{name: "unknown build result", builds: []adoBuildObs{obsBuild("completed", "none", ""), obsBuild("completed", "succeeded", "")}, want: CIResult{Verdict: CIAsk, Reason: "none"}},
		{name: "unknown build status", builds: []adoBuildObs{obsBuild("all", "", "")}, want: CIResult{Verdict: CIAsk, Reason: "all"}},
		{name: "running outranks ask", builds: []adoBuildObs{obsBuild("inProgress", "", ""), obsBuild("completed", "none", "")}, want: CIResult{Verdict: CIPending}},
		{name: "ask outranks queued", builds: []adoBuildObs{obsBuild("notStarted", "", ""), obsBuild("completed", "none", "")}, want: CIResult{Verdict: CIAsk, Reason: "none"}},
		{name: "nothing counted", want: CIResult{Verdict: CINone}},
		{name: "all green", builds: []adoBuildObs{obsBuild("Completed", "Succeeded", "")}, evals: []adoEvalObs{obsEval(adoTypeBuild, "approved", ""), obsEval(adoTypeStatus, "Approved", "")}, want: CIResult{Verdict: CIPass}},
	})
}

// TestADOPolicyClass: CI and the merge gates ship names are told apart by
// their full type id, in any case; anything else is neither.
func TestADOPolicyClass(t *testing.T) {
	for _, tc := range []struct {
		typeID string
		kind   adoPolicyKind
		name   string
	}{
		{adoTypeBuild, adoPolicyCI, "Build"},
		{adoTypeStatus, adoPolicyCI, "Status"},
		{adoTypeRequired, adoPolicyBlocker, "Required reviewers"},
		{adoTypeMinimum, adoPolicyBlocker, "Minimum number of reviewers"},
		{adoTypeComments, adoPolicyBlocker, "Comment requirements"},
		{adoTypeWorkItem, adoPolicyBlocker, "Work item linking"},
		{strings.ToUpper(adoTypeMinimum), adoPolicyBlocker, "Minimum number of reviewers"},
		{strings.ToUpper(adoTypeBuild), adoPolicyCI, "Build"},
		{adoTypeStrategy, adoPolicyOther, ""},
		{"fa4e907d-c16b-4a4c-9dfa", adoPolicyOther, ""},
		{adoTypeMinimum + "0", adoPolicyOther, ""},
		{"00000000-0000-0000-0000-000000000000", adoPolicyOther, ""},
		{"", adoPolicyOther, ""},
	} {
		kind, name := adoPolicyClass(tc.typeID)
		if kind != tc.kind || name != tc.name {
			t.Errorf("%q: got (%d, %q), want (%d, %q)", tc.typeID, kind, name, tc.kind, tc.name)
		}
	}
}
