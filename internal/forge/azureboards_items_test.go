package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// adoItemsCall is one request the items server saw.
type adoItemsCall struct {
	method, path, contentType string
	ops                       []map[string]any // a JSON-patch body
	body                      map[string]any   // any other JSON body
}

// adoItemsServer is a small Azure Boards: a process (backlog type, type
// names, each type's states), a store of work items by id, and the WIQL and
// workitemsbatch reads over it. Every request is recorded.
type adoItemsServer struct {
	*httptest.Server
	mu          sync.Mutex
	calls       []adoItemsCall
	requirement string
	typeNames   []string
	states      map[string][]ADOState
	items       map[int]map[string]any // id -> fields
	wiqlIDs     []int                  // what WIQL answers; nil = every stored id
	nextID      int
	failWIQL    int // status for WIQL, 0 = ok
	failTypes   int // status for the type-name read, 0 = ok
	failBatchAt int // the 1-based batch that fails with a 500, 0 = none
	batches     int
	nullInBatch map[int]bool // ids the batch answers as null
}

func newADOItemsServer(t *testing.T, requirement string, typeNames ...string) *adoItemsServer {
	t.Helper()
	s := &adoItemsServer{
		requirement: requirement, typeNames: typeNames, items: map[int]map[string]any{}, nextID: 500,
		states: map[string][]ADOState{
			"User Story": agileStory, "Task": agileTask, "Epic": agileEpic, "Bug": agileStory,
			"Product Backlog Item": scrumPBI, "Issue": basicIssue,
		},
	}
	s.Server, _ = adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		call := adoItemsCall{method: r.Method, path: r.URL.EscapedPath(), contentType: r.Header.Get("Content-Type")}
		_ = json.Unmarshal(raw, &call.ops)
		_ = json.Unmarshal(raw, &call.body)
		s.calls = append(s.calls, call)
		w.Header().Set("Content-Type", "application/json")
		s.serve(t, w, r, call)
	})
	return s
}

