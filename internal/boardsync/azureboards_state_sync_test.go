package boardsync

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Rivil/dross/internal/board"
	"github.com/Rivil/dross/internal/project"
)

// Board sync against Azure Boards (azure-boards-backend c-1, c-3, c-4, c-9).
// adoTracker is a small Azure Boards behind httptest: one process (its
// requirement backlog type and each type's states in order, with categories),
// a store of work items, and the WIQL and batch reads over it. The client is
// built exactly as cmd builds it — boardsync.Config into forge.NewBoard.

// adoState is one state of a work-item type, in the type's order.
type adoState struct{ name, category string }

// adoProcess is a project's process as the metadata reads answer it.
type adoProcess struct {
	requirement string
	types       map[string][]adoState
}

var (
	agileStates  = []adoState{{"New", "Proposed"}, {"Active", "InProgress"}, {"Resolved", "Resolved"}, {"Closed", "Completed"}, {"Removed", "Removed"}}
	agileProcess = adoProcess{"User Story", map[string][]adoState{
		"Epic": agileStates, "User Story": agileStates,
		"Task": {{"New", "Proposed"}, {"Active", "InProgress"}, {"Closed", "Completed"}, {"Removed", "Removed"}},
	}}
	scrumProcess = adoProcess{"Product Backlog Item", map[string][]adoState{
		"Epic":                 {{"New", "Proposed"}, {"In Progress", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
		"Product Backlog Item": {{"New", "Proposed"}, {"Approved", "Proposed"}, {"Committed", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
		"Task":                 {{"To Do", "Proposed"}, {"In Progress", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
	}}
	basicStates  = []adoState{{"To Do", "Proposed"}, {"Doing", "InProgress"}, {"Done", "Completed"}}
	basicProcess = adoProcess{"Issue", map[string][]adoState{"Epic": basicStates, "Issue": basicStates, "Task": basicStates}}
	// customProcess is an inherited process: its requirement type, Story,
	// runs New → Active → Done and has a second Completed state, Accepted,
	// so a close that honours [board].state_map and one that ignores it
	// write different states.
	customProcess = adoProcess{"Story", map[string][]adoState{
		"Story": {{"New", "Proposed"}, {"Active", "InProgress"}, {"Done", "Completed"}, {"Accepted", "Completed"}, {"Removed", "Removed"}},
		"Task":  {{"New", "Proposed"}, {"Active", "InProgress"}, {"Done", "Completed"}, {"Removed", "Removed"}},
	}}
)

// adoProjectAPI is the project-scoped API root every Boards call targets.
const adoProjectAPI = "/acme/Fabrikam/_apis"

// adoCall is one request the tracker saw.
type adoCall struct {
	method, path, query, auth string
	ops                       []map[string]any // a JSON-patch body
}

type adoItem struct {
	typ, state, title, tags string
	rels                    []map[string]any
}

type adoTracker struct {
	*httptest.Server
	t     *testing.T
	mu    sync.Mutex
	proc  adoProcess
	items map[int]*adoItem
	next  int
	// stick holds, per id, the state a System.State write leaves the item
	// in, whatever was written: a workflow rule that answers 200 and keeps
	// the item where it was. stickOnce does the same for the next write
	// only.
	stick     map[int]string
	stickOnce map[int]string
	calls     []adoCall
}

// newADOTracker serves proc. The first item created is 10, then 11, 12, ...
func newADOTracker(t *testing.T, proc adoProcess) *adoTracker {
	t.Helper()
	tr := &adoTracker{t: t, proc: proc, items: map[int]*adoItem{}, next: 9, stick: map[int]string{}, stickOnce: map[int]string{}}
	tr.Server = httptest.NewServer(http.HandlerFunc(tr.serve))
	t.Cleanup(tr.Close)
	return tr
}

func (tr *adoTracker) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	c := adoCall{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
	_ = json.Unmarshal(raw, &c.ops)
	tr.calls = append(tr.calls, c)
	w.Header().Set("Content-Type", "application/json")
	p, ok := strings.CutPrefix(c.path, adoProjectAPI)
	if !ok {
		tr.t.Errorf("a request outside the project: %s %s", r.Method, c.path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	reply := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write(b)
	}
	switch {
	case r.Method == "GET" && p == "/work/backlogconfiguration":
		req := map[string]string{"name": tr.proc.requirement}
		reply(map[string]any{"requirementBacklog": map[string]any{"workItemTypes": []any{req}, "defaultWorkItemType": req}})
	case r.Method == "GET" && p == "/wit/workitemtypes":
		var names []string
		for n := range tr.proc.types {
			names = append(names, n)
		}
		sort.Strings(names)
		var vals []map[string]string
		for _, n := range names {
			vals = append(vals, map[string]string{"name": n})
		}
		reply(map[string]any{"value": vals})
	case r.Method == "GET" && strings.HasPrefix(p, "/wit/workitemtypes/") && strings.HasSuffix(p, "/states"):
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(p, "/wit/workitemtypes/"), "/states"), "%20", " ")
		var vals []map[string]string
		for _, s := range tr.proc.types[name] {
			vals = append(vals, map[string]string{"name": s.name, "category": s.category})
		}
		reply(map[string]any{"value": vals})
	case r.Method == "POST" && strings.HasPrefix(p, "/wit/workitems/$"):
		typ := strings.ReplaceAll(strings.TrimPrefix(p, "/wit/workitems/$"), "%20", " ")
		states := tr.proc.types[typ]
		if len(states) == 0 {
			tr.t.Errorf("created a %q, which the process has no such type", typ)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tr.next++
		it := &adoItem{typ: typ, state: states[0].name}
		tr.apply(tr.next, it, c.ops)
		tr.items[tr.next] = it
		reply(tr.itemJSON(tr.next, it))
	case strings.HasPrefix(p, "/wit/workitems/") && (r.Method == "PATCH" || r.Method == "GET"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/wit/workitems/"))
		it, ok := tr.items[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == "PATCH" {
			tr.apply(id, it, c.ops)
		}
		reply(tr.itemJSON(id, it))
	case r.Method == "POST" && p == "/wit/wiql":
		// Every item: the client post-filters tags exactly, so a broad
		// answer is a faithful one.
		refs := []map[string]int{}
		for _, id := range tr.ids() {
			refs = append(refs, map[string]int{"id": id})
		}
		reply(map[string]any{"workItems": refs})
	case r.Method == "POST" && p == "/wit/workitemsbatch":
		var body struct{ IDs []int }
		_ = json.Unmarshal(raw, &body)
		vals := []any{}
		for _, id := range body.IDs {
			if it, ok := tr.items[id]; ok {
				vals = append(vals, tr.itemJSON(id, it))
			}
		}
		reply(map[string]any{"count": len(vals), "value": vals})
	default:
		tr.t.Errorf("unexpected request %s %s", r.Method, c.path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (tr *adoTracker) apply(id int, it *adoItem, ops []map[string]any) {
	for _, op := range ops {
		path, _ := op["path"].(string)
		val, _ := op["value"].(string)
		switch {
		case op["op"] == "add" && path == "/fields/System.State":
			it.state = val
			if s, ok := tr.stickOnce[id]; ok {
				it.state = s
				delete(tr.stickOnce, id)
			}
			if s := tr.stick[id]; s != "" {
				it.state = s
			}
		case op["op"] == "add" && path == "/fields/System.Title":
			it.title = val
		case op["op"] == "add" && path == "/fields/System.Tags" && it.tags != "":
			// The live service merges an add into the tags the item has;
			// only replace replaces them (t-21).
			it.tags += "; " + val
		case (op["op"] == "add" || op["op"] == "replace") && path == "/fields/System.Tags":
			it.tags = val
		case op["op"] == "add" && path == "/relations/-":
			rel, _ := op["value"].(map[string]any)
			it.rels = append(it.rels, rel)
		case op["op"] == "remove" && strings.HasPrefix(path, "/relations/"):
			if i, err := strconv.Atoi(strings.TrimPrefix(path, "/relations/")); err == nil && i < len(it.rels) {
				it.rels = append(it.rels[:i], it.rels[i+1:]...)
			}
		}
	}
}

func (tr *adoTracker) itemJSON(id int, it *adoItem) map[string]any {
	return map[string]any{"id": id, "rev": 1, "relations": it.rels, "fields": map[string]any{
		"System.WorkItemType": it.typ, "System.State": it.state, "System.Title": it.title, "System.Tags": it.tags,
	}}
}

func (tr *adoTracker) ids() []int {
	var ids []int
	for id := range tr.items {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// statePatches is every System.State value PATCHed to work item id, in order.
func (tr *adoTracker) statePatches(id int) []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	for _, c := range tr.calls {
		if c.method != "PATCH" || c.path != adoProjectAPI+"/wit/workitems/"+strconv.Itoa(id) {
			continue
		}
		for _, op := range c.ops {
			if op["path"] == "/fields/System.State" {
				out = append(out, fmt.Sprint(op["value"]))
			}
		}
	}
	return out
}

// creates is every work-item create, in order.
func (tr *adoTracker) creates() []adoCall {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []adoCall
	for _, c := range tr.calls {
		if c.method == "POST" && strings.HasPrefix(c.path, adoProjectAPI+"/wit/workitems/$") {
			out = append(out, c)
		}
	}
	return out
}

// itemIDsOf is the ids of the stored items of typ, in order.
func (tr *adoTracker) itemIDsOf(typ string) []int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []int
	for _, id := range tr.ids() {
		if tr.items[id].typ == typ {
			out = append(out, id)
		}
	}
	return out
}

// adoRepo is a .dross root on an azure-devops [board] served by tr, with
// mod applied to the [board] block, and the client resolved through Config
// and forge.NewBoard as cmd's openBoard does.
func adoRepo(t *testing.T, tr *adoTracker, boardJSON string, mod func(*project.Project)) *Ctx {
	t.Helper()
	t.Setenv("MOCK_TOKEN", "secret")
	root := filepath.Join(t.TempDir(), ".dross")
	mustWrite(t, filepath.Join(root, board.File), boardJSON)
	p := &project.Project{}
	p.Remote.URL = tr.URL
	p.Board = project.Board{Enabled: true, Provider: "azure-devops", BaseURL: tr.URL + "/acme", AuthEnv: "MOCK_TOKEN", Project: "Fabrikam"}
	if mod != nil {
		mod(p)
	}
	return repoCtx(t, p, root)
}

// TestConfigCarriesADOFields: the [board] keys only Azure Boards reads reach
// the tracker; [remote] never stands in for them; auth_scheme reaches no
// other board.
func TestConfigCarriesADOFields(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		tr := newADOTracker(t, agileProcess)
		ctx := adoRepo(t, tr, `{}`, func(p *project.Project) {
			p.Board.APIVersion, p.Board.AuthScheme = "6.0", "bearer"
			p.Board.AreaPath, p.Board.IterationPath = `Web`, `Sprint 1`
		})
		phaseRepo(t, ctx, authSpec, "")
		if err := SyncPhase(ctx, "auth", "", false); err != nil {
			t.Fatal(err)
		}
		for _, c := range tr.calls {
			if !strings.Contains(c.query, "api-version=6.0") {
				t.Errorf("%s %s?%s: want api-version=6.0", c.method, c.path, c.query)
			}
			if c.auth != "Bearer secret" {
				t.Errorf("%s %s: Authorization = %q, want a Bearer token", c.method, c.path, c.auth)
			}
		}
		creates := tr.creates()
		if len(creates) != 1 {
			t.Fatalf("%d creates, want 1", len(creates))
		}
		var area, iteration any
		for _, op := range creates[0].ops {
			switch op["path"] {
			case "/fields/System.AreaPath":
				area = op["value"]
			case "/fields/System.IterationPath":
				iteration = op["value"]
			}
		}
		if area != `Fabrikam\Web` || iteration != `Fabrikam\Sprint 1` {
			t.Errorf("create carried AreaPath %v, IterationPath %v; want Fabrikam\\Web, Fabrikam\\Sprint 1", area, iteration)
		}
	})
	t.Run("never from [remote]", func(t *testing.T) {
		tr := newADOTracker(t, agileProcess)
		ctx := adoRepo(t, tr, `{}`, func(p *project.Project) {
			p.Remote.APIVersion, p.Remote.AuthScheme = "5.0", "bearer"
		})
		phaseRepo(t, ctx, authSpec, "")
		if err := SyncPhase(ctx, "auth", "", false); err != nil {
			t.Fatal(err)
		}
		for _, c := range tr.calls {
			if !strings.Contains(c.query, "api-version=7.1") {
				t.Errorf("%s %s?%s: [remote].api_version leaked; want 7.1", c.method, c.path, c.query)
			}
			if !strings.HasPrefix(c.auth, "Basic ") {
				t.Errorf("%s %s: Authorization = %q; [remote].auth_scheme leaked", c.method, c.path, c.auth)
			}
		}
	})
	t.Run("auth_scheme is azure-devops only", func(t *testing.T) {
		b := project.Board{Provider: "gitlab", BaseURL: "https://gitlab.example/api/v4", Project: "g/p", AuthScheme: "bearer"}
		if got := Config(b, "https://gitlab.example/g/p", nil).AuthScheme; got != "" {
			t.Errorf("a gitlab board's AuthScheme = %q, want empty", got)
		}
		b.Provider = " Azure-DevOps "
		if got := Config(b, "https://dev.azure.com/o/p/_git/r", nil).AuthScheme; got != "bearer" {
			t.Errorf("an azure-devops board's AuthScheme = %q, want bearer", got)
		}
	})
}

// TestADOSyncStateByProcess: a phase and its tasks move to the first state of
// the status's category in each process's own order; state_map overrides by
// name; a state the type lacks warns and skips without failing the sync.
func TestADOSyncStateByProcess(t *testing.T) {
	for _, c := range []struct {
		name            string
		proc            adoProcess
		phaseWant, task string
	}{
		{"Agile", agileProcess, "Active", "Active"},
		{"Scrum", scrumProcess, "Committed", "In Progress"},
		{"Basic", basicProcess, "Doing", "Doing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := newADOTracker(t, c.proc)
			ctx := adoRepo(t, tr, `{}`, nil)
			phaseRepo(t, ctx, authSpec, twoTaskPlan)
			if err := SyncPhase(ctx, "auth", StatusInProgress, false); err != nil {
				t.Fatal(err)
			}
			if got := tr.statePatches(10); fmt.Sprint(got) != "["+c.phaseWant+"]" {
				t.Errorf("phase state PATCHes = %v, want [%s]", got, c.phaseWant)
			}
			var err error
			stderr := captureStderr(t, func() { err = SyncTasks(ctx, "auth", "", StatusTaskInProgress, false) })
			if err != nil {
				t.Fatal(err)
			}
			tasks := tr.itemIDsOf("Task")
			if len(tasks) != 2 {
				t.Fatalf("task items = %v, want 2", tasks)
			}
			for _, id := range tasks {
				if got := tr.statePatches(id); fmt.Sprint(got) != "["+c.task+"]" {
					t.Errorf("task %d state PATCHes = %v, want [%s]", id, got, c.task)
				}
			}
			if strings.Contains(stderr, "no workflow state field") {
				t.Errorf("an Azure Boards task sync fell to the stateless arm:\n%s", stderr)
			}
		})
	}
	t.Run("state_map overrides", func(t *testing.T) {
		tr := newADOTracker(t, scrumProcess)
		ctx := adoRepo(t, tr, `{}`, func(p *project.Project) { p.Board.StateMap = map[string]string{"in-progress": "Approved"} })
		phaseRepo(t, ctx, authSpec, "")
		if err := SyncPhase(ctx, "auth", StatusInProgress, false); err != nil {
			t.Fatal(err)
		}
		if got := tr.statePatches(10); fmt.Sprint(got) != "[Approved]" {
			t.Errorf("phase state PATCHes = %v, want [Approved]", got)
		}
	})
	t.Run("a state the type lacks", func(t *testing.T) {
		tr := newADOTracker(t, agileProcess)
		ctx := adoRepo(t, tr, `{}`, func(p *project.Project) {
			p.Board.StateMap = map[string]string{"in-progress": "Blocked", "task-in-progress": "Blocked"}
		})
		phaseRepo(t, ctx, authSpec, twoTaskPlan)
		var phaseErr, taskErr error
		stderr := captureStderr(t, func() {
			phaseErr = SyncPhase(ctx, "auth", StatusInProgress, false)
			taskErr = SyncTasks(ctx, "auth", "", StatusTaskInProgress, false)
		})
		if phaseErr != nil || taskErr != nil {
			t.Fatalf("SyncPhase = %v, SyncTasks = %v; an unreachable state must not fail the sync", phaseErr, taskErr)
		}
		for _, id := range tr.ids() {
			if got := tr.statePatches(id); len(got) != 0 {
				t.Errorf("work item %d got state PATCHes %v", id, got)
			}
			if n := strings.Count(stderr, "work item "+strconv.Itoa(id)+" "); n != 1 {
				t.Errorf("work item %d warned %d times, want once:\n%s", id, n, stderr)
			}
		}
		saved, err := board.Load(ctx.BoardPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := saved.PhaseIssue("auth"); !ok {
			t.Error("board.json was not saved with the phase link")
		}
		for _, task := range []string{"t-1", "t-2"} {
			if _, ok := saved.TaskIssue("auth", task); !ok {
				t.Errorf("board.json was not saved with %s's link", task)
			}
		}
	})
}

// TestADOPhaseCloseReadBack: "(closed)" is printed only for an item that
// reads back Completed; a write the workflow kept from taking fails the
// sync naming the item — and, for tasks, only that task, the rest still
// closing.
func TestADOPhaseCloseReadBack(t *testing.T) {
	t.Run("phase reads back Active", func(t *testing.T) {
		tr := newADOTracker(t, customProcess)
		tr.stick[10] = "Active"
		ctx := adoRepo(t, tr, `{}`, nil)
		phaseRepo(t, ctx, authSpec, "")
		err := SyncPhase(ctx, "auth", "complete", true)
		if err == nil || !strings.Contains(err.Error(), "work item 10") {
			t.Fatalf("err = %v; want an error naming work item 10", err)
		}
		if out := ctx.Out.(fmt.Stringer).String(); strings.Contains(out, "(closed)") {
			t.Errorf("printed (closed) over an item still Active:\n%s", out)
		}
	})
	t.Run("phase reads back Done", func(t *testing.T) {
		tr := newADOTracker(t, customProcess)
		ctx := adoRepo(t, tr, `{}`, nil)
		phaseRepo(t, ctx, authSpec, "")
		if err := SyncPhase(ctx, "auth", "complete", true); err != nil {
			t.Fatal(err)
		}
		if out := ctx.Out.(fmt.Stringer).String(); !strings.Contains(out, "(closed)") {
			t.Errorf("a phase that reads back Done did not print (closed):\n%s", out)
		}
		if got := tr.statePatches(10); len(got) == 0 || got[len(got)-1] != "Done" {
			t.Errorf("phase state PATCHes = %v, want Done", got)
		}
	})
	t.Run("one refused task close strands no other", func(t *testing.T) {
		tr := newADOTracker(t, customProcess)
		ctx := adoRepo(t, tr, `{}`, nil)
		phaseRepo(t, ctx, authSpec, twoTaskPlan)
		if err := SyncPhase(ctx, "auth", "", false); err != nil {
			t.Fatal(err)
		}
		tr.stick[11] = "Active" // t-1's item, for good; t-2's is 12
		// t-2's status write does not take, so its item is still Active when
		// the close runs: only CloseIssueAs can send the second Done.
		tr.stickOnce[12] = "Active"
		var err error
		captureStderr(t, func() { err = SyncTasks(ctx, "auth", "", StatusTaskComplete, true) })
		if err == nil || !strings.Contains(err.Error(), "t-1") || strings.Contains(err.Error(), "t-2") {
			t.Fatalf("err = %v; want a refusal naming t-1 only", err)
		}
		if got := tr.statePatches(12); fmt.Sprint(got) != "[Done Done]" {
			t.Errorf("t-2's item got state PATCHes %v; want its status write, then its own close, to Done", got)
		}
		tr.mu.Lock()
		state := tr.items[12].state
		tr.mu.Unlock()
		if state != "Done" {
			t.Errorf("t-2's item ended %s, want Done", state)
		}
	})
}

// TestBoardClientArmsRouteByType: the type switches in setBoardState,
// SyncPhase and CloseIssue route each tracker to its own state path, and the
// Azure Boards arms sit beside YouTrack's and Jira's without displacing them.
// Each case is built so the generic path would send something different: a
// state_map override only the tracker's own arm honours.
func TestBoardClientArmsRouteByType(t *testing.T) {
	t.Run("youtrack still closes through CloseIssueAs with the state_map", func(t *testing.T) {
		tr := strictTracker(t)
		tr.on("POST", "/api/issues/PROJ-1", `{}`)
		tr.on("GET", "/api/issues/PROJ-1", `{"idReadable":"PROJ-1","resolved":1,"customFields":[{"name":"State","value":{"name":"Verified","isResolved":true}}]}`)
		_, ctx := ytRepoMode(t, tr, `{}`, "")
		ctx.Proj.Board.StateMap = map[string]string{"complete": "Verified"}
		if err := CloseIssue(ctx, "PROJ-1", "complete"); err != nil {
			t.Fatal(err)
		}
		writes := tr.callsTo("POST", "/api/issues/PROJ-1")
		if len(writes) != 1 || !strings.Contains(writes[0].Body, `"Verified"`) {
			t.Errorf("state writes = %+v; want one carrying the state_map's Verified", writes)
		}
	})
	t.Run("jira still closes through SetState then verifyClosed", func(t *testing.T) {
		tr := strictTracker(t)
		tr.on("GET", "/rest/api/3/issue/PROJ-1/transitions",
			`{"transitions":[{"id":"31","to":{"name":"Done","statusCategory":{"key":"done"}}},{"id":"41","to":{"name":"Shipped","statusCategory":{"key":"done"}}}]}`)
		tr.on("POST", "/rest/api/3/issue/PROJ-1/transitions", `{}`)
		tr.on("GET", "/rest/api/3/issue/PROJ-1", `{"key":"PROJ-1","fields":{"status":{"name":"Shipped","statusCategory":{"key":"done"}}}}`)
		_, ctx := jiraRepo(t, tr, `{}`)
		ctx.Proj.Board.StateMap = map[string]string{"complete": "Shipped"}
		if err := CloseIssue(ctx, "PROJ-1", "complete"); err != nil {
			t.Fatal(err)
		}
		moves := tr.callsTo("POST", "/rest/api/3/issue/PROJ-1/transitions")
		if len(moves) != 1 || !strings.Contains(moves[0].Body, `"41"`) {
			t.Errorf("transitions = %+v; want one to the state_map's Shipped (41)", moves)
		}
		var order []string
		for _, c := range tr.calls {
			order = append(order, c.Method+" "+c.Path)
		}
		if n := len(order); n == 0 || order[n-1] != "GET /rest/api/3/issue/PROJ-1" {
			t.Errorf("calls = %v; want the transition verified by a read-back last", order)
		}
	})
	t.Run("azure boards takes its own arm in all three switches", func(t *testing.T) {
		tr := newADOTracker(t, customProcess)
		tr.items[10] = &adoItem{typ: "Story", state: "New", title: "auth — Auth", tags: "dross; dross/phase:auth"}
		tr.items[11] = &adoItem{typ: "Story", state: "New", title: "other", tags: "dross"}
		ctx := adoRepo(t, tr, `{"phases":{"auth":"10"}}`, func(p *project.Project) {
			p.Board.StateMap = map[string]string{"in-progress": "Active", "complete": "Accepted"}
		})
		phaseRepo(t, ctx, authSpec, "")

		var err error
		stderr := captureStderr(t, func() { err = setBoardState(ctx, "11", StatusInProgress, &runWarnings{}) })
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr, "no workflow state field") {
			t.Errorf("setBoardState fell to the stateless default arm:\n%s", stderr)
		}
		if got := tr.statePatches(11); fmt.Sprint(got) != "[Active]" {
			t.Errorf("setBoardState PATCHes = %v, want [Active]", got)
		}

		if err := SyncPhase(ctx, "auth", StatusInProgress, false); err != nil {
			t.Fatal(err)
		}
		if got := tr.statePatches(10); fmt.Sprint(got) != "[Active]" {
			t.Errorf("SyncPhase PATCHes = %v, want [Active]", got)
		}

		// The generic close would be CloseIssue(key) — "complete" with no
		// state_map — and write Story's first Completed state, Done.
		if err := CloseIssue(ctx, "10", "complete"); err != nil {
			t.Fatal(err)
		}
		if got := tr.statePatches(10); fmt.Sprint(got) != "[Active Accepted]" {
			t.Errorf("CloseIssue PATCHes = %v, want the state_map's Accepted after Active", got)
		}
	})
}
