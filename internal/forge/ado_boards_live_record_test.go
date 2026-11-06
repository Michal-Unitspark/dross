package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/Rivil/dross/internal/liverecord"
)

// The live Azure Boards smoke record (c-8) and the validator that keeps it
// honest. The record is testdata/ado_boards_live_smoke.toml; its comments are
// the runbook. Everything here is test-local: nothing in the binary reads it.
// The rules every live record shares are liverecord.Validate; the per-step
// proofs below are this record's own.

// boardsLiveRecordPath is the committed record.
var boardsLiveRecordPath = filepath.Join("testdata", "ado_boards_live_smoke.toml")

// boardsLiveStepKeys are the c-8 steps, in order. Each appears exactly once.
var boardsLiveStepKeys = []string{"create", "transition", "close", "parent-link", "pull"}

// boardsLiveItem is one work item the create step made.
type boardsLiveItem struct {
	ID   int    `toml:"id"`
	Type string `toml:"type"`
	URL  string `toml:"url"`
}

// boardsLiveLink is one parent link the parent-link step read back.
type boardsLiveLink struct {
	Child  int    `toml:"child"`
	Parent int    `toml:"parent"`
	Rel    string `toml:"rel"`
}

// boardsLiveStep is one step of the run; each step uses its own typed fields.
type boardsLiveStep struct {
	Key         string           `toml:"key"`
	Result      string           `toml:"result"`
	Evidence    string           `toml:"evidence"`
	Epic        *boardsLiveItem  `toml:"epic"`
	Requirement *boardsLiveItem  `toml:"requirement"`
	Task        *boardsLiveItem  `toml:"task"`
	WorkItem    int              `toml:"work_item"`
	FromState   string           `toml:"from_state"`
	State       string           `toml:"state"`
	Category    string           `toml:"category"`
	Links       []boardsLiveLink `toml:"links"`
	Envelope    string           `toml:"envelope"`
	BugID       int              `toml:"bug_id"`
}

// texts is every free-text field of the step, for the loopback screen.
func (s boardsLiveStep) texts() []string {
	out := []string{s.FromState, s.State, s.Category, s.Envelope}
	for _, it := range []*boardsLiveItem{s.Epic, s.Requirement, s.Task} {
		if it != nil {
			out = append(out, it.Type, it.URL)
		}
	}
	for _, l := range s.Links {
		out = append(out, l.Rel)
	}
	return out
}

// boardsLiveRecord is the whole record.
type boardsLiveRecord struct {
	Posture    string           `toml:"posture"`
	RunAt      string           `toml:"run_at"`
	APIBase    string           `toml:"api_base"`
	APIVersion string           `toml:"api_version"`
	Process    string           `toml:"process"`
	Steps      []boardsLiveStep `toml:"step"`
}

// generic converts the record into the shape liverecord.Validate judges. The
// steps keep their order, so a Proof's index addresses r.Steps directly.
func (r boardsLiveRecord) generic() liverecord.Record {
	g := liverecord.Record{
		Posture:    r.Posture,
		RunAt:      r.RunAt,
		APIBase:    r.APIBase,
		APIVersion: r.APIVersion,
		Required:   []liverecord.Field{{Name: "process", Value: r.Process}},
	}
	for _, s := range r.Steps {
		g.Steps = append(g.Steps, liverecord.Step{Key: s.Key, Result: s.Result, Evidence: s.Evidence, Texts: s.texts()})
	}
	return g
}

// boardsProcessRequirement is the requirement backlog type of each stock
// process. An inherited process names its own; for one of these the create
// step's requirement item must carry exactly this type.
var boardsProcessRequirement = map[string]string{
	"agile": "User Story", "scrum": "Product Backlog Item", "basic": "Issue", "cmmi": "Requirement",
}

// created is the create step's three items — only when that step passed, so
// its proof has checked them. A failed or unrun create anchors nothing: no
// later step can pass on ids nobody proved.
func (r boardsLiveRecord) created() (epic, req, task *boardsLiveItem) {
	for _, s := range r.Steps {
		if s.Key == "create" && s.Result == liverecord.Pass {
			return s.Epic, s.Requirement, s.Task
		}
	}
	return nil, nil, nil
}

