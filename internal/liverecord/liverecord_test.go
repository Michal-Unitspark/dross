package liverecord

import (
	"errors"
	"strings"
	"testing"
)

var testKeys = []string{"create", "transition", "close"}

// complete is a run where every step passed with evidence on the run's host.
func complete() Record {
	return Record{
		Posture:    Live,
		RunAt:      "2026-11-03T10:00:00Z",
		APIBase:    "https://dev.azure.com/acme",
		APIVersion: "7.1",
		Required:   []Field{{Name: "process", Value: "Agile"}},
		Steps: []Step{
			{Key: "create", Result: Pass, Evidence: "https://dev.azure.com/acme/Fabrikam/_workitems/edit/41"},
			{Key: "transition", Result: Pass, Evidence: "work item 41 is Active"},
			{Key: "close", Result: Pass, Evidence: "PR #12 merged", Texts: []string{"Closed"}},
		},
	}
}

// mockOnly is a template: nothing has run.
func mockOnly() Record {
	r := Record{Posture: MockOnly}
	for _, k := range testKeys {
		r.Steps = append(r.Steps, Step{Key: k, Result: NotRun})
	}
	return r
}

// partial is a real run with one step not run.
func partial() Record {
	r := complete()
	r.Posture = Partial
	r.Steps[2].Result = NotRun
	return r
}

// TestValidatePostures: each posture is accepted only on what it can prove.
// The baselines are accepted as themselves, and every weakening of a rule is
// rejected.
func TestValidatePostures(t *testing.T) {
	for name, fixture := range map[string]func() Record{"live": complete, "mock-only": mockOnly, "partial": partial} {
		got, err := Validate(fixture(), testKeys, nil)
		if err != nil || got != fixture().Posture {
			t.Fatalf("%s baseline: got %q, %v; want accepted as %s", name, got, err, fixture().Posture)
		}
	}

	for name, weaken := range map[string]func(*Record){
		"live with one not-run step": func(r *Record) { r.Steps[1].Result = NotRun },
		"live with one failed step":  func(r *Record) { r.Steps[0].Result = Fail },
		"partial with every pass":    func(r *Record) { r.Posture = Partial },
		"mock-only carrying a pass": func(r *Record) {
			*r = mockOnly()
			r.Steps[0] = Step{Key: "create", Result: Pass, Evidence: "work item 41"}
		},
		"mock-only carrying a fail": func(r *Record) { *r = mockOnly(); r.Steps[2].Result = Fail },
		"partial with nothing run": func(r *Record) {
			m := mockOnly()
			r.Posture, r.Steps = Partial, m.Steps
		},
		"a step key twice":             func(r *Record) { r.Steps = append(r.Steps, r.Steps[0]) },
		"a step key missing":           func(r *Record) { r.Steps = r.Steps[:2] },
		"an unknown step key":          func(r *Record) { r.Steps = append(r.Steps, Step{Key: "extra", Result: NotRun}) },
		"an unknown result":            func(r *Record) { r.Steps[0].Result = "skipped" },
		"an unknown posture":           func(r *Record) { r.Posture = "verified" },
		"api_base on 127.0.0.1":        func(r *Record) { r.APIBase = "http://127.0.0.1:39001/acme" },
		"api_base on localhost":        func(r *Record) { r.APIBase = "https://localhost/acme" },
		"evidence naming 127.0.0.1":    func(r *Record) { r.Steps[0].Evidence = "http://127.0.0.1:41234/acme/_workitems/edit/41" },
		"evidence naming localhost":    func(r *Record) { r.Steps[1].Evidence = "work item 41 at http://localhost:8080" },
		"evidence naming [::1]":        func(r *Record) { r.Steps[2].Evidence = "http://[::1]:5555/x #12" },
		"a text naming loopback":       func(r *Record) { r.Steps[2].Texts = []string{"http://127.0.0.1:1/x"} },
		"a pass naming nothing":        func(r *Record) { r.Steps[1].Evidence = "moved at 10:42" },
		"a pass naming another host":   func(r *Record) { r.Steps[0].Evidence = "https://example.com/acme/_workitems/edit/41" },
		"a pass with blank evidence":   func(r *Record) { r.Steps[2].Evidence = "" },
		"run_at yesterday":             func(r *Record) { r.RunAt = "yesterday" },
		"run_at missing":               func(r *Record) { r.RunAt = "" },
		"api_version missing":          func(r *Record) { r.APIVersion = "" },
		"api_base missing":             func(r *Record) { r.APIBase = "" },
		"a required field missing":     func(r *Record) { r.Required[0].Value = "" },
		"partial without run_at":       func(r *Record) { *r = partial(); r.RunAt = "" },
		"partial without its required": func(r *Record) { *r = partial(); r.Required[0].Value = " " },
	} {
		r := complete()
		weaken(&r)
		if got, err := Validate(r, testKeys, nil); err == nil {
			t.Errorf("%s: accepted as %s", name, got)
		}
	}
}