func (s *adoItemsServer) serve(t *testing.T, w http.ResponseWriter, r *http.Request, call adoItemsCall) {
	p := strings.TrimPrefix(call.path, adoProjectAPI)
	switch {
	case r.Method == "GET" && p == "/work/backlogconfiguration":
		_, _ = io.WriteString(w, backlogConfig(s.requirement))
	case r.Method == "GET" && p == "/wit/workitemtypes":
		if s.failTypes != 0 {
			w.WriteHeader(s.failTypes)
			return
		}
		var vals []map[string]string
		for _, n := range s.typeNames {
			vals = append(vals, map[string]string{"name": n})
		}
		b, _ := json.Marshal(map[string]any{"value": vals})
		_, _ = w.Write(b)
	case r.Method == "GET" && strings.HasPrefix(p, "/wit/workitemtypes/") && strings.HasSuffix(p, "/states"):
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(p, "/wit/workitemtypes/"), "/states"), "%20", " ")
		var vals []map[string]string
		for _, st := range s.states[name] {
			vals = append(vals, map[string]string{"name": st.Name, "category": st.Category})
		}
		b, _ := json.Marshal(map[string]any{"value": vals})
		_, _ = w.Write(b)
	case r.Method == "POST" && strings.HasPrefix(p, "/wit/workitems/$"):
		typ := strings.ReplaceAll(strings.TrimPrefix(p, "/wit/workitems/$"), "%20", " ")
		s.nextID++
		fields := map[string]any{"System.WorkItemType": typ, "System.State": s.states[typ][0].Name}
		applyOps(fields, call.ops)
		s.items[s.nextID] = fields
		_, _ = w.Write(itemJSON(s.nextID, fields))
	case r.Method == "PATCH" && strings.HasPrefix(p, "/wit/workitems/"):
		var id int
		fmt.Sscanf(strings.TrimPrefix(p, "/wit/workitems/"), "%d", &id)
		fields, ok := s.items[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		applyOps(fields, call.ops)
		_, _ = w.Write(itemJSON(id, fields))
	case r.Method == "GET" && strings.HasPrefix(p, "/wit/workitems/"):
		var id int
		fmt.Sscanf(strings.TrimPrefix(p, "/wit/workitems/"), "%d", &id)
		fields, ok := s.items[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(itemJSON(id, fields))
	case r.Method == "POST" && p == "/wit/wiql":
		if s.failWIQL != 0 {
			w.WriteHeader(s.failWIQL)
			_, _ = io.WriteString(w, `<html>sign in</html>`)
			return
		}
		ids := s.wiqlIDs
		if ids == nil {
			for id := range s.items {
				ids = append(ids, id)
			}
		}
		refs := []map[string]int{} // ADO answers an empty match as [], never null
		for _, id := range ids {
			refs = append(refs, map[string]int{"id": id})
		}
		b, _ := json.Marshal(map[string]any{"queryType": "flat", "workItems": refs})
		_, _ = w.Write(b)
	case r.Method == "POST" && p == "/wit/workitemsbatch":
		s.batches++
		if s.failBatchAt == s.batches {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var vals []json.RawMessage
		for _, v := range call.body["ids"].([]any) {
			id := int(v.(float64))
			fields, ok := s.items[id]
			if !ok || s.nullInBatch[id] {
				vals = append(vals, json.RawMessage(`null`))
				continue
			}
			vals = append(vals, itemJSON(id, fields))
		}
		b, _ := json.Marshal(map[string]any{"count": len(vals), "value": vals})
		_, _ = w.Write(b)
	default:
		t.Errorf("unexpected request %s %s", r.Method, call.path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func applyOps(fields map[string]any, ops []map[string]any) {
	for _, op := range ops {
		if f, ok := strings.CutPrefix(op["path"].(string), "/fields/"); ok {
			fields[f] = op["value"]
		}
	}
}

func itemJSON(id int, fields map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "rev": 1, "fields": fields})
	return b
}

// put stores a work item.
func (s *adoItemsServer) put(id int, typ, state, title, tags string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = map[string]any{"System.WorkItemType": typ, "System.State": state, "System.Title": title, "System.Tags": tags}
}

// writes is every non-GET call whose path contains part.
func (s *adoItemsServer) writes(method, part string) []adoItemsCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []adoItemsCall
	for _, c := range s.calls {
		if c.method == method && strings.Contains(c.path, part) {
			out = append(out, c)
		}
	}
	return out
}

func (s *adoItemsServer) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// opValue is the value of the op at path in ops, and whether there is one.
func opValue(ops []map[string]any, path string) (any, bool) {
	for _, op := range ops {
		if op["path"] == path {
			return op["value"], true
		}
	}
	return nil, false
}

// TestADOCreateIssueType: a task identity creates a Task; anything else the
// project's requirement backlog type, escaped once, as a JSON patch.
func TestADOCreateIssueType(t *testing.T) {
	t.Run("task", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		if _, err := newTestAzureBoards(t, s.Server, nil).CreateIssue(IssueInput{Title: "t-1", Labels: []string{"dross", "dross/phase:p", "dross/task:p/t-1"}}); err != nil {
			t.Fatal(err)
		}
		if got := s.writes("POST", "/wit/workitems/"); len(got) != 1 || !strings.HasSuffix(got[0].path, "/wit/workitems/$Task") {
			t.Errorf("creates = %+v, want one POST .../workitems/$Task", got)
		}
	})
	for process, want := range map[string]string{"Scrum": "$Product%20Backlog%20Item", "Basic": "$Issue"} {
		t.Run(process, func(t *testing.T) {
			typ := map[string]string{"Scrum": "Product Backlog Item", "Basic": "Issue"}[process]
			s := newADOItemsServer(t, typ)
			if _, err := newTestAzureBoards(t, s.Server, nil).CreateIssue(IssueInput{Title: "Phase p", Labels: []string{"dross", "dross/phase:p"}}); err != nil {
				t.Fatal(err)
			}
			got := s.writes("POST", "/wit/workitems/")
			if len(got) != 1 || !strings.HasSuffix(got[0].path, "/wit/workitems/"+want) {
				t.Fatalf("creates = %+v, want one POST .../workitems/%s", got, want)
			}
			if got[0].contentType != "application/json-patch+json" {
				t.Errorf("Content-Type = %q", got[0].contentType)
			}
			if tags, _ := opValue(got[0].ops, "/fields/System.Tags"); tags != "dross; dross/phase:p" {
				t.Errorf("System.Tags = %v, want \"dross; dross/phase:p\"", tags)
			}
		})
	}
}

