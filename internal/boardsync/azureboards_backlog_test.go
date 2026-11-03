package boardsync

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Backlog links and reap undo on Azure Boards (azure-boards-backend c-2, c-6,
// c-7), on t-16's adoTracker and t-17's relation helpers.

// relationPatches is every relation op a PATCH carried, as sentRelations
// spells them; relatedPatches is those of rel System.LinkTypes.Related.
func (tr *adoTracker) relationPatches() []string {
	var out []string
	for _, r := range tr.sentRelations() {
		if strings.HasPrefix(r, "PATCH ") {
			out = append(out, r)
		}
	}
	return out
}

func (tr *adoTracker) relatedPatches() []string {
	var out []string
	for _, r := range tr.relationPatches() {
		if strings.Contains(r, "System.LinkTypes.Related") {
			out = append(out, r)
		}
	}
	return out
}

// TestADOBacklogRelatesRoutedItem: a deferred item routed to phase p is
// related to p's work item once; a re-run relates nothing again.
func TestADOBacklogRelatesRoutedItem(t *testing.T) {
	tr := newADOTracker(t, agileProcess)
	tr.items[20] = &adoItem{typ: "User Story", state: "Active", title: "p — P", tags: "dross; dross/phase:p"}
	ctx := adoRepo(t, tr, `{"phases":{"p":"20"}}`, nil)
	writeMilestoneScope(t, ctx.Root, "v1.8", "Ship it", "c1")
	mustWrite(t, filepath.Join(ctx.Root, "phases", "src", "spec.toml"),
		"[phase]\nid = \"src\"\ntitle = \"Src\"\n\n[[deferred]]\nid = \"ab\"\ntext = \"an idea\"\ntarget = \"p\"\n")

	sync := func() {
		t.Helper()
		var err error
		stderr := captureStderr(t, func() { err = SyncBacklog(ctx, "v1.8") })
		if err != nil {
			t.Fatalf("SyncBacklog: %v\n%s", err, stderr)
		}
	}
	sync()
	got := tr.relatedPatches()
	if len(got) != 1 || !strings.HasSuffix(got[0], "add System.LinkTypes.Related->20") {
		t.Fatalf("Related PATCHes = %v; want one adding a Related link to work item 20", got)
	}
	before := len(tr.relationPatches())
	sync()
	if again := tr.relationPatches()[before:]; len(again) != 0 {
		t.Errorf("the second sync sent relation PATCHes %v; want none", again)
	}
}

// refuseStateWrites fronts tr, answering 400 to any System.State write of
// state to work item id — Azure Boards refusing a transition (TF401320).
func refuseStateWrites(t *testing.T, tr *adoTracker, id int, state string) string {
	t.Helper()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, fmt.Sprintf("/wit/workitems/%d", id)) &&
			strings.Contains(string(raw), `"/fields/System.State"`) && strings.Contains(string(raw), `"`+state+`"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"TF401320: Rule Error for field State"}`)
			return
		}
		tr.serve(w, r)
	}))
	t.Cleanup(front.Close)
	return front.URL
}

// TestADOReapUndoRestoresPriorState: a sweep closes the cards, and undo puts
// each back in exactly the state it was swept from, with its prior tags; a
// restore the board refuses is reported by id and not counted.
func TestADOReapUndoRestoresPriorState(t *testing.T) {
	uatTags := "dross; dross/phase:p; dross/status:uat"
	for _, c := range []struct {
		name  string
		proc  adoProcess
		typ   string
		prior string
	}{
		{"Scrum card from Committed", scrumProcess, "Product Backlog Item", "Committed"},
		{"Agile User Story from Resolved", agileProcess, "User Story", "Resolved"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := newADOTracker(t, c.proc)
			tr.items[20] = &adoItem{typ: c.typ, state: c.prior, title: "p — P", tags: uatTags}
			ctx := adoRepo(t, tr, `{"phases":{"p":"20"}}`, nil)
			if err := Apply(ctx, &ReapPlan{Cards: []ReapCard{{Key: "20", Lane: "Phases", Terminal: StatusComplete}}}); err != nil {
				t.Fatal(err)
			}
			tr.mu.Lock()
			swept, sweptTags := tr.items[20].state, tr.items[20].tags
			tr.mu.Unlock()
			if swept == c.prior || sweptTags == uatTags {
				t.Fatalf("Apply left work item 20 at %s / %q — the undo would pass vacuously", swept, sweptTags)
			}
			mark := len(tr.statePatches(20))
			if err := Undo(ctx); err != nil {
				t.Fatal(err)
			}
			if got := tr.statePatches(20)[mark:]; fmt.Sprint(got) != "["+c.prior+"]" {
				t.Errorf("undo state PATCHes = %v, want exactly [%s]", got, c.prior)
			}
			tr.mu.Lock()
			state, tags := tr.items[20].state, tr.items[20].tags
			tr.mu.Unlock()
			if state != c.prior || tags != uatTags {
				t.Errorf("after undo: state %q tags %q; want %q, %q", state, tags, c.prior, uatTags)
			}
		})
	}
	t.Run("a refused restore", func(t *testing.T) {
		tr := newADOTracker(t, scrumProcess)
		tr.items[20] = &adoItem{typ: "Product Backlog Item", state: "Committed", title: "p — P", tags: uatTags}
		tr.items[21] = &adoItem{typ: "Product Backlog Item", state: "Committed", title: "q — Q", tags: "dross; dross/phase:q; dross/status:uat"}
		front := refuseStateWrites(t, tr, 21, "Committed")
		ctx := adoRepoAt(t, front, `{"phases":{"p":"20","q":"21"}}`)
		plan := &ReapPlan{Cards: []ReapCard{
			{Key: "20", Lane: "Phases", Terminal: StatusComplete},
			{Key: "21", Lane: "Phases", Terminal: StatusComplete},
		}}
		if err := Apply(ctx, plan); err != nil {
			t.Fatal(err)
		}
		var err error
		stderr := captureStderr(t, func() { err = Undo(ctx) })
		out := ctx.Out.(fmt.Stringer).String()
		if err == nil || !strings.Contains(stderr, "  21: ") || strings.Contains(stderr, "  20: ") {
			t.Fatalf("Undo = %v, stderr %q; want an error and only work item 21 named", err, stderr)
		}
		if !strings.Contains(out, "restored 1 card(s)") {
			t.Errorf("out = %q; want the restored count to cover only work item 20", out)
		}
		tr.mu.Lock()
		s20, s21 := tr.items[20].state, tr.items[21].state
		tr.mu.Unlock()
		if s20 != "Committed" || s21 != "Done" {
			t.Errorf("after undo: 20 is %s, 21 is %s; want 20 restored and 21 still Done", s20, s21)
		}
	})
}
