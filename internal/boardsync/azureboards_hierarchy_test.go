package boardsync

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Rivil/dross/internal/board"
	"github.com/Rivil/dross/internal/project"
)

// The Azure Boards hierarchy (azure-boards-backend c-2, c-4; locked
// hierarchy): a milestone is an Epic, a phase the process's requirement type
// under it, a task a Task under its phase item — every link a
// System.LinkTypes.Hierarchy-Reverse, never a Related one. Built on t-16's
// adoTracker (azureboards_state_sync_test.go).

const adoAuthSpecV18 = "[phase]\nid = \"auth\"\ntitle = \"Auth\"\nmilestone = \"v1.8\"\n"

const adoParentRel = "System.LinkTypes.Hierarchy-Reverse"

// relsOf is work item id's relations as "<rel>-><target id>".
func (tr *adoTracker) relsOf(id int) []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	for _, r := range tr.items[id].rels {
		u := fmt.Sprint(r["url"])
		out = append(out, fmt.Sprint(r["rel"])+"->"+u[strings.LastIndex(u, "/")+1:])
	}
	return out
}

// sentRelations is every relation op value any request carried, as
// "<method> <path> <rel>-><target>".
func (tr *adoTracker) sentRelations() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []string
	for _, c := range tr.calls {
		for _, op := range c.ops {
			if !strings.HasPrefix(fmt.Sprint(op["path"]), "/relations") {
				continue
			}
			v, _ := op["value"].(map[string]any)
			u := fmt.Sprint(v["url"])
			out = append(out, c.method+" "+strings.TrimPrefix(c.path, adoProjectAPI)+" "+fmt.Sprint(op["op"])+" "+fmt.Sprint(v["rel"])+"->"+u[strings.LastIndex(u, "/")+1:])
		}
	}
	return out
}

// adoRepoAt is adoRepo against an Azure Boards at baseURL — a wrapper in
// front of an adoTracker, for a test that injects faults.
func adoRepoAt(t *testing.T, serverURL, boardJSON string) *Ctx {
	t.Helper()
	t.Setenv("MOCK_TOKEN", "secret")
	root := filepath.Join(t.TempDir(), ".dross")
	mustWrite(t, filepath.Join(root, board.File), boardJSON)
	p := &project.Project{}
	p.Remote.URL = serverURL
	p.Board = project.Board{Enabled: true, Provider: "azure-devops", BaseURL: serverURL + "/acme", AuthEnv: "MOCK_TOKEN", Project: "Fabrikam"}
	return repoCtx(t, p, root)
}

// syncAll runs the milestone-bearing phase sync, then the task sync.
func syncAll(t *testing.T, ctx *Ctx) string {
	t.Helper()
	var perr, terr error
	stderr := captureStderr(t, func() {
		perr = SyncPhase(ctx, "auth", "", false)
		if perr == nil {
			terr = SyncTasks(ctx, "auth", "", "", false)
		}
	})
	if perr != nil || terr != nil {
		t.Fatalf("SyncPhase = %v, SyncTasks = %v\n%s", perr, terr, stderr)
	}
	return stderr
}

// TestADOSyncBuildsHierarchy: Epic → requirement type → Task, per process.
func TestADOSyncBuildsHierarchy(t *testing.T) {
	for _, c := range []struct {
		name string
		proc adoProcess
		path string // the requirement type as its create path spells it
	}{
		{"Agile", agileProcess, "$User%20Story"},
		{"Scrum", scrumProcess, "$Product%20Backlog%20Item"},
		{"Basic", basicProcess, "$Issue"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := newADOTracker(t, c.proc)
			ctx := adoRepo(t, tr, `{}`, nil)
			writeMilestoneScope(t, ctx.Root, "v1.8", "Ship it", "c1")
			phaseRepo(t, ctx, adoAuthSpecV18, twoTaskPlan)
			syncAll(t, ctx)

			var paths []string
			for _, cr := range tr.creates() {
				paths = append(paths, strings.TrimPrefix(cr.path, adoProjectAPI+"/wit/workitems/"))
			}
			if want := "[$Epic " + c.path + " $Task $Task]"; fmt.Sprint(paths) != want {
				t.Fatalf("creates = %v, want %s", paths, want)
			}
			// Epic 10, phase item 11, tasks 12 and 13.
			if got := tr.relsOf(11); fmt.Sprint(got) != "["+adoParentRel+"->10]" {
				t.Errorf("phase item relations = %v, want one Hierarchy-Reverse to the Epic", got)
			}
			phaseCreate := tr.creates()[1]
			n := 0
			for _, op := range phaseCreate.ops {
				if op["path"] == "/relations/-" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("the phase create carried %d relation ops, want 1", n)
			}
			for _, id := range []int{12, 13} {
				if got := tr.relsOf(id); fmt.Sprint(got) != "["+adoParentRel+"->11]" {
					t.Errorf("task %d relations = %v, want one Hierarchy-Reverse to the phase item", id, got)
				}
			}
			for _, r := range tr.sentRelations() {
				if strings.Contains(r, "System.LinkTypes.Related") {
					t.Errorf("a Related relation was sent: %s", r)
				}
			}
		})
	}
}