// TestValidateRulesHoldWithoutAPass: the exactly-once and loopback rules are
// checked on records no pass-count or evidence rule would reject — a mock-only
// template and a partial run — so each case can fail only on the rule it names.
func TestValidateRulesHoldWithoutAPass(t *testing.T) {
	for _, fixture := range []func() Record{mockOnly, partial} {
		for i, k := range testKeys {
			r := fixture()
			r.Steps = append(r.Steps[:i:i], r.Steps[i+1:]...)
			if got, err := Validate(r, testKeys, nil); err == nil {
				t.Errorf("a %s record without %s was accepted", got, k)
			}
			r = fixture()
			r.Steps = append(r.Steps, Step{Key: k, Result: NotRun})
			if got, err := Validate(r, testKeys, nil); err == nil {
				t.Errorf("a %s record with %s twice was accepted", got, k)
			}
		}
	}
	for _, base := range []string{"http://127.0.0.1:39001/acme", "https://localhost/acme", "http://[::1]:5555/acme"} {
		r := mockOnly()
		r.APIBase = base
		if got, err := Validate(r, testKeys, nil); err == nil {
			t.Errorf("a %s record on api_base %s was accepted", got, base)
		}
	}
	r := mockOnly()
	r.Steps[0].Evidence = "planned against http://127.0.0.1:41234"
	if got, err := Validate(r, testKeys, nil); err == nil {
		t.Errorf("a %s record whose not-run step names loopback was accepted", got)
	}
}

// TestValidateRunsProofOnPassesOnly: the record's own proof decides a pass, is
// told which step it is judging, and never runs for a step that did not pass.
func TestValidateRunsProofOnPassesOnly(t *testing.T) {
	var judged []int
	proof := func(i int) error {
		judged = append(judged, i)
		return nil
	}
	if _, err := Validate(partial(), testKeys, proof); err != nil {
		t.Fatalf("partial: %v", err)
	}
	if len(judged) != 2 || judged[0] != 0 || judged[1] != 1 {
		t.Errorf("proof judged steps %v, want [0 1] (the two passes)", judged)
	}

	hollow := errors.New("no read-back")
	_, err := Validate(complete(), testKeys, func(i int) error {
		if i == 1 {
			return hollow
		}
		return nil
	})
	if !errors.Is(err, hollow) || !strings.Contains(err.Error(), "step transition") {
		t.Errorf("a failing proof must reject naming the step, got %v", err)
	}
}

// TestEvidenceNamesADO pins what counts as naming Azure DevOps.
func TestEvidenceNamesADO(t *testing.T) {
	const base = "https://acme.visualstudio.com"
	for _, ev := range []string{
		"PR #38", "build 50", "thread #89", "work item 41", "#7",
		"user 29be652f-4869-456c-be18-65c7a51a37c8",
		"see https://acme.visualstudio.com/Fabrikam/_workitems/edit/41",
		"see https://ACME.visualstudio.com/x",
	} {
		if !EvidenceNamesADO(base, ev) {
			t.Errorf("EvidenceNamesADO(%q) = false, want true", ev)
		}
	}
	for _, ev := range []string{
		"", "merged at 10:42", "https://example.com/_workitems/edit/41",
		"http://acme.visualstudio.com/x", "PR #0",
	} {
		if EvidenceNamesADO(base, ev) {
			t.Errorf("EvidenceNamesADO(%q) = true, want false", ev)
		}
	}
	if EvidenceNamesADO("not a url", "https://acme.visualstudio.com/x") {
		t.Error("a URL counted against an unparseable api_base")
	}
}