// createdIDs is every id the create step recorded.
func (r boardsLiveRecord) createdIDs() map[int]bool {
	ids := map[int]bool{}
	epic, req, task := r.created()
	for _, it := range []*boardsLiveItem{epic, req, task} {
		if it != nil && it.ID > 0 {
			ids[it.ID] = true
		}
	}
	return ids
}

// boardsItemURL checks that raw is item id's browser URL on apiBase's host.
func boardsItemURL(apiBase, raw string, id int) error {
	u, err := url.Parse(raw)
	base, berr := url.Parse(apiBase)
	if err != nil || berr != nil || u.Scheme != "https" || u.Host == "" || !strings.EqualFold(u.Host, base.Host) {
		return fmt.Errorf("url %q is not on the ADO host of %q", raw, apiBase)
	}
	if !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/_workitems/edit/"+strconv.Itoa(id)) {
		return fmt.Errorf("url %q is not work item %d's _workitems/edit page", raw, id)
	}
	return nil
}

// boardsCreateProof: three distinct items, typed Epic / requirement / Task,
// each with its own browser URL on the run's host.
func boardsCreateProof(r boardsLiveRecord, s boardsLiveStep) error {
	roles := []struct {
		name string
		item *boardsLiveItem
	}{{"epic", s.Epic}, {"requirement", s.Requirement}, {"task", s.Task}}
	seen := map[int]bool{}
	for _, role := range roles {
		it := role.item
		if it == nil || it.ID <= 0 {
			return fmt.Errorf("no %s id recorded", role.name)
		}
		if seen[it.ID] {
			return fmt.Errorf("the %s reuses id %d: the three items must be distinct", role.name, it.ID)
		}
		seen[it.ID] = true
		if err := boardsItemURL(r.APIBase, it.URL, it.ID); err != nil {
			return fmt.Errorf("%s: %w", role.name, err)
		}
	}
	if s.Epic.Type != "Epic" {
		return fmt.Errorf("the epic is typed %q, not Epic", s.Epic.Type)
	}
	if s.Task.Type != "Task" {
		return fmt.Errorf("the task is typed %q, not Task", s.Task.Type)
	}
	rt := strings.TrimSpace(s.Requirement.Type)
	if rt == "" || strings.EqualFold(rt, "Epic") || strings.EqualFold(rt, "Task") {
		return fmt.Errorf("the requirement is typed %q, not a requirement backlog type", rt)
	}
	if want, ok := boardsProcessRequirement[strings.ToLower(strings.TrimSpace(r.Process))]; ok && rt != want {
		return fmt.Errorf("the requirement is typed %q, but the %s process's requirement type is %s", rt, r.Process, want)
	}
	return nil
}

// boardsWorkItemProof: a transition or close names an item dross created.
func boardsWorkItemProof(r boardsLiveRecord, s boardsLiveStep) error {
	if s.WorkItem <= 0 || !r.createdIDs()[s.WorkItem] {
		return fmt.Errorf("work_item %d is not one the create step recorded", s.WorkItem)
	}
	if strings.TrimSpace(s.State) == "" {
		return errors.New("no read-back state recorded")
	}
	return nil
}

// boardsLiveStepProof is what a step must show before its pass is believed.
func boardsLiveStepProof(r boardsLiveRecord, s boardsLiveStep) error {
	switch s.Key {
	case "create":
		return boardsCreateProof(r, s)
	case "transition":
		if err := boardsWorkItemProof(r, s); err != nil {
			return err
		}
		if strings.TrimSpace(s.FromState) == "" || strings.EqualFold(s.FromState, s.State) {
			return fmt.Errorf("read-back state %q does not differ from the created state %q", s.State, s.FromState)
		}
		if s.Category != ADOCategoryInProgress {
			return fmt.Errorf("read-back category %q is not %s", s.Category, ADOCategoryInProgress)
		}
	case "close":
		if err := boardsWorkItemProof(r, s); err != nil {
			return err
		}
		if s.Category != ADOCategoryCompleted {
			return fmt.Errorf("read-back category %q is not %s: a Resolved or Removed item is not closed", s.Category, ADOCategoryCompleted)
		}
	case "parent-link":
		epic, req, task := r.created()
		if epic == nil || req == nil || task == nil {
			return errors.New("the create step recorded no items to chain")
		}
		for _, want := range [][2]int{{task.ID, req.ID}, {req.ID, epic.ID}} {
			found := false
			for _, l := range s.Links {
				found = found || (l.Child == want[0] && l.Parent == want[1] && l.Rel == adoRelParent)
			}
			if !found {
				return fmt.Errorf("no %s link records %d under %d", adoRelParent, want[0], want[1])
			}
		}
	case "pull":
		return boardsPullProof(r, s)
	}
	return nil
}

