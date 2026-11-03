package forge

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// adoRoute is one canned answer of an adoMetaServer.
type adoRoute struct {
	status int
	body   string
}

// adoMetaServer answers by escaped path and records every request it sees.
type adoMetaServer struct {
	*httptest.Server
	hits   *int32
	mu     sync.Mutex
	seen   []*http.Request
	routes map[string]adoRoute
}

func newADOMetaServer(t *testing.T, routes map[string]adoRoute) *adoMetaServer {
	t.Helper()
	m := &adoMetaServer{routes: routes}
	m.Server, m.hits = adoBoardsServer(t, func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.seen = append(m.seen, r.Clone(r.Context()))
		m.mu.Unlock()
		rt, ok := m.routes[r.URL.EscapedPath()]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if rt.status != 0 {
			w.WriteHeader(rt.status)
		}
		_, _ = w.Write([]byte(rt.body))
	})
	return m
}

func (m *adoMetaServer) count() int32 { return atomic.LoadInt32(m.hits) }

func (m *adoMetaServer) last() *http.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.seen) == 0 {
		return nil
	}
	return m.seen[len(m.seen)-1]
}

const adoProjectAPI = "/acme/Fabrikam/_apis"

// backlogConfig is a backlogconfiguration answer naming typeName.
func backlogConfig(typeName string) string {
	return `{"requirementBacklog":{"name":"Backlog items","workItemTypes":[{"name":"` + typeName +
		`"}],"defaultWorkItemType":{"name":"` + typeName + `"}},"taskBacklog":{"defaultWorkItemType":{"name":"Task"}}}`
}

