package ship

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// The live Azure DevOps smoke record (c-7) and the validator that keeps it
// honest. The record is testdata/ado_live_smoke.toml; its comments are the
// runbook. Everything here is test-local: nothing in the binary reads it.

// liveRecordPath is the committed record.
var liveRecordPath = filepath.Join("testdata", "ado_live_smoke.toml")

// liveReplyMarker is prtriage.ReplyMarker, as a literal: prtriage imports
// this package, so this package's tests cannot import it.
const liveReplyMarker = "## /dross-respond — rejected review comments"

// liveStepKeys are the c-7 steps, in order. Each appears exactly once.
var liveStepKeys = []string{
	"open", "dedupe", "comment", "comment-policy", "listing", "head",
	"user", "respond", "ci", "merge", "merged-status",
}

// liveStep is one step of the run. The typed fields are the evidence a pass
// needs; each step uses its own.
type liveStep struct {
	Key            string `toml:"key"`
	Result         string `toml:"result"`
	Evidence       string `toml:"evidence"`
	PRURL          string `toml:"pr_url"`
	InlinePath     string `toml:"inline_path"`
	InlineLine     int    `toml:"inline_line"`
	HeadRef        string `toml:"head_ref"`
	HeadOpen       *bool  `toml:"head_open"`
	HeadCrossRepo  *bool  `toml:"head_cross_repo"`
	UserID         string `toml:"user_id"`
	ThreadAuthorID string `toml:"thread_author_id"`
	ReplyBody      string `toml:"reply_body"`
	ReplyDropped   bool   `toml:"reply_dropped"`
	CIVerdict      string `toml:"ci_verdict"`
	BuildURL       string `toml:"build_url"`
	LsRemote       string `toml:"ls_remote"`
	MergedStatus   string `toml:"merged_status"`
}

// texts is every free-text field of the step, for the loopback screen.
func (s liveStep) texts() []string {
	return []string{s.Evidence, s.PRURL, s.InlinePath, s.HeadRef, s.UserID, s.ThreadAuthorID,
		s.ReplyBody, s.CIVerdict, s.BuildURL, s.LsRemote, s.MergedStatus}
}

// liveRecord is the whole record.
type liveRecord struct {
	Posture                string     `toml:"posture"`
	RunAt                  string     `toml:"run_at"`
	Phase                  string     `toml:"phase"`
	APIBase                string     `toml:"api_base"`
	APIVersion             string     `toml:"api_version"`
	StatuslessThreadBlocks *bool      `toml:"statusless_thread_blocks"`
	Steps                  []liveStep `toml:"step"`
}

var (
	// liveLoopback is mock evidence: an httptest server listens on loopback.
	liveLoopback = regexp.MustCompile(`(?i)localhost|127\.[0-9]+\.[0-9]+\.[0-9]+|\[::1\]`)
	// liveADOID is an Azure DevOps id in a step's evidence: a PR, build or
	// thread number, or an identity GUID. A URL counts only on api_base's
	// host (liveEvidenceNamesADO).
	liveADOID    = regexp.MustCompile(`(?i)\b(PR|build|thread) #?[1-9][0-9]*\b|#[1-9][0-9]*\b|\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	liveURL      = regexp.MustCompile(`https://\S+`)
	liveDedupe   = regexp.MustCompile(`found open PR #[1-9][0-9]*`)
	livePRURL    = regexp.MustCompile(`^https://\S+/_git/\S+/pullrequest/[1-9][0-9]*$`)
	liveCIOutput = regexp.MustCompile(`^ci: (pass|fail|pending|none|ask)\b`)
)

// liveEvidenceNamesADO is evidence that names an Azure DevOps id, or a URL on
// the run's own ADO host.
func liveEvidenceNamesADO(r liveRecord, evidence string) bool {
	if liveADOID.MatchString(evidence) {
		return true
	}
	base, err := url.Parse(r.APIBase)
	if err != nil || base.Host == "" {
		return false
	}
	for _, raw := range liveURL.FindAllString(evidence, -1) {
		if u, err := url.Parse(raw); err == nil && strings.EqualFold(u.Host, base.Host) {
			return true
		}
	}
	return false
}

// liveLsRemoteHas is ls-remote output listing exactly ref, not a ref it is a
// prefix of.
func liveLsRemoteHas(out, ref string) bool {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == ref {
			return true
		}
	}
	return false
}

