// Package liverecord holds the honesty rules every live-smoke record shares.
//
// A live smoke run against a real Azure DevOps org is recorded beside the
// mocked httptest suite and never substitutes for it (locked verify_posture).
// Each record — the ship loop's, the Boards backend's — is a TOML file whose
// comments are the runbook and whose test decodes it into its own test-local
// struct with its own per-step proofs. What they share lives here, so a rule
// tightened for one record is tightened for all: the three postures, the
// loopback screen, exactly-once step keys, an RFC 3339 run_at, and "a pass
// names an ADO id or a URL on the run's own host".
//
// The types carry no serialization tags on purpose. Nothing decodes into them
// and nothing in the binary reads a record: each test converts its own shape.
package liverecord

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The postures a record may declare.
const (
	// MockOnly is nothing having run live: every step is not-run.
	MockOnly = "mock-only"
	// Partial is a live run where at least one step did not pass.
	Partial = "partial"
	// Live is every step passed live, each with its evidence.
	Live = "live"
)

// The results a step may carry.
const (
	NotRun = "not-run"
	Pass   = "pass"
	Fail   = "fail"
)

// Field is a record-specific value a run must have filled in before it claims
// to have run (the ship record's phase, the Boards record's process).
type Field struct {
	Name  string
	Value string
}

// Step is one step of a run.
type Step struct {
	Key      string
	Result   string // NotRun | Pass | Fail
	Evidence string // what the step printed, verbatim
	// Texts is every other free-text field of the step. It is screened for
	// loopback addresses alongside Evidence.
	Texts []string
}

// Record is a run in the shape Validate judges.
type Record struct {
	Posture    string
	RunAt      string // RFC 3339
	APIBase    string // the org or collection URL the run talked to
	APIVersion string
	// Required are the record's own fields a run that happened must carry.
	Required []Field
	Steps    []Step
}

// Proof is a record's own check on step i (an index into Record.Steps) before
// its pass is believed. It runs only for a step whose result is Pass and whose
// evidence already names ADO.
type Proof func(i int) error

var (
	// loopback is mock evidence: an httptest server listens on loopback.
	loopback = regexp.MustCompile(`(?i)localhost|127\.[0-9]+\.[0-9]+\.[0-9]+|\[::1\]`)
	// adoID is an Azure DevOps id in a step's evidence: a PR, build, thread or
	// work-item number, or an identity GUID. A URL counts only on api_base's
	// host (EvidenceNamesADO).
	adoID  = regexp.MustCompile(`(?i)\b(PR|build|thread|work item) #?[1-9][0-9]*\b|#[1-9][0-9]*\b|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	webURL = regexp.MustCompile(`https://\S+`)
)

// IsLoopback reports whether s names a loopback address — mock evidence.
func IsLoopback(s string) bool { return loopback.MatchString(s) }

// EvidenceNamesADO reports whether evidence names an Azure DevOps id, or an
// https URL on apiBase's own host.
func EvidenceNamesADO(apiBase, evidence string) bool {
	if adoID.MatchString(evidence) {
		return true
	}
	return URLOnHost(apiBase, evidence)
}

// URLOnHost reports whether text holds an https URL on apiBase's host.
func URLOnHost(apiBase, text string) bool {
	base, err := url.Parse(apiBase)
	if err != nil || base.Host == "" {
		return false
	}
	for _, raw := range webURL.FindAllString(text, -1) {
		if u, err := url.Parse(raw); err == nil && strings.EqualFold(u.Host, base.Host) {
			return true
		}
	}
	return false
}

// Validate accepts rec as the posture it can prove — the one it declares, or
// nothing. keys are the record's steps; each must appear exactly once and no
// other may. proof may be nil.
func Validate(rec Record, keys []string, proof Proof) (string, error) {
	if IsLoopback(rec.APIBase) {
		return "", fmt.Errorf("api_base %q is a loopback address: mock evidence", rec.APIBase)
	}
	seen := map[string]int{}
	ran, passed := 0, 0
	for i, s := range rec.Steps {
		seen[s.Key]++
		for _, text := range append([]string{s.Evidence}, s.Texts...) {
			if IsLoopback(text) {
				return "", fmt.Errorf("step %s names a loopback address (%q): mock evidence", s.Key, text)
			}
		}
		switch s.Result {
		case NotRun:
			continue
		case Pass:
			passed++
			if !EvidenceNamesADO(rec.APIBase, s.Evidence) {
				return "", fmt.Errorf("step %s passed without evidence naming an ADO URL or id", s.Key)
			}
			if proof != nil {
				if err := proof(i); err != nil {
					return "", fmt.Errorf("step %s passed on hollow evidence: %w", s.Key, err)
				}
			}
		case Fail:
		default:
			return "", fmt.Errorf("step %s has result %q (want %s, %s or %s)", s.Key, s.Result, NotRun, Pass, Fail)
		}
		ran++
	}
	for _, k := range keys {
		if seen[k] != 1 {
			return "", fmt.Errorf("step %s appears %d times, want exactly once", k, seen[k])
		}
		delete(seen, k)
	}
	for k := range seen {
		return "", fmt.Errorf("step %s is not one of this record's steps (%s)", k, strings.Join(keys, ", "))
	}

	switch rec.Posture {
	case MockOnly:
		if ran > 0 {
			return "", errors.New("a mock-only record carries a live result")
		}
	case Partial:
		if err := ranLive(rec); err != nil {
			return "", err
		}
		if ran == 0 {
			return "", errors.New("a partial record has no step that ran")
		}
		if passed == len(keys) {
			return "", errors.New("every step passed: that is live, not partial")
		}
	case Live:
		if err := ranLive(rec); err != nil {
			return "", err
		}
		if passed != len(keys) {
			return "", fmt.Errorf("only %d of %d steps passed: that is partial at best", passed, len(keys))
		}
	default:
		return "", fmt.Errorf("posture %q is not %s, %s or %s", rec.Posture, MockOnly, Partial, Live)
	}
	return rec.Posture, nil
}

// ranLive is what a record that claims a run happened must carry: when, where,
// at which api-version, and its own required fields.
func ranLive(rec Record) error {
	missing := []string{}
	for _, f := range append([]Field{
		{"run_at", rec.RunAt}, {"api_version", rec.APIVersion}, {"api_base", rec.APIBase},
	}, rec.Required...) {
		if strings.TrimSpace(f.Value) == "" {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("a run that happened needs %s", strings.Join(missing, ", "))
	}
	if _, err := time.Parse(time.RFC3339, rec.RunAt); err != nil {
		return fmt.Errorf("run_at %q is not RFC 3339", rec.RunAt)
	}
	return nil
}
