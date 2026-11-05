package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Rivil/dross/internal/configenum"
)

// The process fixtures: each type's states in the order the API lists them.
var (
	scrumPBI   = []ADOState{{"New", "Proposed"}, {"Approved", "Proposed"}, {"Committed", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}}
	scrumTask  = []ADOState{{"To Do", "Proposed"}, {"In Progress", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}}
	agileStory = []ADOState{{"New", "Proposed"}, {"Active", "InProgress"}, {"Resolved", "Resolved"}, {"Closed", "Completed"}, {"Removed", "Removed"}}
	agileTask  = []ADOState{{"New", "Proposed"}, {"Active", "InProgress"}, {"Closed", "Completed"}, {"Removed", "Removed"}}
	agileEpic  = []ADOState{{"New", "Proposed"}, {"Active", "InProgress"}, {"Resolved", "Resolved"}, {"Closed", "Completed"}, {"Removed", "Removed"}}
	basicIssue = []ADOState{{"To Do", "Proposed"}, {"Doing", "InProgress"}, {"Done", "Completed"}}
	basicTask  = []ADOState{{"To Do", "Proposed"}, {"Doing", "InProgress"}, {"Done", "Completed"}}
	basicEpic  = []ADOState{{"To Do", "Proposed"}, {"Doing", "InProgress"}, {"Done", "Completed"}}
)

// processTypes names every fixture type, for the tables that sweep them all.
var processTypes = map[string][]ADOState{
	"Scrum Product Backlog Item": scrumPBI, "Scrum Task": scrumTask,
	"Agile User Story": agileStory, "Agile Task": agileTask, "Agile Epic": agileEpic,
	"Basic Issue": basicIssue, "Basic Task": basicTask, "Basic Epic": basicEpic,
}

// TestADODefaultStateResolution: every default resolves by category to the
// first state of that category in the type's own order — one rule, no
// per-process table.
func TestADODefaultStateResolution(t *testing.T) {
	for _, tc := range []struct {
		typ    string
		states []ADOState
		status string
		want   string
	}{
		{"Scrum PBI", scrumPBI, "in-progress", "Committed"},
		{"Scrum PBI", scrumPBI, "uat", "Committed"},
		{"Scrum PBI", scrumPBI, "planned", "New"},
		{"Scrum PBI", scrumPBI, "complete", "Done"},
		{"Scrum Task", scrumTask, "task-in-progress", "In Progress"},
		{"Scrum Task", scrumTask, "task-in-review", "In Progress"},
		{"Scrum Task", scrumTask, "task-complete", "Done"},
		{"Agile User Story", agileStory, "in-progress", "Active"},
		{"Agile User Story", agileStory, "uat", "Resolved"},
		{"Agile User Story", agileStory, "complete", "Closed"},
		{"Agile Task", agileTask, "task-in-review", "Active"},
		{"Agile Epic", agileEpic, "complete", "Closed"},
		{"Basic Issue", basicIssue, "in-progress", "Doing"},
		{"Basic Issue", basicIssue, "uat", "Doing"},
		{"Basic Issue", basicIssue, "complete", "Done"},
	} {
		got, err := resolveADOState(tc.status, tc.states, nil)
		if err != nil || got.Name != tc.want {
			t.Errorf("%s %s = %q, %v; want %q", tc.typ, tc.status, got.Name, err, tc.want)
		}
	}
}

// TestADODefaultsNeverRemoved: no default, on any process's type, lands in a
// Removed-category state.
func TestADODefaultsNeverRemoved(t *testing.T) {
	for typ, states := range processTypes {
		for _, status := range configenum.LifecycleStatuses.Values() {
			if status == "" {
				continue
			}
			got, err := resolveADOState(status, states, nil)
			if err != nil {
				t.Errorf("%s %s: %v", typ, status, err)
				continue
			}
			if got.Category == ADOCategoryRemoved {
				t.Errorf("%s %s resolves to %q, a Removed state", typ, status, got.Name)
			}
		}
	}
}

// adoStateItem is one work item the state server holds.
type adoStateItem struct {
	typ, state string
}