// liveStepProof is what a step must show before its pass is believed.
func liveStepProof(r liveRecord, s liveStep) error {
	switch s.Key {
	case "open":
		if !livePRURL.MatchString(s.PRURL) {
			return fmt.Errorf("pr_url %q is not a PR's browser URL", s.PRURL)
		}
	case "dedupe":
		if !liveDedupe.MatchString(s.Evidence) {
			return errors.New("the re-run printed no 'found open PR #N' line")
		}
	case "comment-policy":
		if r.StatuslessThreadBlocks == nil {
			return errors.New("statusless_thread_blocks is not recorded")
		}
	case "listing":
		if s.InlinePath == "" || s.InlineLine <= 0 {
			return fmt.Errorf("no inline comment listed (path %q, line %d)", s.InlinePath, s.InlineLine)
		}
	case "head":
		if r.Phase == "" || s.HeadRef != "phase/"+r.Phase {
			return fmt.Errorf("head_ref %q is not phase/%s", s.HeadRef, r.Phase)
		}
		if s.HeadOpen == nil || !*s.HeadOpen {
			return errors.New("the head is not recorded open")
		}
		if s.HeadCrossRepo == nil || *s.HeadCrossRepo {
			return errors.New("the head is not recorded as this repo's own branch")
		}
	case "user":
		if s.UserID == "" || s.UserID != s.ThreadAuthorID {
			return fmt.Errorf("user_id %q does not equal dross's thread author id %q", s.UserID, s.ThreadAuthorID)
		}
	case "respond":
		if !strings.HasPrefix(s.ReplyBody, liveReplyMarker) {
			return errors.New("the reply thread does not start with the ReplyMarker")
		}
		if !s.ReplyDropped {
			return errors.New("the next listing still lists dross's own reply")
		}
	case "ci":
		if !liveCIOutput.MatchString(s.CIVerdict) {
			return fmt.Errorf("ci_verdict %q is not a `ci:` line", s.CIVerdict)
		}
		bu, err := url.Parse(s.BuildURL)
		base, berr := url.Parse(r.APIBase)
		if err != nil || berr != nil || bu.Scheme != "https" || bu.Host == "" || !strings.EqualFold(bu.Host, base.Host) {
			return fmt.Errorf("build_url %q is not on the ADO host of %q", s.BuildURL, r.APIBase)
		}
	case "merge":
		if r.Phase == "" || !liveLsRemoteHas(s.LsRemote, "refs/heads/phase/"+r.Phase) {
			return errors.New("ls_remote does not show phase/<phase> still on origin")
		}
	case "merged-status":
		if s.MergedStatus != "merged" {
			return fmt.Errorf("merged_status %q is not merged", s.MergedStatus)
		}
	}
	return nil
}