// TestADOCreateIssueClassification: area and iteration ride every create —
// requirement, Task and Epic — exactly as configured and made absolute; unset,
// no create carries them; an update never does.
func TestADOCreateIssueClassification(t *testing.T) {
	createAll := func(t *testing.T, mod func(*Config)) (*adoItemsServer, []adoItemsCall) {
		s := newADOItemsServer(t, "User Story")
		s.wiqlIDs = []int{}
		c := newTestAzureBoards(t, s.Server, mod)
		if _, err := c.CreateIssue(IssueInput{Title: "phase", Labels: []string{"dross", "dross/phase:p"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.CreateIssue(IssueInput{Title: "task", Labels: []string{"dross", "dross/task:p/t-1"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.EnsureMilestoneEpic("v1.8", "Ship it", ""); err != nil {
			t.Fatal(err)
		}
		creates := s.writes("POST", "/wit/workitems/$")
		if len(creates) != 3 {
			t.Fatalf("%d creates, want 3", len(creates))
		}
		return s, creates
	}
	for name, mod := range map[string]func(*Config){
		"absolute": func(c *Config) { c.AreaPath, c.IterationPath = `Fabrikam\Web`, `Fabrikam\Sprint 1` },
		"relative": func(c *Config) { c.AreaPath, c.IterationPath = `Web`, `Sprint 1` },
	} {
		t.Run(name, func(t *testing.T) {
			s, creates := createAll(t, mod)
			for _, c := range creates {
				if v, _ := opValue(c.ops, "/fields/System.AreaPath"); v != `Fabrikam\Web` {
					t.Errorf("%s AreaPath = %v, want Fabrikam\\Web", c.path, v)
				}
				if v, _ := opValue(c.ops, "/fields/System.IterationPath"); v != `Fabrikam\Sprint 1` {
					t.Errorf("%s IterationPath = %v, want Fabrikam\\Sprint 1", c.path, v)
				}
			}
			title := "renamed"
			if _, err := newTestAzureBoards(t, s.Server, mod).UpdateIssue("501", IssuePatch{Title: &title}); err != nil {
				t.Fatal(err)
			}
			for _, u := range s.writes("PATCH", "/wit/workitems/") {
				for _, p := range []string{"/fields/System.AreaPath", "/fields/System.IterationPath"} {
					if _, ok := opValue(u.ops, p); ok {
						t.Errorf("an update carries %s", p)
					}
				}
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		_, creates := createAll(t, nil)
		for _, c := range creates {
			for _, p := range []string{"/fields/System.AreaPath", "/fields/System.IterationPath"} {
				if v, ok := opValue(c.ops, p); ok {
					t.Errorf("%s carries %s = %v with nothing configured", c.path, p, v)
				}
			}
		}
	})
}

// TestADOCreateIssueParentEpic: Milestone is the Epic's id, linked once as the
// parent; an update never links.
func TestADOCreateIssueParentEpic(t *testing.T) {
	relOps := func(ops []map[string]any) []map[string]any {
		var out []map[string]any
		for _, op := range ops {
			if strings.HasPrefix(op["path"].(string), "/relations") {
				out = append(out, op)
			}
		}
		return out
	}
	s := newADOItemsServer(t, "User Story")
	c := newTestAzureBoards(t, s.Server, nil)
	if _, err := c.CreateIssue(IssueInput{Title: "phase", Labels: []string{"dross"}, Milestone: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIssue(IssueInput{Title: "phase 2", Labels: []string{"dross"}}); err != nil {
		t.Fatal(err)
	}
	creates := s.writes("POST", "/wit/workitems/$")
	rels := relOps(creates[0].ops)
	if len(rels) != 1 {
		t.Fatalf("Milestone 42 adds %d relation ops, want 1", len(rels))
	}
	val, _ := rels[0]["value"].(map[string]any)
	if val["rel"] != adoRelParent || !strings.HasSuffix(val["url"].(string), "/_apis/wit/workItems/42") {
		t.Errorf("relation = %v, want Hierarchy-Reverse to .../_apis/wit/workItems/42", val)
	}
	if n := len(relOps(creates[1].ops)); n != 0 {
		t.Errorf("Milestone 0 adds %d relation ops", n)
	}
	m := 42
	if _, err := c.UpdateIssue("501", IssuePatch{Milestone: &m}); err != nil {
		t.Fatal(err)
	}
	for _, u := range s.writes("PATCH", "/wit/workitems/") {
		if n := len(relOps(u.ops)); n != 0 {
			t.Errorf("an update with Milestone sent %d relation ops", n)
		}
	}
}

// TestADOCreateIssueInput: titles are cut by rune, bodies are HTML, and a
// label that cannot be a tag never reaches the wire.
func TestADOCreateIssueInput(t *testing.T) {
	s := newADOItemsServer(t, "User Story")
	c := newTestAzureBoards(t, s.Server, nil)
	title := strings.Repeat("a", 254) + strings.Repeat("é", 46) // multi-byte at the cut
	if _, err := c.CreateIssue(IssueInput{Title: title, Body: "a < b\n- x & y", Labels: []string{"dross"}}); err != nil {
		t.Fatal(err)
	}
	ops := s.writes("POST", "/wit/workitems/$")[0].ops
	sent, _ := opValue(ops, "/fields/System.Title")
	if got := sent.(string); utf8.RuneCountInString(got) != 255 || !utf8.ValidString(got) || !strings.HasSuffix(got, "é") {
		t.Errorf("title sent as %d runes (valid %v)", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if desc, _ := opValue(ops, "/fields/System.Description"); desc != "a &lt; b<br>- x &amp; y" {
		t.Errorf("System.Description = %v", desc)
	}

	before := s.total()
	_, err := c.CreateIssue(IssueInput{Title: "t", Labels: []string{"dross", "a;b"}})
	if err == nil || !strings.Contains(err.Error(), "a;b") {
		t.Errorf("a ';' label = %v; want an error naming it", err)
	}
	labels := []string{"a;b"}
	if _, err := c.UpdateIssue("501", IssuePatch{Labels: &labels}); err == nil {
		t.Error("an update with a ';' label was sent")
	}
	if n := s.total() - before; n != 0 {
		t.Errorf("';' labels made %d requests, want 0", n)
	}
}

// TestADOUpdateIssueFields: labels replace the tag set, nil labels leave it,
// and a State patch is refused.
func TestADOUpdateIssueFields(t *testing.T) {
	s := newADOItemsServer(t, "User Story")
	s.put(7, "User Story", "Active", "t", "a; b")
	c := newTestAzureBoards(t, s.Server, nil)
	labels := []string{"a"}
	iss, err := c.UpdateIssue("7", IssuePatch{Labels: &labels})
	if err != nil {
		t.Fatal(err)
	}
	ups := s.writes("PATCH", "/wit/workitems/7")
	if tags, _ := opValue(ups[0].ops, "/fields/System.Tags"); tags != "a" {
		t.Errorf("System.Tags = %v, want a", tags)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "a" {
		t.Errorf("Labels = %v, want [a]", iss.Labels)
	}
	title := "renamed"
	if _, err := c.UpdateIssue("7", IssuePatch{Title: &title}); err != nil {
		t.Fatal(err)
	}
	ups = s.writes("PATCH", "/wit/workitems/7")
	if _, ok := opValue(ups[1].ops, "/fields/System.Tags"); ok {
		t.Error("nil Labels sent a tags op")
	}
	before := s.total()
	closed := "closed"
	if _, err := c.UpdateIssue("7", IssuePatch{State: &closed}); err == nil {
		t.Error("a State patch was accepted")
	}
	if n := s.total() - before; n != 0 {
		t.Errorf("a State patch made %d requests", n)
	}
}

// epicTags is a milestone Epic's tags.
const epicTags = "dross; dross/milestone:v1.8"

// TestADOEnsureMilestoneEpic: one Epic per milestone, found by its tag before
// any create; the lowest id of duplicates wins; a stale title is fixed once.
func TestADOEnsureMilestoneEpic(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		s.put(3, "Epic", "New", "v1.8 — Ship it", epicTags)
		id, err := newTestAzureBoards(t, s.Server, nil).EnsureMilestoneEpic("v1.8", "Ship it", "")
		if err != nil || id != "3" {
			t.Fatalf("EnsureMilestoneEpic = %q, %v; want 3", id, err)
		}
		if n := len(s.writes("POST", "/wit/workitems/$")); n != 0 {
			t.Errorf("%d creates for an Epic that exists", n)
		}
		if n := len(s.writes("PATCH", "/wit/workitems/")); n != 0 {
			t.Errorf("an unchanged title was PATCHed %d times", n)
		}
	})
	t.Run("duplicates", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		s.put(100, "Epic", "New", "v1.8 — Ship it", epicTags)
		s.put(99, "Epic", "New", "v1.8 — Ship it", epicTags)
		var id string
		var err error
		out := captureStderrForge(t, func() { id, err = newTestAzureBoards(t, s.Server, nil).EnsureMilestoneEpic("v1.8", "Ship it", "") })
		if err != nil || id != "99" {
			t.Fatalf("EnsureMilestoneEpic = %q, %v; want 99", id, err)
		}
		if !strings.Contains(out, "100") {
			t.Errorf("no warning names the duplicate: %q", out)
		}
	})
	t.Run("absent", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		s.wiqlIDs = []int{}
		id, err := newTestAzureBoards(t, s.Server, nil).EnsureMilestoneEpic("v1.8", "Ship it", "the scope")
		if err != nil || id == "" {
			t.Fatalf("EnsureMilestoneEpic = %q, %v", id, err)
		}
		creates := s.writes("POST", "/wit/workitems/$")
		if len(creates) != 1 || !strings.HasSuffix(creates[0].path, "/wit/workitems/$Epic") {
			t.Fatalf("creates = %+v, want one POST .../workitems/$Epic", creates)
		}
		if tags, _ := opValue(creates[0].ops, "/fields/System.Tags"); tags != epicTags {
			t.Errorf("System.Tags = %v, want %q", tags, epicTags)
		}
		if title, _ := opValue(creates[0].ops, "/fields/System.Title"); title != "v1.8 — Ship it" {
			t.Errorf("System.Title = %v", title)
		}
	})
	t.Run("stale title", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		s.put(3, "Epic", "New", "v1.8 — Old name", epicTags)
		c := newTestAzureBoards(t, s.Server, nil)
		for range 2 {
			if _, err := c.EnsureMilestoneEpic("v1.8", "Ship it", ""); err != nil {
				t.Fatal(err)
			}
		}
		ups := s.writes("PATCH", "/wit/workitems/3")
		if len(ups) != 1 {
			t.Fatalf("%d title PATCHes, want 1", len(ups))
		}
		if title, _ := opValue(ups[0].ops, "/fields/System.Title"); title != "v1.8 — Ship it" {
			t.Errorf("retitled to %v", title)
		}
	})
	t.Run("a near-miss tag is not the Epic", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		s.put(3, "Epic", "New", "v1.80", "dross; dross/milestone:v1.80")
		if id, err := newTestAzureBoards(t, s.Server, nil).EnsureMilestoneEpic("v1.8", "", ""); err != nil || id == "3" {
			t.Errorf("EnsureMilestoneEpic = %q, %v; v1.80's Epic is not v1.8's", id, err)
		}
	})
}

// wiqlQueries is every WIQL query s received.
func (s *adoItemsServer) wiqlQueries(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range s.writes("POST", "/_apis/wit/wiql") {
		if c.path != adoProjectAPI+"/wit/wiql" {
			t.Errorf("WIQL POSTed to %s, want %s/wit/wiql", c.path, adoProjectAPI)
		}
		q, _ := c.body["query"].(string)
		if !strings.Contains(q, "[System.TeamProject] = @project") {
			t.Errorf("a WIQL query crosses projects: %q", q)
		}
		out = append(out, q)
	}
	return out
}

// TestADOListIssuesLabelMatch: a label matches a tag, and — when it names a
// type the project has — that type; dross labels are tags only; values never
// leave their literal.
func TestADOListIssuesLabelMatch(t *testing.T) {
	t.Run("agile", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story", "Bug", "Epic", "Feature", "Task", "User Story")
		if _, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{State: "open", Labels: []string{"bug", "enhancement"}}); err != nil {
			t.Fatal(err)
		}
		q := s.wiqlQueries(t)
		want := "([System.Tags] CONTAINS 'bug' OR [System.WorkItemType] = 'Bug' OR [System.Tags] CONTAINS 'enhancement')"
		if len(q) != 1 || !strings.Contains(q[0], want) {
			t.Errorf("WIQL = %q, want it to hold %q", q, want)
		}
		if strings.Count(q[0], "[System.WorkItemType]") != 1 {
			t.Errorf("WIQL = %q: a type clause for enhancement", q[0])
		}
	})
	t.Run("basic", func(t *testing.T) {
		s := newADOItemsServer(t, "Issue", "Epic", "Issue", "Task")
		if _, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{Labels: []string{"bug"}}); err != nil {
			t.Fatal(err)
		}
		if q := s.wiqlQueries(t); strings.Contains(q[0], "[System.WorkItemType]") {
			t.Errorf("WIQL = %q: Basic has no Bug type", q[0])
		}
	})
	t.Run("dross labels", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story", "dross", "Task")
		if _, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{Labels: []string{"dross", "dross/phase:x"}}); err != nil {
			t.Fatal(err)
		}
		if q := s.wiqlQueries(t); strings.Contains(q[0], "[System.WorkItemType]") {
			t.Errorf("WIQL = %q: a dross label became a type clause", q[0])
		}
	})
	t.Run("quoting", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story", "Task")
		if _, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{Labels: []string{"o'brien", "x' OR [System.Id] > '0"}}); err != nil {
			t.Fatal(err)
		}
		q := s.wiqlQueries(t)[0]
		for _, want := range []string{"CONTAINS 'o''brien'", "CONTAINS 'x'' OR [System.Id] > ''0'"} {
			if !strings.Contains(q, want) {
				t.Errorf("WIQL = %q, want %q", q, want)
			}
		}
		if strings.Contains(q, "OR [System.Id] > '0") && !strings.Contains(q, "''0'") {
			t.Errorf("WIQL = %q: a label escaped its literal", q)
		}
	})
}