// adoStateServer serves work items and their types' states, applies each
// state PATCH unless told not to, and records what was written.
type adoStateServer struct {
	*httptest.Server
	mu          sync.Mutex
	items       map[string]*adoStateItem
	types       map[string][]ADOState
	patchStatus int    // 0 = 200
	patchBody   string // answered on a refused PATCH
	stick       bool   // accept the PATCH and change nothing
	readBack500 bool   // after the first PATCH, every item GET is a 500
	written     []string
	patched     bool
}

func newADOStateServer(t *testing.T, items map[string]*adoStateItem, types map[string][]ADOState) *adoStateServer {
	t.Helper()
	s := &adoStateServer{items: items, types: types}
	s.Server, _ = adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		path := r.URL.EscapedPath()
		if typ, ok := strings.CutPrefix(path, adoProjectAPI+"/wit/workitemtypes/"); ok && strings.HasSuffix(typ, "/states") {
			name, _ := url.PathUnescape(strings.TrimSuffix(typ, "/states"))
			states, ok := s.types[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var vals []map[string]string
			for _, st := range states {
				vals = append(vals, map[string]string{"name": st.Name, "category": st.Category})
			}
			b, _ := json.Marshal(map[string]any{"count": len(vals), "value": vals})
			_, _ = w.Write(b)
			return
		}
		id, ok := strings.CutPrefix(path, adoProjectAPI+"/wit/workitems/")
		item := s.items[id]
		if !ok || item == nil {
			t.Errorf("unexpected request %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if s.readBack500 && s.patched {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			b, _ := json.Marshal(map[string]any{"id": json.Number(id), "rev": 1,
				"fields": map[string]string{"System.WorkItemType": item.typ, "System.State": item.state}})
			_, _ = w.Write(b)
		case http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			var ops []struct {
				Op, Path string
				Value    string
			}
			_ = json.Unmarshal(raw, &ops)
			s.patched = true
			for _, op := range ops {
				if op.Path == "/fields/System.State" {
					s.written = append(s.written, op.Value)
					if s.patchStatus == 0 && !s.stick {
						item.state = op.Value
					}
				}
			}
			if s.patchStatus != 0 {
				w.WriteHeader(s.patchStatus)
				_, _ = w.Write([]byte(s.patchBody))
				return
			}
			_, _ = w.Write([]byte(`{"id":` + id + `}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
		}
	})
	return s
}

// stateClient is a client on s.
func stateClient(t *testing.T, s *adoStateServer) *AzureBoardsClient {
	t.Helper()
	return newTestAzureBoards(t, s.Server, nil)
}

// TestADOSetStateOverride: [board].state_map overrides by state name, and an
// override the type cannot honour warns and writes nothing.
func TestADOSetStateOverride(t *testing.T) {
	t.Run("Approved on a PBI", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Product Backlog Item", "New"}},
			map[string][]ADOState{"Product Backlog Item": scrumPBI})
		if err := stateClient(t, s).SetState("42", "in-progress", map[string]string{"in-progress": "Approved"}); err != nil {
			t.Fatal(err)
		}
		if len(s.written) != 1 || s.written[0] != "Approved" {
			t.Errorf("wrote %v, want [Approved]", s.written)
		}
	})
	t.Run("an override beats the default", func(t *testing.T) {
		for status, override := range map[string]string{"in-progress": "Doing", "planned": "Doing"} {
			s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Issue", "Done"}},
				map[string][]ADOState{"Issue": basicIssue})
			if err := stateClient(t, s).SetState("42", status, map[string]string{status: override}); err != nil {
				t.Fatal(err)
			}
			if len(s.written) != 1 || s.written[0] != override {
				t.Errorf("%s with override %q wrote %v", status, override, s.written)
			}
		}
	})
	t.Run("a state the type lacks", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Task", "To Do"}},
			map[string][]ADOState{"Task": scrumTask})
		var err error
		out := captureStderrForge(t, func() {
			err = stateClient(t, s).SetState("42", "task-in-review", map[string]string{"task-in-review": "In Review"})
		})
		if err != nil {
			t.Fatalf("an unhonourable override failed the sync: %v", err)
		}
		if len(s.written) != 0 {
			t.Errorf("wrote %v, want nothing", s.written)
		}
		for _, want := range []string{"task-in-review", "42", "In Review"} {
			if !strings.Contains(out, want) {
				t.Errorf("warning %q does not name %q", out, want)
			}
		}
	})
}

// TestADOSetStateUnreachable: a state board sync cannot reach warns and
// returns nil; a server failure is an error; an item already there is left.
func TestADOSetStateUnreachable(t *testing.T) {
	noProposed := []ADOState{{"Active", "InProgress"}, {"Closed", "Completed"}}
	t.Run("no Proposed state", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Custom", "Active"}}, map[string][]ADOState{"Custom": noProposed})
		var err error
		out := captureStderrForge(t, func() { err = stateClient(t, s).SetState("42", "planned", nil) })
		if err != nil || len(s.written) != 0 || !strings.Contains(out, "warning") {
			t.Errorf("err %v, wrote %v, stderr %q; want a warning, nil and no write", err, s.written, out)
		}
	})
	t.Run("a 400 rule rejection", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Product Backlog Item", "New"}}, map[string][]ADOState{"Product Backlog Item": scrumPBI})
		s.patchStatus, s.patchBody = http.StatusBadRequest, `{"message":"TF401320: Rule Error for field Effort. Error code: Required."}`
		var err error
		out := captureStderrForge(t, func() { err = stateClient(t, s).SetState("42", "in-progress", nil) })
		if err != nil || !strings.Contains(out, "warning") || !strings.Contains(out, "42") {
			t.Errorf("a 400 = %v, stderr %q; want a warning naming the item and nil", err, out)
		}
	})
	t.Run("a rule rejection past the message cap", func(t *testing.T) {
		// customProperties can precede message in an Azure DevOps error body,
		// pushing the code past the 500 bytes an error message quotes.
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Product Backlog Item", "New"}}, map[string][]ADOState{"Product Backlog Item": scrumPBI})
		s.patchStatus = http.StatusBadRequest
		s.patchBody = `{"customProperties":{"pad":"` + strings.Repeat("x", 600) + `"},"message":"TF401320: Rule Error for field State."}`
		var err error
		out := captureStderrForge(t, func() { err = stateClient(t, s).SetState("42", "in-progress", nil) })
		if err != nil || !strings.Contains(out, "warning") {
			t.Errorf("a TF401320 past byte 500 = %v, stderr %q; want a warning and nil — the check reads the whole body", err, out)
		}
	})
	for name, body := range map[string]string{
		"an unknown field":  `{"message":"TF51535: Cannot find field System.Stat."}`,
		"a malformed patch": `{"message":"You must pass a valid patch document in the body of the request."}`,
	} {
		t.Run("a 400 that is not a rule rejection: "+name, func(t *testing.T) {
			s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Product Backlog Item", "New"}}, map[string][]ADOState{"Product Backlog Item": scrumPBI})
			s.patchStatus, s.patchBody = http.StatusBadRequest, body
			var err error
			out := captureStderrForge(t, func() { err = stateClient(t, s).SetState("42", "in-progress", nil) })
			if err == nil || !strings.Contains(err.Error(), "work item 42") || strings.Contains(out, "warning") {
				t.Errorf("a non-rule 400 = %v, stderr %q; want an error naming work item 42 and no warning", err, out)
			}
		})
	}
	t.Run("a 500", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Product Backlog Item", "New"}}, map[string][]ADOState{"Product Backlog Item": scrumPBI})
		s.patchStatus, s.patchBody = http.StatusInternalServerError, `oops`
		if err := stateClient(t, s).SetState("42", "in-progress", nil); err == nil {
			t.Error("a 500 on the state write returned nil")
		}
	})
	t.Run("already there", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Product Backlog Item", "Committed"}}, map[string][]ADOState{"Product Backlog Item": scrumPBI})
		if err := stateClient(t, s).SetState("42", "in-progress", nil); err != nil || s.patched {
			t.Errorf("err %v, patched %v; want no PATCH for an item already in Committed", err, s.patched)
		}
	})
	t.Run("an unreadable states list is an error", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"Ghost", "New"}}, map[string][]ADOState{})
		if err := stateClient(t, s).SetState("42", "in-progress", nil); err == nil {
			t.Error("a 404 on the type's states read as a mapping gap")
		}
	})
}

// TestADOCloseIssueAsVerifies: a close lands in a Completed state, or errors
// naming the item — before the write when the target is wrong, after it when
// the read-back disagrees.
func TestADOCloseIssueAsVerifies(t *testing.T) {
	pbi := func() map[string]*adoStateItem {
		return map[string]*adoStateItem{"42": {"Product Backlog Item", "Committed"}}
	}
	types := map[string][]ADOState{"Product Backlog Item": scrumPBI, "User Story": agileStory}

	t.Run("read back Done", func(t *testing.T) {
		s := newADOStateServer(t, pbi(), types)
		if err := stateClient(t, s).CloseIssueAs("42", "complete", nil); err != nil || len(s.written) != 1 || s.written[0] != "Done" {
			t.Errorf("err %v, wrote %v; want nil after writing Done", err, s.written)
		}
	})
	t.Run("CloseIssue is complete", func(t *testing.T) {
		s := newADOStateServer(t, pbi(), types)
		if err := stateClient(t, s).CloseIssue("42"); err != nil || len(s.written) != 1 || s.written[0] != "Done" {
			t.Errorf("err %v, wrote %v; want Done", err, s.written)
		}
	})
	t.Run("a 200 that did not take", func(t *testing.T) {
		s := newADOStateServer(t, pbi(), types)
		s.stick = true
		err := stateClient(t, s).CloseIssueAs("42", "complete", nil)
		if err == nil || !strings.Contains(err.Error(), "work item 42") || !strings.Contains(err.Error(), "Done") {
			t.Errorf("a close that read back Committed = %v; want an error naming work item 42 and Done", err)
		}
	})
	t.Run("a read-back 500", func(t *testing.T) {
		s := newADOStateServer(t, pbi(), types)
		s.readBack500 = true
		if err := stateClient(t, s).CloseIssueAs("42", "complete", nil); err == nil {
			t.Error("an unreadable read-back reported a close")
		}
	})
	for name, override := range map[string]map[string]string{
		"an override to a Resolved state": {"complete": "Resolved"},
		"a blank override":                {"complete": ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := newADOStateServer(t, map[string]*adoStateItem{"42": {"User Story", "Active"}}, types)
			err := stateClient(t, s).CloseIssueAs("42", "complete", override)
			if err == nil || !strings.Contains(err.Error(), "42") {
				t.Errorf("CloseIssueAs = %v; want an error naming 42", err)
			}
			if s.patched {
				t.Error("a close to a non-Completed target was written")
			}
		})
	}
}

// TestADOSetStateRawLiteral: undo writes the literal state, verifies it, and
// treats a refusal as an error.
func TestADOSetStateRawLiteral(t *testing.T) {
	types := map[string][]ADOState{"User Story": agileStory}
	t.Run("verbatim", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"User Story", "Closed"}}, types)
		if err := stateClient(t, s).SetStateRaw("42", "Resolved"); err != nil {
			t.Fatal(err)
		}
		if len(s.written) != 1 || s.written[0] != "Resolved" {
			t.Errorf("wrote %v, want [Resolved]", s.written)
		}
	})
	t.Run("a read-back that disagrees", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"User Story", "Closed"}}, types)
		s.stick = true
		if err := stateClient(t, s).SetStateRaw("42", "Resolved"); err == nil || !strings.Contains(err.Error(), "42") {
			t.Errorf("SetStateRaw over a write that did not take = %v; want an error naming 42", err)
		}
	})
	t.Run("a 400 is an error", func(t *testing.T) {
		s := newADOStateServer(t, map[string]*adoStateItem{"42": {"User Story", "Closed"}}, types)
		s.patchStatus, s.patchBody = http.StatusBadRequest, `{"message":"TF401320"}`
		if err := stateClient(t, s).SetStateRaw("42", "Resolved"); err == nil {
			t.Error("a refused raw write returned nil")
		}
	})
}