// validateLiveRecord accepts the record as the posture it can prove — the one
// it declares, or nothing. threadStatus is adoThreadStatus.
func validateLiveRecord(r liveRecord, threadStatus string) (string, error) {
	if liveLoopback.MatchString(r.APIBase) {
		return "", fmt.Errorf("api_base %q is a loopback address: mock evidence", r.APIBase)
	}
	seen := map[string]int{}
	ran, passed := 0, 0
	for _, s := range r.Steps {
		seen[s.Key]++
		for _, text := range s.texts() {
			if liveLoopback.MatchString(text) {
				return "", fmt.Errorf("step %s names a loopback address (%q): mock evidence", s.Key, text)
			}
		}
		switch s.Result {
		case "not-run":
			continue
		case "pass":
			passed++
			if !liveEvidenceNamesADO(r, s.Evidence) {
				return "", fmt.Errorf("step %s passed without evidence naming an ADO URL or id", s.Key)
			}
			if err := liveStepProof(r, s); err != nil {
				return "", fmt.Errorf("step %s passed on hollow evidence: %w", s.Key, err)
			}
		case "fail":
		default:
			return "", fmt.Errorf("step %s has result %q (want not-run, pass or fail)", s.Key, s.Result)
		}
		ran++
	}
	for _, k := range liveStepKeys {
		if seen[k] != 1 {
			return "", fmt.Errorf("step %s appears %d times, want exactly once", k, seen[k])
		}
		delete(seen, k)
	}
	for k := range seen {
		return "", fmt.Errorf("step %s is not a c-7 step", k)
	}
	if r.StatuslessThreadBlocks != nil && *r.StatuslessThreadBlocks && threadStatus == "" {
		return "", errors.New("a statusless thread blocks completion, yet adoThreadStatus still posts statusless threads")
	}

	ranLive := func() error {
		if r.RunAt == "" || r.APIVersion == "" || r.APIBase == "" || r.Phase == "" {
			return errors.New("a live run needs run_at, api_version, api_base and phase")
		}
		if _, err := time.Parse(time.RFC3339, r.RunAt); err != nil {
			return fmt.Errorf("run_at %q is not RFC 3339", r.RunAt)
		}
		return nil
	}
	switch r.Posture {
	case "mock-only":
		if ran > 0 || r.StatuslessThreadBlocks != nil {
			return "", errors.New("a mock-only record carries a live result")
		}
	case "partial":
		if err := ranLive(); err != nil {
			return "", err
		}
		if ran == 0 {
			return "", errors.New("a partial record has no step that ran")
		}
		if passed == len(liveStepKeys) {
			return "", errors.New("every step passed: that is live, not partial")
		}
	case "live":
		if err := ranLive(); err != nil {
			return "", err
		}
		if passed != len(liveStepKeys) {
			return "", fmt.Errorf("only %d of %d steps passed: that is partial at best", passed, len(liveStepKeys))
		}
	default:
		return "", fmt.Errorf("posture %q is not mock-only, partial or live", r.Posture)
	}
	return r.Posture, nil
}

// --- fixtures ---

func liveBool(b bool) *bool { return &b }

// completeLiveRecord is a run where every step passed with its evidence.
func completeLiveRecord() liveRecord {
	const user = "ab12cd34-0000-4000-8000-0000000000ff"
	return liveRecord{
		Posture:                "live",
		RunAt:                  "2026-11-02T10:00:00Z",
		Phase:                  "smoke",
		APIBase:                "https://dev.azure.com/acme",
		APIVersion:             "7.1",
		StatuslessThreadBlocks: liveBool(false),
		Steps: []liveStep{
			{Key: "open", Result: "pass", Evidence: "opened https://dev.azure.com/acme/p/_git/r/pullrequest/12", PRURL: "https://dev.azure.com/acme/p/_git/r/pullrequest/12"},
			{Key: "dedupe", Result: "pass", Evidence: "found open PR #12 for phase/smoke — recording it rather than opening a second"},
			{Key: "comment", Result: "pass", Evidence: "https://dev.azure.com/acme/p/_git/r/pullrequest/12?discussionId=3"},
			{Key: "comment-policy", Result: "pass", Evidence: "PR 12 Comment requirements: approved"},
			{Key: "listing", Result: "pass", Evidence: "PR 12: i400001 src/a.go:12 rev@corp.com", InlinePath: "src/a.go", InlineLine: 12},
			{Key: "head", Result: "pass", Evidence: "PR 12 head phase/smoke", HeadRef: "phase/smoke", HeadOpen: liveBool(true), HeadCrossRepo: liveBool(false)},
			{Key: "user", Result: "pass", Evidence: "connectionData id " + user, UserID: user, ThreadAuthorID: user},
			{Key: "respond", Result: "pass", Evidence: "posted thread 5 on PR 12", ReplyBody: liveReplyMarker, ReplyDropped: true},
			{Key: "ci", Result: "pass", Evidence: "build 88", CIVerdict: "ci: pass", BuildURL: "https://dev.azure.com/acme/p/_build/results?buildId=88"},
			{Key: "merge", Result: "pass", Evidence: "https://dev.azure.com/acme/p/_git/r/pullrequest/12", LsRemote: "0f3c9e1d\trefs/heads/phase/smoke"},
			{Key: "merged-status", Result: "pass", Evidence: "PR 12 merged", MergedStatus: "merged"},
		},
	}
}

