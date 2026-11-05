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
)

// FinalizePhase on Azure Boards (azure-boards-backend c-4): task items close
// before the phase item, each verified on read-back; a card created only at
// finalize goes under the phase card; a re-run only reads.

// finalizeADO is phase auth (two tasks) on tr, synced as far as upTo says
// ("phase" or "tasks"; "none" leaves every card for finalize to create),
// with changes.json complete.
func finalizeADO(t *testing.T, tr *adoTracker, upTo string) *Ctx {
	t.Helper()
	ctx := adoRepo(t, tr, `{}`, nil)
	phaseRepo(t, ctx, authSpec, twoTaskPlan)
	captureStderr(t, func() {
		if upTo == "none" {
			return
		}
		if err := SyncPhase(ctx, "auth", "", false); err != nil {
			t.Fatal(err)
		}
		if upTo == "tasks" {
			if err := SyncTasks(ctx, "auth", "", "", false); err != nil {
				t.Fatal(err)
			}
		}
	})
	mustWrite(t, filepath.Join(ctx.Root, "phases", "auth", "changes.json"), `{"phase":"auth","status":"complete","tasks":{}}`)
	return ctx
}

// doneOrder is the work items PATCHed to state, in the order the PATCHes
// were sent.
func (tr *adoTracker) doneOrder(state string) []int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []int
	for _, c := range tr.calls {
		if c.method != "PATCH" {
			continue
		}
		for _, op := range c.ops {
			if op["path"] == "/fields/System.State" && op["value"] == state {
				id, _ := strconv.Atoi(c.path[strings.LastIndex(c.path, "/")+1:])
				out = append(out, id)
			}
		}
	}
	return out
}

// TestADOFinalizeTasksThenPhase: both task items reach Done before the
// phase item does, and every one reads back Completed.
func TestADOFinalizeTasksThenPhase(t *testing.T) {
	tr := newADOTracker(t, scrumProcess)
	ctx := finalizeADO(t, tr, "tasks") // phase 10, tasks 11 and 12
	var err error
	captureStderr(t, func() { err = FinalizePhase(ctx, "auth") })
	if err != nil {
		t.Fatal(err)
	}
	order := tr.doneOrder("Done")
	last := map[int]int{}
	for i, id := range order {
		last[id] = i
	}
	for _, task := range []int{11, 12} {
		if _, ok := last[task]; !ok {
			t.Fatalf("task item %d was never PATCHed to Done (order %v)", task, order)
		}
	}
	phaseFirst := -1
	for i, id := range order {
		if id == 10 {
			phaseFirst = i
			break
		}
	}
	if phaseFirst < 0 || phaseFirst < last[11] || phaseFirst < last[12] {
		t.Errorf("Done PATCH order = %v; want both task items before the phase item 10", order)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, id := range []int{10, 11, 12} {
		if s := tr.items[id].state; s != "Done" {
			t.Errorf("work item %d reads %s, want Done", id, s)
		}
	}
}

// TestADOFinalizeParentsCreatedCards: a task card missing from the board is
// created under the phase card — one Hierarchy-Reverse, no Related.
func TestADOFinalizeParentsCreatedCards(t *testing.T) {
	tr := newADOTracker(t, scrumProcess)
	ctx := finalizeADO(t, tr, "phase") // phase 10; no task items yet
	var err error
	captureStderr(t, func() { err = FinalizePhase(ctx, "auth") })
	if err != nil {
		t.Fatal(err)
	}
	tasks := tr.itemIDsOf("Task")
	if len(tasks) != 2 {
		t.Fatalf("task items = %v, want 2 created at finalize", tasks)
	}
	for _, id := range tasks {
		if got := tr.relsOf(id); fmt.Sprint(got) != "["+adoParentRel+"->10]" {
			t.Errorf("task item %d relations = %v, want exactly one Hierarchy-Reverse to the phase card", id, got)
		}
	}
}

// TestADOFinalizeParentFaultStillCloses: a parent link refused for every
// created card warns once, and the cards still close.
func TestADOFinalizeParentFaultStillCloses(t *testing.T) {
	tr := newADOTracker(t, scrumProcess)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if r.Method == "PATCH" && strings.Contains(string(raw), `"/relations/-"`) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tr.serve(w, r)
	}))
	t.Cleanup(front.Close)
	ctx := adoRepoAt(t, front.URL, `{}`)
	phaseRepo(t, ctx, authSpec, twoTaskPlan)
	captureStderr(t, func() {
		if err := SyncPhase(ctx, "auth", "", false); err != nil {
			t.Fatal(err)
		}
	})
	mustWrite(t, filepath.Join(ctx.Root, "phases", "auth", "changes.json"), `{"phase":"auth","status":"complete","tasks":{}}`)
	var err error
	stderr := captureStderr(t, func() { err = FinalizePhase(ctx, "auth") })
	if err != nil {
		t.Fatalf("a refused parent link failed finalize: %v", err)
	}
	if n := warningLines(stderr, "could not put task cards under"); n != 1 {
		t.Errorf("%d parent-link warnings, want 1:\n%s", n, stderr)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, id := range []int{10, 11, 12} {
		if s := tr.items[id].state; s != "Done" {
			t.Errorf("work item %d reads %s; the link fault stopped its close", id, s)
		}
	}
}