// boardsPullProof: the envelope parsed, carries "error": null, lists the
// hand-filed Bug and none of dross's own items.
func boardsPullProof(r boardsLiveRecord, s boardsLiveStep) error {
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s.Envelope), &env); err != nil {
		return fmt.Errorf("envelope is not the --json envelope: %v", err)
	}
	if e, ok := env["error"]; !ok || strings.TrimSpace(string(e)) != "null" {
		return fmt.Errorf(`envelope does not carry "error": null`)
	}
	var issues []Issue
	if err := json.Unmarshal(env["issues"], &issues); err != nil {
		return fmt.Errorf("envelope issues are not a list: %v", err)
	}
	created := r.createdIDs()
	if s.BugID <= 0 || created[s.BugID] {
		return fmt.Errorf("bug_id %d is not a hand-filed item", s.BugID)
	}
	listed := false
	for _, iss := range issues {
		if created[iss.Number] {
			return fmt.Errorf("the pull lists dross's own work item %d", iss.Number)
		}
		listed = listed || iss.Number == s.BugID
	}
	if !listed {
		return fmt.Errorf("the pull does not list the hand-filed Bug %d", s.BugID)
	}
	return nil
}

// validateBoardsLiveRecord accepts the record as the posture it can prove —
// the one it declares, or nothing.
func validateBoardsLiveRecord(r boardsLiveRecord) (string, error) {
	return liverecord.Validate(r.generic(), boardsLiveStepKeys, func(i int) error {
		return boardsLiveStepProof(r, r.Steps[i])
	})
}

// --- fixtures ---

// completeBoardsRecord is a run where every step passed with its evidence.
func completeBoardsRecord() boardsLiveRecord {
	const base = "https://dev.azure.com/acme"
	item := func(id int, typ string) *boardsLiveItem {
		return &boardsLiveItem{ID: id, Type: typ, URL: base + "/Fabrikam/_workitems/edit/" + strconv.Itoa(id)}
	}
	return boardsLiveRecord{
		Posture:    liverecord.Live,
		RunAt:      "2026-11-03T10:00:00Z",
		APIBase:    base,
		APIVersion: "7.1",
		Process:    "Scrum",
		Steps: []boardsLiveStep{
			{Key: "create", Result: liverecord.Pass, Evidence: "created work item 40, 41 and 42",
				Epic: item(40, "Epic"), Requirement: item(41, "Product Backlog Item"), Task: item(42, "Task")},
			{Key: "transition", Result: liverecord.Pass, Evidence: "work item 42 is In Progress",
				WorkItem: 42, FromState: "To Do", State: "In Progress", Category: ADOCategoryInProgress},
			{Key: "close", Result: liverecord.Pass, Evidence: "work item 42 is Done",
				WorkItem: 42, State: "Done", Category: ADOCategoryCompleted},
			{Key: "parent-link", Result: liverecord.Pass, Evidence: "work item 42 under 41 under 40",
				Links: []boardsLiveLink{{Child: 42, Parent: 41, Rel: adoRelParent}, {Child: 41, Parent: 40, Rel: adoRelParent}}},
			{Key: "pull", Result: liverecord.Pass, Evidence: "pull listed work item 50",
				Envelope: `{"issues":[{"Number":50,"Key":"50","Title":"Login 500s","State":"open"}],"error":null}`, BugID: 50},
		},
	}
}

// mockOnlyBoardsRecord is the template's shape: nothing has run.
func mockOnlyBoardsRecord() boardsLiveRecord {
	r := boardsLiveRecord{Posture: liverecord.MockOnly}
	for _, k := range boardsLiveStepKeys {
		r.Steps = append(r.Steps, boardsLiveStep{Key: k, Result: liverecord.NotRun})
	}
	return r
}