// TestADOListIssuesFilters: identity is an exact tag, open excludes finished
// and removed work, and the type of a label matches without its tag.
func TestADOListIssuesFilters(t *testing.T) {
	s := newADOItemsServer(t, "User Story", "Bug", "Task", "User Story")
	s.put(1, "Task", "Active", "t-1", "dross; dross/task:p/t-1")
	s.put(10, "Task", "Active", "t-10", "dross; dross/task:p/t-10")
	s.put(20, "Bug", "Active", "hand-filed", "")
	s.put(21, "User Story", "Resolved", "awaiting", "bug")
	s.put(22, "User Story", "Closed", "done", "bug")
	s.put(23, "User Story", "Removed", "gone", "bug")
	c := newTestAzureBoards(t, s.Server, nil)

	ids := func(iss []Issue) []int {
		var out []int
		for _, i := range iss {
			out = append(out, i.Number)
		}
		return out
	}
	s.wiqlIDs = []int{1, 10}
	got, err := c.ListIssues(IssueFilter{State: "all", Labels: []string{"dross/task:p/t-1"}})
	if err != nil || len(got) != 1 || got[0].Number != 1 {
		t.Errorf("dross/task:p/t-1 lookup = %v, %v; want [1] (t-10 only CONTAINS it)", ids(got), err)
	}
	s.wiqlIDs = []int{20, 21, 22, 23}
	got, err = c.ListIssues(IssueFilter{State: "open", Labels: []string{"bug"}})
	if err != nil || fmt.Sprint(ids(got)) != "[20 21]" {
		t.Errorf("open bug = %v, %v; want [20 21] (an untagged Bug, a Resolved item)", ids(got), err)
	}
	got, err = c.ListIssues(IssueFilter{State: "all", Labels: []string{"bug"}})
	if err != nil || fmt.Sprint(ids(got)) != "[20 21 22 23]" {
		t.Errorf("all bug = %v, %v; want [20 21 22 23]", ids(got), err)
	}
}