// TestADOResyncUpdatesInPlace: a re-clone (board.json empty) finds every
// item by its tags and updates it in place — no create, and no relation
// write for a link already there; a phase moved to another milestone ends up
// under the new Epic only.
func TestADOResyncUpdatesInPlace(t *testing.T) {
	tr := newADOTracker(t, agileProcess)
	first := adoRepo(t, tr, `{}`, nil)
	writeMilestoneScope(t, first.Root, "v1.8", "Ship it", "c1")
	phaseRepo(t, first, adoAuthSpecV18, twoTaskPlan)
	syncAll(t, first)

	again := adoRepo(t, tr, `{}`, nil)
	writeMilestoneScope(t, again.Root, "v1.8", "Ship it", "c1")
	phaseRepo(t, again, adoAuthSpecV18, twoTaskPlan)
	tr.mu.Lock()
	mark := len(tr.calls)
	tr.mu.Unlock()
	syncAll(t, again)

	tr.mu.Lock()
	rerun := append([]adoCall(nil), tr.calls[mark:]...)
	tr.mu.Unlock()
	patched := map[string]bool{}
	for _, c := range rerun {
		p := strings.TrimPrefix(c.path, adoProjectAPI)
		switch {
		case c.method == "GET", c.method == "POST" && (p == "/wit/wiql" || p == "/wit/workitemsbatch"):
			// reads
		case c.method == "PATCH" && strings.HasPrefix(p, "/wit/workitems/"):
			for _, op := range c.ops {
				if strings.HasPrefix(fmt.Sprint(op["path"]), "/relations") {
					t.Errorf("the re-run rewrote a link already present: %s %v", p, op)
				}
			}
			patched[strings.TrimPrefix(p, "/wit/workitems/")] = true
		default:
			t.Errorf("the re-run sent %s %s; want only reads and PATCH /workitems/<id>", c.method, p)
		}
	}
	for _, id := range []string{"11", "12", "13"} {
		if !patched[id] {
			t.Errorf("work item %s was not updated in place (patched %v)", id, patched)
		}
	}

	t.Run("a moved phase leaves its old Epic", func(t *testing.T) {
		writeMilestoneScope(t, again.Root, "v1.9", "Next", "c1")
		phaseRepo(t, again, strings.Replace(adoAuthSpecV18, "v1.8", "v1.9", 1), "")
		if err := SyncPhase(again, "auth", "", false); err != nil {
			t.Fatal(err)
		}
		epics := tr.itemIDsOf("Epic")
		if len(epics) != 2 {
			t.Fatalf("Epics = %v, want v1.8's and v1.9's", epics)
		}
		if got := tr.relsOf(11); fmt.Sprint(got) != "["+adoParentRel+"->"+strconv.Itoa(epics[1])+"]" {
			t.Errorf("phase item relations = %v, want only the v1.9 Epic (%d)", got, epics[1])
		}
	})
}

// TestADOTaskParentFaultHeals: a parent link that fails on one Task warns
// once, the other Tasks are still parented, and the next run heals it.
func TestADOTaskParentFaultHeals(t *testing.T) {
	tr := newADOTracker(t, agileProcess)
	failures := 1
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/wit/workitems/12") && strings.Contains(string(raw), "/relations/") && failures > 0 {
			failures--
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tr.serve(w, r)
	}))
	t.Cleanup(front.Close)
	ctx := adoRepoAt(t, front.URL, `{}`)
	phaseRepo(t, ctx, authSpec, twoTaskPlan)
	if err := SyncPhase(ctx, "auth", "", false); err != nil {
		t.Fatal(err)
	}

	var err error
	stderr := captureStderr(t, func() { err = SyncTasks(ctx, "auth", "", "", false) })
	if err != nil {
		t.Fatalf("a link fault failed the sync: %v", err)
	}
	if n := warningLines(stderr, "could not put task issues under"); n != 1 {
		t.Errorf("%d link warnings, want 1:\n%s", n, stderr)
	}
	if got := tr.relsOf(11); fmt.Sprint(got) != "["+adoParentRel+"->10]" {
		t.Errorf("t-1's item relations = %v; the fault on t-2 stranded it", got)
	}
	if got := tr.relsOf(12); len(got) != 0 {
		t.Fatalf("t-2's item relations = %v before the retry", got)
	}

	stderr = captureStderr(t, func() { err = SyncTasks(ctx, "auth", "", "", false) })
	if err != nil || strings.Contains(stderr, "could not put") {
		t.Fatalf("the healing run: %v\n%s", err, stderr)
	}
	if got := tr.relsOf(12); fmt.Sprint(got) != "["+adoParentRel+"->10]" {
		t.Errorf("t-2's item relations = %v after the retry, want the phase item", got)
	}
	if got := tr.relsOf(11); len(got) != 1 {
		t.Errorf("t-1's item relations = %v; the retry duplicated a link", got)
	}
}