// mockOnlyRecord is the template's shape: nothing has run.
func mockOnlyRecord() liveRecord {
	r := liveRecord{Posture: "mock-only"}
	for _, k := range liveStepKeys {
		r.Steps = append(r.Steps, liveStep{Key: k, Result: "not-run"})
	}
	return r
}

// stepOf is the record's step for key.
func stepOf(t *testing.T, r *liveRecord, key string) *liveStep {
	t.Helper()
	for i := range r.Steps {
		if r.Steps[i].Key == key {
			return &r.Steps[i]
		}
	}
	t.Fatalf("no step %s", key)
	return nil
}

// TestADOLiveRecordHonest: the record can claim live only with every step
// passed live, and the committed record is held to exactly that.
func TestADOLiveRecordHonest(t *testing.T) {
	t.Run("the committed record validates", func(t *testing.T) {
		var r liveRecord
		if _, err := toml.DecodeFile(liveRecordPath, &r); err != nil {
			t.Fatal(err)
		}
		if _, err := validateLiveRecord(r, adoThreadStatus); err != nil {
			t.Fatalf("%s: %v", liveRecordPath, err)
		}
	})

	t.Run("the validator is not vacuous", func(t *testing.T) {
		gap := completeLiveRecord()
		stepOf(t, &gap, "ci").Result = "not-run"
		if got, err := validateLiveRecord(gap, ""); err == nil {
			t.Errorf("live with a gap was accepted as %s", got)
		}
		if got, err := validateLiveRecord(mockOnlyRecord(), ""); err != nil || got != "mock-only" {
			t.Errorf("mock-only: got %q, %v; want accepted as mock-only", got, err)
		}
		if got, err := validateLiveRecord(completeLiveRecord(), ""); err != nil || got != "live" {
			t.Errorf("complete live: got %q, %v; want accepted as live", got, err)
		}
	})

	t.Run("live needs every step passed, with run_at and api_version", func(t *testing.T) {
		for _, k := range liveStepKeys {
			for _, result := range []string{"not-run", "fail"} {
				r := completeLiveRecord()
				stepOf(t, &r, k).Result = result
				if _, err := validateLiveRecord(r, ""); err == nil {
					t.Errorf("live with %s %s was accepted", k, result)
				}
				r.Posture = "partial"
				if got, err := validateLiveRecord(r, ""); err != nil || got != "partial" {
					t.Errorf("partial with %s %s: got %q, %v; want accepted as partial", k, result, got, err)
				}
			}
			r := completeLiveRecord()
			stepOf(t, &r, k).Evidence = ""
			if _, err := validateLiveRecord(r, ""); err == nil {
				t.Errorf("live with %s's evidence blank was accepted", k)
			}
		}
		for name, blank := range map[string]func(*liveRecord){
			"run_at":      func(r *liveRecord) { r.RunAt = "" },
			"api_version": func(r *liveRecord) { r.APIVersion = "" },
			"bad run_at":  func(r *liveRecord) { r.RunAt = "yesterday" },
		} {
			r := completeLiveRecord()
			blank(&r)
			if _, err := validateLiveRecord(r, ""); err == nil {
				t.Errorf("live without %s was accepted", name)
			}
		}
	})

	t.Run("partial is not a softer live, and mock-only ran nothing", func(t *testing.T) {
		r := completeLiveRecord()
		r.Posture = "partial"
		if _, err := validateLiveRecord(r, ""); err == nil {
			t.Error("partial with every step passed was accepted")
		}
		m := mockOnlyRecord()
		m.Posture = "partial"
		m.RunAt, m.Phase, m.APIBase, m.APIVersion = "2026-11-02T10:00:00Z", "smoke", "https://dev.azure.com/acme", "7.1"
		if _, err := validateLiveRecord(m, ""); err == nil {
			t.Error("partial with nothing run was accepted")
		}
		m = mockOnlyRecord()
		stepOf(t, &m, "open").Result = "fail"
		if _, err := validateLiveRecord(m, ""); err == nil {
			t.Error("mock-only with a step that ran was accepted")
		}
		m = mockOnlyRecord()
		m.Posture = "verified"
		if _, err := validateLiveRecord(m, ""); err == nil {
			t.Error("an unknown posture was accepted")
		}
	})

	t.Run("httptest evidence is mock evidence", func(t *testing.T) {
		for name, taint := range map[string]func(*testing.T, *liveRecord){
			"127.0.0.1 port": func(t *testing.T, r *liveRecord) {
				stepOf(t, r, "comment").Evidence = "http://127.0.0.1:41234/o/p/_git/r/pullrequest/12"
			},
			"localhost": func(t *testing.T, r *liveRecord) {
				stepOf(t, r, "open").Evidence = "opened http://localhost:8080/pullrequest/12"
			},
			"ipv6 loopback": func(t *testing.T, r *liveRecord) { stepOf(t, r, "merge").Evidence = "http://[::1]:5555/x 12" },
			"build url": func(t *testing.T, r *liveRecord) {
				stepOf(t, r, "ci").BuildURL = "https://127.0.0.1:39001/p/_build/results?buildId=1"
			},
			"api_base": func(t *testing.T, r *liveRecord) { r.APIBase = "http://127.0.0.1:39001/o" },
		} {
			r := completeLiveRecord()
			taint(t, &r)
			if _, err := validateLiveRecord(r, ""); err == nil {
				t.Errorf("%s: loopback evidence was accepted", name)
			}
		}
	})

	t.Run("every step exactly once", func(t *testing.T) {
		// Mock-only and partial records, which no pass count rejects, so only
		// the exactly-once rule can.
		partial := func() liveRecord {
			r := completeLiveRecord()
			r.Posture = "partial"
			stepOf(t, &r, "merged-status").Result = "not-run"
			return r
		}
		for _, fixture := range []func() liveRecord{mockOnlyRecord, partial} {
			if _, err := validateLiveRecord(fixture(), ""); err != nil {
				t.Fatalf("the base fixture is rejected: %v", err)
			}
			for i, k := range liveStepKeys {
				r := fixture()
				r.Steps = append(r.Steps[:i:i], r.Steps[i+1:]...)
				if got, err := validateLiveRecord(r, ""); err == nil {
					t.Errorf("a %s record without %s was accepted", got, k)
				}
				r = fixture()
				r.Steps = append(r.Steps, r.Steps[i])
				if got, err := validateLiveRecord(r, ""); err == nil {
					t.Errorf("a %s record with %s twice was accepted", got, k)
				}
			}
		}
		m := mockOnlyRecord()
		m.Steps = append(m.Steps, liveStep{Key: "extra", Result: "not-run"})
		if _, err := validateLiveRecord(m, ""); err == nil {
			t.Error("a record with an unknown step was accepted")
		}
	})

	t.Run("the statusless-thread fallback is not ignored", func(t *testing.T) {
		r := completeLiveRecord()
		r.StatuslessThreadBlocks = liveBool(true)
		if _, err := validateLiveRecord(r, ""); err == nil {
			t.Error("statusless_thread_blocks = true was accepted while adoThreadStatus is ''")
		}
		if got, err := validateLiveRecord(r, "closed"); err != nil || got != "live" {
			t.Errorf("with adoThreadStatus closed: got %q, %v; want accepted as live", got, err)
		}
	})

	t.Run("the runbook keeps its prerequisites", func(t *testing.T) {
		b, err := os.ReadFile(liveRecordPath)
		if err != nil {
			t.Fatal(err)
		}
		var comments []string
		for _, l := range strings.Split(string(b), "\n") {
			if c, ok := strings.CutPrefix(strings.TrimSpace(l), "#"); ok {
				comments = append(comments, c)
			}
		}
		runbook := strings.Join(comments, "\n")
		for what, phrases := range map[string][]string{
			"dross init on a scratch ADO repo":   {"scratch Azure DevOps repo", "`dross init`"},
			"a pipeline plus a runnable agent":   {"pipeline", "agent that can run it"},
			"a verify pass on phase/<id>":        {"phase/<id>", "/dross-verify pass"},
			"the hand-set resolution policy":     {"comment-resolution policy", "set by hand"},
			"a hand-posted inline comment":       {"inline comment posted by hand", "Before the listing step"},
			"reviewer threads resolved to merge": {"every reviewer thread resolved", "Before the merge step"},
		} {
			for _, p := range phrases {
				if !strings.Contains(runbook, p) {
					t.Errorf("the runbook lost %s: its comments no longer say %q", what, p)
				}
			}
		}
	})
}