// boardsStep is the record's step for key.
func boardsStep(t *testing.T, r *boardsLiveRecord, key string) *boardsLiveStep {
	t.Helper()
	for i := range r.Steps {
		if r.Steps[i].Key == key {
			return &r.Steps[i]
		}
	}
	t.Fatalf("no step %s", key)
	return nil
}

// TestADOBoardsLiveRecordHonest: the record can claim live only with every
// step passed live, and the committed record is held to exactly that.
func TestADOBoardsLiveRecordHonest(t *testing.T) {
	t.Run("the committed record validates as live", func(t *testing.T) {
		var r boardsLiveRecord
		if _, err := toml.DecodeFile(boardsLiveRecordPath, &r); err != nil {
			t.Fatal(err)
		}
		got, err := validateBoardsLiveRecord(r)
		if err != nil {
			t.Fatalf("%s: %v", boardsLiveRecordPath, err)
		}
		// The live run of 2026-11-04 (t-21) replaced the mock-only template,
		// and this line with it; a later run that loses a step must say so
		// here too, in the same commit.
		if got != liverecord.Live {
			t.Errorf("%s validates as %s, want the live run t-21 recorded", boardsLiveRecordPath, got)
		}
	})

	t.Run("the validator is not vacuous", func(t *testing.T) {
		gap := completeBoardsRecord()
		boardsStep(t, &gap, "pull").Result = liverecord.NotRun
		if got, err := validateBoardsLiveRecord(gap); err == nil {
			t.Errorf("live with a gap was accepted as %s", got)
		}
		if got, err := validateBoardsLiveRecord(mockOnlyBoardsRecord()); err != nil || got != liverecord.MockOnly {
			t.Errorf("mock-only: got %q, %v; want accepted as mock-only", got, err)
		}
		if got, err := validateBoardsLiveRecord(completeBoardsRecord()); err != nil || got != liverecord.Live {
			t.Errorf("complete live: got %q, %v; want accepted as live", got, err)
		}
	})

	t.Run("live needs every step passed, with run_at, api_version, api_base and process", func(t *testing.T) {
		for _, k := range boardsLiveStepKeys {
			for _, result := range []string{liverecord.NotRun, liverecord.Fail} {
				r := completeBoardsRecord()
				boardsStep(t, &r, k).Result = result
				if _, err := validateBoardsLiveRecord(r); err == nil {
					t.Errorf("live with %s %s was accepted", k, result)
				}
				r.Posture = liverecord.Partial
				if k == "create" {
					// Every later step acts on the created items, so a run whose
					// create did not pass has nothing to run them on.
					for i := range r.Steps[1:] {
						r.Steps[i+1] = boardsLiveStep{Key: r.Steps[i+1].Key, Result: liverecord.NotRun}
					}
					if result == liverecord.NotRun {
						// …and then nothing ran at all: that is not a partial run.
						if got, err := validateBoardsLiveRecord(r); err == nil {
							t.Errorf("partial with nothing run was accepted as %s", got)
						}
						continue
					}
				}
				if got, err := validateBoardsLiveRecord(r); err != nil || got != liverecord.Partial {
					t.Errorf("partial with %s %s: got %q, %v; want accepted as partial", k, result, got, err)
				}
			}
		}
		for name, blank := range map[string]func(*boardsLiveRecord){
			"run_at":         func(r *boardsLiveRecord) { r.RunAt = "" },
			"api_version":    func(r *boardsLiveRecord) { r.APIVersion = "" },
			"api_base":       func(r *boardsLiveRecord) { r.APIBase = "" },
			"process":        func(r *boardsLiveRecord) { r.Process = "" },
			"a valid run_at": func(r *boardsLiveRecord) { r.RunAt = "this morning" },
		} {
			r := completeBoardsRecord()
			blank(&r)
			if _, err := validateBoardsLiveRecord(r); err == nil {
				t.Errorf("live without %s was accepted", name)
			}
		}
	})

	t.Run("partial is not a softer live", func(t *testing.T) {
		r := completeBoardsRecord()
		r.Posture = liverecord.Partial
		if _, err := validateBoardsLiveRecord(r); err == nil {
			t.Error("partial with every step passed was accepted")
		}
		m := mockOnlyBoardsRecord()
		boardsStep(t, &m, "create").Result = liverecord.Fail
		if _, err := validateBoardsLiveRecord(m); err == nil {
			t.Error("mock-only with a step that ran was accepted")
		}
	})

	t.Run("httptest evidence is mock evidence", func(t *testing.T) {
		for name, taint := range map[string]func(*testing.T, *boardsLiveRecord){
			"127.0.0.1 evidence": func(t *testing.T, r *boardsLiveRecord) {
				boardsStep(t, r, "create").Evidence = "http://127.0.0.1:41234/acme/Fabrikam/_workitems/edit/41"
			},
			"localhost evidence": func(t *testing.T, r *boardsLiveRecord) {
				boardsStep(t, r, "transition").Evidence = "work item 42 at http://localhost:8080"
			},
			"[::1] evidence": func(t *testing.T, r *boardsLiveRecord) {
				boardsStep(t, r, "close").Evidence = "http://[::1]:5555/x work item 42"
			},
			"loopback item url": func(t *testing.T, r *boardsLiveRecord) {
				boardsStep(t, r, "create").Task.URL = "https://127.0.0.1:39001/acme/Fabrikam/_workitems/edit/42"
			},
			"loopback envelope": func(t *testing.T, r *boardsLiveRecord) {
				boardsStep(t, r, "pull").Envelope = `{"issues":[{"Number":50,"URL":"http://localhost/50"}],"error":null}`
			},
			"loopback api_base": func(t *testing.T, r *boardsLiveRecord) { r.APIBase = "http://127.0.0.1:39001/acme" },
		} {
			r := completeBoardsRecord()
			taint(t, &r)
			if _, err := validateBoardsLiveRecord(r); err == nil {
				t.Errorf("%s: loopback evidence was accepted", name)
			}
		}
	})

	t.Run("every step exactly once", func(t *testing.T) {
		for i, k := range boardsLiveStepKeys {
			r := mockOnlyBoardsRecord()
			r.Steps = append(r.Steps[:i:i], r.Steps[i+1:]...)
			if got, err := validateBoardsLiveRecord(r); err == nil {
				t.Errorf("a %s record without %s was accepted", got, k)
			}
			r = mockOnlyBoardsRecord()
			r.Steps = append(r.Steps, r.Steps[i])
			if got, err := validateBoardsLiveRecord(r); err == nil {
				t.Errorf("a %s record with %s twice was accepted", got, k)
			}
		}
	})

	t.Run("the runbook keeps its prerequisites", func(t *testing.T) {
		b, err := os.ReadFile(boardsLiveRecordPath)
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
			"dross init on a scratch ADO project": {"scratch Azure DevOps project", "`dross init`"},
			"the azure-devops board provider":     {`provider = "azure-devops"`, "base_url", "project", "auth_env"},
			"the process":                         {"process", "requirement type"},
			"the hand-filed Bug":                  {"Bug filed by hand", "Before the pull step"},
			"make install first":                  {"`make install`"},
		} {
			for _, p := range phrases {
				if !strings.Contains(runbook, p) {
					t.Errorf("the runbook lost %s: its comments no longer say %q", what, p)
				}
			}
		}
	})
}