// TestADOListIssuesFailures: a failed read is an error, never an empty list;
// reads batch at 200; a null batch entry is skipped.
func TestADOListIssuesFailures(t *testing.T) {
	for name, set := range map[string]func(*adoItemsServer){
		"WIQL 500":  func(s *adoItemsServer) { s.failWIQL = http.StatusInternalServerError },
		"WIQL 203":  func(s *adoItemsServer) { s.failWIQL = http.StatusNonAuthoritativeInfo },
		"types 500": func(s *adoItemsServer) { s.failTypes = http.StatusInternalServerError },
		"types 203": func(s *adoItemsServer) { s.failTypes = http.StatusNonAuthoritativeInfo },
	} {
		t.Run(name, func(t *testing.T) {
			s := newADOItemsServer(t, "User Story", "Bug")
			set(s)
			got, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{Labels: []string{"bug"}})
			if err == nil || got != nil {
				t.Errorf("ListIssues = %v, %v; want (nil, error)", got, err)
			}
		})
	}

	big := func() *adoItemsServer {
		s := newADOItemsServer(t, "User Story")
		for id := 1; id <= 450; id++ {
			s.put(id, "Task", "Active", "t", "dross")
		}
		s.wiqlIDs = make([]int, 0, 450)
		for id := 1; id <= 450; id++ {
			s.wiqlIDs = append(s.wiqlIDs, id)
		}
		return s
	}
	t.Run("batches of 200", func(t *testing.T) {
		s := big()
		got, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{State: "all"})
		if err != nil || len(got) != 450 {
			t.Fatalf("ListIssues = %d items, %v; want 450", len(got), err)
		}
		var sizes []int
		for _, b := range s.writes("POST", "/wit/workitemsbatch") {
			sizes = append(sizes, len(b.body["ids"].([]any)))
		}
		if fmt.Sprint(sizes) != "[200 200 50]" {
			t.Errorf("batch sizes = %v, want [200 200 50]", sizes)
		}
	})
	t.Run("a failed second batch", func(t *testing.T) {
		s := big()
		s.failBatchAt = 2
		if got, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{State: "all"}); err == nil || got != nil {
			t.Errorf("ListIssues = %d items, %v; want (nil, error)", len(got), err)
		}
	})
	t.Run("a null entry", func(t *testing.T) {
		s := newADOItemsServer(t, "User Story")
		s.put(1, "Task", "Active", "t", "dross")
		s.put(2, "Task", "Active", "t", "dross")
		s.wiqlIDs = []int{1, 2}
		s.nullInBatch = map[int]bool{2: true}
		got, err := newTestAzureBoards(t, s.Server, nil).ListIssues(IssueFilter{State: "all"})
		if err != nil || len(got) != 1 || got[0].Number != 1 {
			t.Errorf("ListIssues = %v, %v; want only item 1", got, err)
		}
	})
}