// TestADOLiveRecordStepEvidence: a step's pass is believed only on the
// evidence that step exists to produce.
func TestADOLiveRecordStepEvidence(t *testing.T) {
	for name, hollow := range map[string]func(*testing.T, *liveRecord){
		"open without a PR URL":      func(t *testing.T, r *liveRecord) { stepOf(t, r, "open").PRURL = "" },
		"dedupe without the line":    func(t *testing.T, r *liveRecord) { stepOf(t, r, "dedupe").Evidence = "opened PR #13" },
		"comment-policy unrecorded":  func(t *testing.T, r *liveRecord) { r.StatuslessThreadBlocks = nil },
		"listing without a path":     func(t *testing.T, r *liveRecord) { stepOf(t, r, "listing").InlinePath = "" },
		"listing at line 0":          func(t *testing.T, r *liveRecord) { stepOf(t, r, "listing").InlineLine = 0 },
		"head on another branch":     func(t *testing.T, r *liveRecord) { stepOf(t, r, "head").HeadRef = "phase/other" },
		"head closed":                func(t *testing.T, r *liveRecord) { stepOf(t, r, "head").HeadOpen = liveBool(false) },
		"head open unrecorded":       func(t *testing.T, r *liveRecord) { stepOf(t, r, "head").HeadOpen = nil },
		"head cross-repo":            func(t *testing.T, r *liveRecord) { stepOf(t, r, "head").HeadCrossRepo = liveBool(true) },
		"head cross-repo unrecorded": func(t *testing.T, r *liveRecord) { stepOf(t, r, "head").HeadCrossRepo = nil },
		"user is not the author":     func(t *testing.T, r *liveRecord) { stepOf(t, r, "user").ThreadAuthorID = "someone-else" },
		"user without an id":         func(t *testing.T, r *liveRecord) { s := stepOf(t, r, "user"); s.UserID, s.ThreadAuthorID = "", "" },
		"reply without the marker":   func(t *testing.T, r *liveRecord) { stepOf(t, r, "respond").ReplyBody = "thanks, will fix" },
		"reply still listed":         func(t *testing.T, r *liveRecord) { stepOf(t, r, "respond").ReplyDropped = false },
		"ci without the verdict":     func(t *testing.T, r *liveRecord) { stepOf(t, r, "ci").CIVerdict = "" },
		"ci verdict paraphrased":     func(t *testing.T, r *liveRecord) { stepOf(t, r, "ci").CIVerdict = "CI looked green" },
		"ci build on another host": func(t *testing.T, r *liveRecord) {
			stepOf(t, r, "ci").BuildURL = "https://example.com/_build/results?buildId=88"
		},
		"ci build over http": func(t *testing.T, r *liveRecord) {
			stepOf(t, r, "ci").BuildURL = "http://dev.azure.com/acme/p/_build/results?buildId=88"
		},
		"merge with empty ls-remote": func(t *testing.T, r *liveRecord) { stepOf(t, r, "merge").LsRemote = "" },
		"merge with another branch":  func(t *testing.T, r *liveRecord) { stepOf(t, r, "merge").LsRemote = "0f3c\trefs/heads/main" },
		"merge with a longer branch": func(t *testing.T, r *liveRecord) { stepOf(t, r, "merge").LsRemote = "0f3c\trefs/heads/phase/smoke-2" },
		"evidence off the ADO host": func(t *testing.T, r *liveRecord) {
			stepOf(t, r, "comment").Evidence = "https://example.com/pullrequest/12"
		},
		"evidence naming nothing":     func(t *testing.T, r *liveRecord) { stepOf(t, r, "merged-status").Evidence = "merged at 10:42" },
		"merged-status not merged":    func(t *testing.T, r *liveRecord) { stepOf(t, r, "merged-status").MergedStatus = "open" },
		"merged-status without proof": func(t *testing.T, r *liveRecord) { stepOf(t, r, "merged-status").MergedStatus = "" },
	} {
		r := completeLiveRecord()
		hollow(t, &r)
		if got, err := validateLiveRecord(r, ""); err == nil {
			t.Errorf("%s: accepted as %s", name, got)
		}
	}
}