// TestADOBoardsLiveRecordStepEvidence: a step's pass is believed only on the
// evidence that step exists to produce.
func TestADOBoardsLiveRecordStepEvidence(t *testing.T) {
	for name, hollow := range map[string]func(*testing.T, *boardsLiveRecord){
		// create
		"create without a task": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "create").Task = nil },
		"create with a reused id": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "create")
			s.Task.ID, s.Task.URL = 41, "https://dev.azure.com/acme/Fabrikam/_workitems/edit/41"
		},
		"create with the epic typed Feature": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "create").Epic.Type = "Feature" },
		"create with the task typed Bug":     func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "create").Task.Type = "Bug" },
		"create with a guessed User Story":   func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "create").Requirement.Type = "User Story" },
		"create with the requirement a Task": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "create").Requirement.Type = "Task" },
		"create url on another host": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "create").Epic.URL = "https://example.com/acme/Fabrikam/_workitems/edit/40"
		},
		"create url over http": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "create").Epic.URL = "http://dev.azure.com/acme/Fabrikam/_workitems/edit/40"
		},
		"create url for another item": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "create").Requirement.URL = "https://dev.azure.com/acme/Fabrikam/_workitems/edit/40"
		},
		"create url that is an API url": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "create").Task.URL = "https://dev.azure.com/acme/_apis/wit/workItems/42"
		},
		// transition
		"transition with no state change": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "transition")
			s.State = s.FromState
		},
		"transition without the created state": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "transition").FromState = "" },
		"transition to a Proposed state": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "transition").Category = ADOCategoryProposed
		},
		"transition to a Completed state": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "transition")
			s.State, s.Category = "Done", ADOCategoryCompleted
		},
		"transition on an item dross did not create": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "transition").WorkItem = 99 },
		// close
		"close read back Resolved": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "close")
			s.State, s.Category = "Resolved", ADOCategoryResolved
		},
		"close read back Removed": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "close")
			s.State, s.Category = "Removed", ADOCategoryRemoved
		},
		"close without a read-back":             func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "close").State = "" },
		"close on an item dross did not create": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "close").WorkItem = 0 },
		// parent-link
		"parent-link missing the task's parent": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "parent-link")
			s.Links = s.Links[1:]
		},
		"parent-link missing the epic": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "parent-link")
			s.Links = s.Links[:1]
		},
		"parent-link as Related": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "parent-link").Links[0].Rel = adoRelRelated
		},
		"parent-link reversed": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "parent-link")
			for i := range s.Links {
				s.Links[i].Child, s.Links[i].Parent = s.Links[i].Parent, s.Links[i].Child
			}
		},
		"parent-link skipping the phase": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "parent-link").Links = []boardsLiveLink{{Child: 42, Parent: 40, Rel: adoRelParent}}
		},
		// pull
		"pull with an error": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "pull").Envelope = `{"issues":[{"Number":50}],"error":"board unreachable"}`
		},
		"pull without an error key": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "pull").Envelope = `{"issues":[{"Number":50}]}`
		},
		"pull that is not JSON": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "pull").Envelope = "1 new issue(s) to triage: 50 Login 500s"
		},
		"pull missing the Bug": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "pull").Envelope = `{"issues":[{"Number":51}],"error":null}`
		},
		"pull listing a dross item": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "pull").Envelope = `{"issues":[{"Number":50},{"Number":41}],"error":null}`
		},
		"pull with the Bug one of dross's": func(t *testing.T, r *boardsLiveRecord) {
			s := boardsStep(t, r, "pull")
			s.BugID, s.Envelope = 42, `{"issues":[{"Number":42}],"error":null}`
		},
		"pull without a bug_id": func(t *testing.T, r *boardsLiveRecord) { boardsStep(t, r, "pull").BugID = 0 },
		// shared
		"close evidence naming nothing": func(t *testing.T, r *boardsLiveRecord) {
			boardsStep(t, r, "close").Evidence = "closed at 10:42"
		},
	} {
		// Each case is named for the step it hollows out, and must be refused
		// BY that step: a case some other step happens to reject pins nothing.
		r := completeBoardsRecord()
		hollow(t, &r)
		got, err := validateBoardsLiveRecord(r)
		if err == nil {
			t.Errorf("%s: accepted as %s", name, got)
			continue
		}
		if want := "step " + strings.Fields(name)[0] + " "; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refused by the wrong rule: %v", name, err)
		}
	}

	// A create that did not pass anchors nothing: a partial run whose create
	// failed cannot pass transition on the ids it recorded.
	p := completeBoardsRecord()
	p.Posture = liverecord.Partial
	boardsStep(t, &p, "create").Result = liverecord.Fail
	if got, err := validateBoardsLiveRecord(p); err == nil || !strings.Contains(err.Error(), "step transition ") {
		t.Errorf("transition on a failed create's ids: got %q, %v; want refused by transition", got, err)
	}

	// An inherited process names its own requirement type; any type that is
	// not Epic or Task is accepted for it.
	r := completeBoardsRecord()
	r.Process = "Fabrikam Agile"
	boardsStep(t, &r, "create").Requirement.Type = "Customer Request"
	if got, err := validateBoardsLiveRecord(r); err != nil || got != liverecord.Live {
		t.Errorf("an inherited process's requirement type: got %q, %v; want accepted as live", got, err)
	}
}