// TestADOMilestoneClose: an Epic is an issue, so it closes in any
// milestone_mode — verified on read-back — and an Epic already tagged for
// the milestone is reused, never duplicated.
func TestADOMilestoneClose(t *testing.T) {
	t.Run("closable in any mode", func(t *testing.T) {
		tr := newADOTracker(t, agileProcess)
		for _, mode := range []string{"", "version", "agile"} {
			ctx := adoRepo(t, tr, `{}`, func(p *project.Project) { p.Board.MilestoneMode = mode })
			if err := CheckMilestoneClosable(ctx); err != nil {
				t.Errorf("milestone_mode %q: %v", mode, err)
			}
		}
		ctx, _ := boardCtx(t, newFaultBoard())
		ctx.Proj.Board.Provider = "forgejo"
		if err := CheckMilestoneClosable(ctx); err == nil {
			t.Error("a forgejo board's milestone was closable")
		}
	})
	for _, c := range []struct {
		stick string
		ok    bool
	}{{"", true}, {"Active", false}, {"Resolved", false}} {
		t.Run("reads back "+map[bool]string{true: "Closed", false: c.stick}[c.ok], func(t *testing.T) {
			tr := newADOTracker(t, agileProcess)
			tr.items[10] = &adoItem{typ: "Epic", state: "Active", title: "v1.8 — Ship it", tags: "dross; dross/milestone:v1.8"}
			tr.stick[10] = c.stick
			ctx := adoRepo(t, tr, `{}`, nil)
			err := CloseIssue(ctx, "10", "")
			switch {
			case c.ok && err != nil:
				t.Errorf("CloseIssue = %v, want nil", err)
			case !c.ok && (err == nil || !strings.Contains(err.Error(), "work item 10")):
				t.Errorf("CloseIssue = %v; want an error naming work item 10", err)
			}
		})
	}
	t.Run("an existing Epic is reused", func(t *testing.T) {
		tr := newADOTracker(t, agileProcess)
		tr.items[10] = &adoItem{typ: "Epic", state: "New", title: "v1.8 — Ship it", tags: "dross; dross/milestone:v1.8"}
		ctx := adoRepo(t, tr, `{}`, nil)
		writeMilestoneScope(t, ctx.Root, "v1.8", "Ship it", "c1")
		id, err := EnsureMilestoneLink(ctx, "v1.8")
		if err != nil || id != "10" {
			t.Fatalf("EnsureMilestoneLink = %q, %v; want the tagged Epic 10", id, err)
		}
		if n := len(tr.creates()); n != 0 {
			t.Errorf("%d creates with the Epic already on the board", n)
		}
	})
}

// TestLinkerOnlyBoardStillRelatesTasks: a backend with LinkIssues and no
// LinkParent keeps the relation it always got, for every task.
func TestLinkerOnlyBoardStillRelatesTasks(t *testing.T) {
	l := newLinkBoard()
	ctx, _ := boardCtx(t, l)
	ctx.Board.SetPhase("auth", "PROJ-100")
	phaseRepo(t, ctx, authSpec, twoTaskPlan)
	var err error
	captureStderr(t, func() { err = SyncTasks(ctx, "auth", "", "", false) })
	if err != nil {
		t.Fatal(err)
	}
	if len(l.links) != 2 {
		t.Fatalf("links = %v, want one per task", l.links)
	}
	children := map[string]bool{}
	for _, link := range l.links {
		if link[0] != "PROJ-100" {
			t.Errorf("link %v, want it from the phase issue PROJ-100", link)
		}
		children[link[1]] = true
	}
	for _, task := range []string{"t-1", "t-2"} {
		key, ok := ctx.Board.TaskIssue("auth", task)
		if !ok || !children[key] {
			t.Errorf("%s's issue %q was not linked (children %v)", task, key, children)
		}
	}
}