// TestADOFinalizeRerunOnlyReads: a second finalize finds every card done and
// writes nothing.
func TestADOFinalizeRerunOnlyReads(t *testing.T) {
	tr := newADOTracker(t, scrumProcess)
	ctx := finalizeADO(t, tr, "tasks")
	captureStderr(t, func() {
		if err := FinalizePhase(ctx, "auth"); err != nil {
			t.Fatal(err)
		}
	})
	tr.mu.Lock()
	mark := len(tr.calls)
	tr.mu.Unlock()
	out := ctx.Out.(interface {
		fmt.Stringer
		Reset()
	})
	out.Reset()
	var err error
	captureStderr(t, func() { err = FinalizePhase(ctx, "auth") })
	if err != nil {
		t.Fatal(err)
	}
	tr.mu.Lock()
	rerun := append([]adoCall(nil), tr.calls[mark:]...)
	tr.mu.Unlock()
	for _, c := range rerun {
		p := strings.TrimPrefix(c.path, adoProjectAPI)
		if c.method == "PATCH" || (c.method == "POST" && strings.HasPrefix(p, "/wit/workitems/$")) {
			t.Errorf("the re-run wrote: %s %s", c.method, p)
		}
	}
	if !strings.Contains(out.String(), "already complete") {
		t.Errorf("narration = %q; want already complete", out.String())
	}
}

// TestADOFinalizeReportsRefusedTaskClose: a task item that reads back In
// Progress after its close fails finalize by name, while the other cards and
// the phase item still close.
func TestADOFinalizeReportsRefusedTaskClose(t *testing.T) {
	tr := newADOTracker(t, scrumProcess)
	ctx := finalizeADO(t, tr, "tasks")
	tr.mu.Lock()
	tr.stick[12] = "In Progress" // t-2's item
	tr.mu.Unlock()
	var err error
	captureStderr(t, func() { err = FinalizePhase(ctx, "auth") })
	if err == nil || !strings.Contains(err.Error(), "t-2") || strings.Contains(err.Error(), "t-1") {
		t.Fatalf("err = %v; want a failure naming t-2 only", err)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, id := range []int{10, 11} {
		if s := tr.items[id].state; s != "Done" {
			t.Errorf("work item %d reads %s; one refused task stranded it", id, s)
		}
	}
}

// TestADOFinalizeReportsRefusedPhaseClose: a phase item that reads back
// Committed after finalize's close — whether the card was synced before or
// created by finalize itself — fails finalize naming the phase card and no
// task, is narrated NOT closed and never "(closed)", and does not strand the
// task items, which still close.
func TestADOFinalizeReportsRefusedPhaseClose(t *testing.T) {
	for _, upTo := range []string{"tasks", "none"} {
		t.Run("synced to "+upTo, func(t *testing.T) {
			tr := newADOTracker(t, scrumProcess)
			ctx := finalizeADO(t, tr, upTo) // phase 10, tasks 11 and 12
			tr.mu.Lock()
			tr.stick[10] = "Committed"
			tr.mu.Unlock()
			out := ctx.Out.(interface {
				fmt.Stringer
				Reset()
			})
			out.Reset()
			var err error
			captureStderr(t, func() { err = FinalizePhase(ctx, "auth") })
			if err == nil || !strings.Contains(err.Error(), "phase 10:") || strings.Contains(err.Error(), "task ") {
				t.Fatalf("err = %v; want a failure naming the phase card 10 and no task", err)
			}
			narration := out.String()
			if strings.Contains(narration, "(closed)") || strings.Contains(narration, "created, closed") || strings.Contains(narration, "already complete") {
				t.Errorf("narration = %q; a phase card that read back Committed was reported done", narration)
			}
			if !strings.Contains(narration, "phase auth -> board 10 (NOT closed); 2 task card(s) closed") {
				t.Errorf("narration = %q; want the phase card NOT closed and both task cards closed", narration)
			}
			tr.mu.Lock()
			defer tr.mu.Unlock()
			if s := tr.items[10].state; s != "Committed" {
				t.Errorf("phase item 10 reads %s; the refusal fixture did not hold", s)
			}
			for _, id := range []int{11, 12} {
				if s := tr.items[id].state; s != "Done" {
					t.Errorf("task item %d reads %s; the refused phase close stranded it", id, s)
				}
			}
		})
	}
}