// TestADORequirementTypeFromBacklogConfig: the requirement type is read from
// the project's backlog configuration, whatever the process calls it, and is
// never guessed when the read fails.
func TestADORequirementTypeFromBacklogConfig(t *testing.T) {
	path := adoProjectAPI + "/work/backlogconfiguration"
	for process, want := range map[string]string{
		"Agile": "User Story", "Scrum": "Product Backlog Item", "Basic": "Issue", "custom": "Customer Request",
	} {
		t.Run(process, func(t *testing.T) {
			srv := newADOMetaServer(t, map[string]adoRoute{path: {body: backlogConfig(want)}})
			c := newTestAzureBoards(t, srv.Server, nil)
			got, err := c.RequirementType()
			if err != nil || got != want {
				t.Fatalf("RequirementType = %q, %v; want %q", got, err, want)
			}
			if _, err := c.RequirementType(); err != nil {
				t.Fatal(err)
			}
			if n := srv.count(); n != 1 {
				t.Errorf("two calls made %d requests, want 1 (cached)", n)
			}
		})
	}
	t.Run("only workItemTypes", func(t *testing.T) {
		srv := newADOMetaServer(t, map[string]adoRoute{path: {body: `{"requirementBacklog":{"workItemTypes":[{"name":"Issue"}]}}`}})
		if got, err := newTestAzureBoards(t, srv.Server, nil).RequirementType(); err != nil || got != "Issue" {
			t.Errorf("RequirementType = %q, %v; want Issue", got, err)
		}
	})
	for name, rt := range map[string]adoRoute{
		"500":                   {status: http.StatusInternalServerError, body: `oops`},
		"no requirementBacklog": {body: `{"taskBacklog":{"defaultWorkItemType":{"name":"Task"}}}`},
		"empty backlog":         {body: `{"requirementBacklog":{"workItemTypes":[]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newADOMetaServer(t, map[string]adoRoute{path: rt})
			c := newTestAzureBoards(t, srv.Server, nil)
			got, err := c.RequirementType()
			if err == nil || got != "" {
				t.Fatalf("RequirementType = %q, %v; want an error and no type", got, err)
			}
			if !strings.Contains(err.Error(), "backlog configuration") {
				t.Errorf("error does not name the backlog configuration: %v", err)
			}
			if _, err := c.RequirementType(); err == nil {
				t.Error("a failed read was cached as a success")
			}
			if n := srv.count(); n != 2 {
				t.Errorf("a failed read was cached: %d requests for two calls, want 2", n)
			}
		})
	}
}

// pbiStates is the Scrum Product Backlog Item states answer.
const pbiStates = `{"count":5,"value":[
	{"name":"New","category":"Proposed"},
	{"name":"Approved","category":"Proposed"},
	{"name":"Committed","category":"InProgress"},
	{"name":"Done","category":"Completed"},
	{"name":"Removed","category":"Removed"}]}`

// TestADOStatesByCategory: a type's states come back with their categories, in
// the order the API lists them, from a path that escapes the type name once.
// The second read is the cache.
func TestADOStatesByCategory(t *testing.T) {
	path := adoProjectAPI + "/wit/workitemtypes/Product%20Backlog%20Item/states"
	srv := newADOMetaServer(t, map[string]adoRoute{path: {body: pbiStates}})
	c := newTestAzureBoards(t, srv.Server, nil)
	got, err := c.States("Product Backlog Item")
	if err != nil {
		t.Fatal(err)
	}
	want := []ADOState{
		{"New", ADOCategoryProposed}, {"Approved", ADOCategoryProposed}, {"Committed", ADOCategoryInProgress},
		{"Done", ADOCategoryCompleted}, {"Removed", ADOCategoryRemoved},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("States = %v, want %v", got, want)
	}
	if p := srv.last().URL.EscapedPath(); strings.Contains(p, "%2520") {
		t.Errorf("type name double-escaped: %s", p)
	}
	before := srv.count()
	again, err := c.States("product backlog item")
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("cached States = %v, %v", again, err)
	}
	if n := srv.count() - before; n != 0 {
		t.Errorf("the second read made %d requests, want 0", n)
	}
	// The cache hands out copies: a caller cannot corrupt it.
	again[0].Name = "mutated"
	if third, _ := c.States("Product Backlog Item"); third[0].Name != "New" {
		t.Error("States returned the cache's own slice")
	}
}

// workItemJSON is a work item answer.
func workItemJSON(id, typ, state, tags string) string {
	return `{"id":` + id + `,"rev":3,"fields":{"System.WorkItemType":"` + typ + `","System.Title":"Phase p",` +
		`"System.Description":"line one<br>a &amp; b","System.Tags":"` + tags + `","System.State":"` + state + `"},` +
		`"relations":[{"rel":"System.LinkTypes.Hierarchy-Reverse","url":"https://dev.azure.com/acme/_apis/wit/workItems/1"}]}`
}

// TestADOGetIssueResolved: closed and Resolved come from the state's category,
// never its name. Done is Completed; a Resolved-category state and Removed are
// open and unresolved.
func TestADOGetIssueResolved(t *testing.T) {
	statesPath := adoProjectAPI + "/wit/workitemtypes/Product%20Backlog%20Item/states"
	for _, tc := range []struct {
		state        string
		states       string
		wantResolved bool
	}{
		{"Done", pbiStates, true},
		{"Removed", pbiStates, false},
		{"Resolved", `{"value":[{"name":"Active","category":"InProgress"},{"name":"Resolved","category":"Resolved"},{"name":"Closed","category":"Completed"}]}`, false},
		{"Committed", pbiStates, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			srv := newADOMetaServer(t, map[string]adoRoute{
				adoProjectAPI + "/wit/workitems/7": {body: workItemJSON("7", "Product Backlog Item", tc.state, "dross")},
				statesPath:                         {body: tc.states},
			})
			iss, err := newTestAzureBoards(t, srv.Server, nil).GetIssue("7")
			if err != nil {
				t.Fatal(err)
			}
			wantState := "open"
			if tc.wantResolved {
				wantState = "closed"
			}
			if iss.Resolved != tc.wantResolved || iss.State != wantState || iss.WorkflowState != tc.state {
				t.Errorf("GetIssue = Resolved %v State %q WorkflowState %q; want %v %q %q",
					iss.Resolved, iss.State, iss.WorkflowState, tc.wantResolved, wantState, tc.state)
			}
		})
	}
}

// TestADOGetIssueMapping: the fields map onto Issue, and the dross vocabulary
// in System.Tags reads back lowercased so identity lookups match.
func TestADOGetIssueMapping(t *testing.T) {
	srv := newADOMetaServer(t, map[string]adoRoute{
		adoProjectAPI + "/wit/workitems/7":                                   {body: workItemJSON("7", "Product Backlog Item", "Committed", "Dross; DROSS/phase:p; Bug")},
		adoProjectAPI + "/wit/workitemtypes/Product%20Backlog%20Item/states": {body: pbiStates},
	})
	iss, err := newTestAzureBoards(t, srv.Server, nil).GetIssue("7")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"dross", "dross/phase:p", "Bug"}; !reflect.DeepEqual(iss.Labels, want) {
		t.Errorf("Labels = %q, want %q", iss.Labels, want)
	}
	// boardsync.HasMarker compares against the literal "dross".
	hasMarker := false
	for _, l := range iss.Labels {
		hasMarker = hasMarker || l == "dross"
	}
	if !hasMarker {
		t.Error("a Dross-cased tag does not read as the dross marker")
	}
	if iss.Number != 7 || iss.Key != "7" || iss.Title != "Phase p" || iss.Body != "line one\na & b" {
		t.Errorf("GetIssue = %+v", iss)
	}
	if want := srv.URL + "/acme/Fabrikam/_workitems/edit/7"; iss.URL != want {
		t.Errorf("URL = %q, want %q", iss.URL, want)
	}
}

// TestADOGetIssueRequest: a key that is not a work-item id never reaches the
// wire; a 404 is an error, never (nil, nil); the read asks for relations and
// sends no fields param.
func TestADOGetIssueRequest(t *testing.T) {
	srv := newADOMetaServer(t, map[string]adoRoute{
		adoProjectAPI + "/wit/workitems/7":                                   {body: workItemJSON("7", "Product Backlog Item", "Committed", "")},
		adoProjectAPI + "/wit/workitemtypes/Product%20Backlog%20Item/states": {body: pbiStates},
		adoProjectAPI + "/wit/workitems/8":                                   {status: http.StatusNotFound, body: `{"message":"TF401232: Work item 8 does not exist"}`},
	})
	c := newTestAzureBoards(t, srv.Server, nil)
	for _, bad := range []string{"PROJ-7", "", "0", "-3", "7/../8", "07"} {
		if iss, err := c.GetIssue(bad); err == nil || iss != nil {
			t.Errorf("GetIssue(%q) = %v, %v; want an error", bad, iss, err)
		}
	}
	if n := srv.count(); n != 0 {
		t.Errorf("bad keys made %d requests, want 0", n)
	}
	if iss, err := c.GetIssue("8"); err == nil || iss != nil {
		t.Errorf("a 404 = %v, %v; want an error", iss, err)
	}
	if _, err := c.GetIssue("7"); err != nil {
		t.Fatal(err)
	}
	var get *http.Request
	srv.mu.Lock()
	for _, r := range srv.seen {
		if r.URL.Path == adoProjectAPI+"/wit/workitems/7" {
			get = r
		}
	}
	srv.mu.Unlock()
	if get == nil {
		t.Fatal("no work item read was recorded")
	}
	q := get.URL.Query()
	if q.Get("$expand") != "relations" {
		t.Errorf("$expand = %q, want relations", q.Get("$expand"))
	}
	if _, ok := q["fields"]; ok {
		t.Error("the read sent a fields param, which cannot be combined with $expand")
	}
}

// TestADOCheckPaths: an area or iteration path is probed at its
// classification node, relative or absolute alike; 404 is missing, and any
// other failure is an error, never "missing".
func TestADOCheckPaths(t *testing.T) {
	areas := adoProjectAPI + "/wit/classificationnodes/Areas"
	iterations := adoProjectAPI + "/wit/classificationnodes/Iterations"
	srv := newADOMetaServer(t, map[string]adoRoute{
		areas + "/Web":             {body: `{"id":2,"name":"Web"}`},
		areas:                      {body: `{"id":1,"name":"Fabrikam"}`},
		iterations + "/Sprint%201": {body: `{"id":5,"name":"Sprint 1"}`},
		areas + "/Gone":            {status: http.StatusNotFound, body: `{"message":"VS402485"}`},
		areas + "/Broken":          {status: http.StatusInternalServerError, body: `oops`},
		areas + "/SignIn":          {status: http.StatusNonAuthoritativeInfo, body: `<html>Sign in</html>`},
		areas + "/Web/Team%20A":    {body: `{"id":3}`},
	})
	c := newTestAzureBoards(t, srv.Server, nil)
	for _, tc := range []struct {
		kind     ADOPathKind
		path     string
		wantPath string
	}{
		{ADOAreaPath, `Fabrikam\Web`, areas + "/Web"},
		{ADOAreaPath, `Web`, areas + "/Web"},
		{ADOAreaPath, `Fabrikam`, areas},
		{ADOAreaPath, `Fabrikam\Web\Team A`, areas + "/Web/Team%20A"},
		{ADOIterationPath, `Fabrikam\Sprint 1`, iterations + "/Sprint%201"},
	} {
		ok, err := c.PathExists(tc.kind, tc.path)
		if err != nil || !ok {
			t.Errorf("PathExists(%s, %q) = %v, %v; want present", tc.kind, tc.path, ok, err)
			continue
		}
		if got := srv.last().URL.EscapedPath(); got != tc.wantPath {
			t.Errorf("PathExists(%s, %q) GET %s, want %s", tc.kind, tc.path, got, tc.wantPath)
		}
	}
	if ok, err := c.PathExists(ADOAreaPath, `Fabrikam\Gone`); err != nil || ok {
		t.Errorf("a 404 = %v, %v; want missing with no error", ok, err)
	}
	for _, p := range []string{`Broken`, `SignIn`} {
		if ok, err := c.PathExists(ADOAreaPath, p); err == nil || ok {
			t.Errorf("PathExists(%q) = %v, %v; want an error, never missing", p, ok, err)
		}
	}
	if _, err := c.PathExists("team", "Web"); err == nil {
		t.Error("an unknown kind was accepted")
	}
	before := srv.count()
	for _, p := range []string{`Web\..`, `Fabrikam\.\Web`, `..`} {
		if ok, err := c.PathExists(ADOAreaPath, p); err == nil || ok {
			t.Errorf("PathExists(%q) = %v, %v; want a dot-segment refusal", p, ok, err)
		}
	}
	if n := srv.count() - before; n != 0 {
		t.Errorf("dot-segment paths made %d requests, want 0", n)
	}
}

// TestADOTypeNames: the project's type names are read once, in API order, and
// HasType matches case-insensitively, answering with the project's spelling.
func TestADOTypeNames(t *testing.T) {
	srv := newADOMetaServer(t, map[string]adoRoute{
		adoProjectAPI + "/wit/workitemtypes": {body: `{"count":3,"value":[{"name":"Bug"},{"name":"Product Backlog Item"},{"name":"Task"}]}`},
	})
	c := newTestAzureBoards(t, srv.Server, nil)
	names, err := c.TypeNames()
	if want := []string{"Bug", "Product Backlog Item", "Task"}; err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("TypeNames = %q, %v; want %q", names, err, want)
	}
	for in, want := range map[string]string{"bug": "Bug", "BUG": "Bug", " task ": "Task"} {
		got, ok, err := c.HasType(in)
		if err != nil || !ok || got != want {
			t.Errorf("HasType(%q) = %q, %v, %v; want %q", in, got, ok, err, want)
		}
	}
	if _, ok, err := c.HasType("Issue"); err != nil || ok {
		t.Errorf("HasType(Issue) = %v, %v; want absent (Scrum has no Issue type)", ok, err)
	}
	if n := srv.count(); n != 1 {
		t.Errorf("TypeNames and four HasType calls made %d requests, want 1", n)
	}
	names[0] = "mutated"
	if again, _ := c.TypeNames(); again[0] != "Bug" {
		t.Error("TypeNames returned the cache's own slice")
	}

	for name, rt := range map[string]adoRoute{
		"no value": {body: `{"count":0}`},
		"500":      {status: http.StatusInternalServerError, body: `oops`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newADOMetaServer(t, map[string]adoRoute{adoProjectAPI + "/wit/workitemtypes": rt})
			c := newTestAzureBoards(t, srv.Server, nil)
			if names, err := c.TypeNames(); err == nil || names != nil {
				t.Errorf("TypeNames = %q, %v; want an error", names, err)
			}
			if _, _, err := c.HasType("Bug"); err == nil {
				t.Error("HasType answered from an unreadable type list")
			}
		})
	}
}

// TestADODecodedValuesCarryNoConfig: a 200 body echoing the PAT is scrubbed
// before it is decoded, and the decoded value carries the bare marker — not
// the token, and not the env var's name, which is config that must not ride a
// decoded field toward a path a caller opens.
func TestADODecodedValuesCarryNoConfig(t *testing.T) {
	srv := newADOMetaServer(t, map[string]adoRoute{
		adoProjectAPI + "/wit/workitems/7":                                   {body: workItemJSON("7", "Product Backlog Item", "Committed", "dross; leaked "+adoBoardsPAT)},
		adoProjectAPI + "/wit/workitemtypes/Product%20Backlog%20Item/states": {body: pbiStates},
	})
	iss, err := newTestAzureBoards(t, srv.Server, nil).GetIssue("7")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(iss.Labels, "|")
	if strings.Contains(joined, adoBoardsPAT) || strings.Contains(joined, adoBoardsTokenEnv) {
		t.Errorf("decoded labels carry the token or its env name: %q", iss.Labels)
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Errorf("decoded labels lost the redaction marker: %q", iss.Labels)
	}
}
