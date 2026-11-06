package cmd

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The issue commands against an azure-devops [board] (azure-boards-backend
// c-1, c-5, c-7), on the fake in ado_board_fake_test.go.

// adoPhaseFiles gives the repo phase x (milestone v1.8, two tasks) and the
// milestone, which also lists the unscaffolded y, so every board verb has
// work to do.
func adoPhaseFiles(t *testing.T, dir string) {
	t.Helper()
	writeMilestoneWithPhases(t, filepath.Join(dir, ".dross"), "v1.8", []string{"x", "y"})
	writeSpec(t, dir, "x", "[phase]\nid = \"x\"\ntitle = \"Tagging\"\nmilestone = \"v1.8\"\n\n[[criteria]]\nid = \"c-1\"\ntext = \"Tags can be added\"\n")
	writePlan(t, dir, "x", "task_seq = 2\n\n[phase]\n  id = \"x\"\n\n[[task]]\n  id = \"t-1\"\n  wave = 1\n  title = \"one\"\n  status = \"pending\"\n\n[[task]]\n  id = \"t-2\"\n  wave = 1\n  title = \"two\"\n  status = \"pending\"\n")
}

// TestEveryBoardCommandUsesAzureBoards: each board verb reaches the Azure
// Boards project API and nothing reaches the [remote] host.
func TestEveryBoardCommandUsesAzureBoards(t *testing.T) {
	f := newADOFake(t)
	f.seed(7, "Bug", "New", "")
	dir, remoteHits := adoBoardRepo(t, f)
	adoPhaseFiles(t, dir)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"issue pull", []string{"pull", "--json"}},
		{"issue milestone sync", []string{"milestone", "sync", "v1.8"}},
		{"issue phase sync", []string{"phase", "sync", "x"}},
		{"issue task sync", []string{"task", "sync", "x"}},
		{"issue backlog sync", []string{"backlog", "sync", "v1.8"}},
		{"issue reap (plan)", []string{"reap"}},
	} {
		before := f.projectRequests()
		var err error
		captureStdout(t, func() { err = runCmd(t, Issue(), c.args...) })
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if sent := f.projectRequests() - before; sent < 1 {
			t.Errorf("%s sent nothing to %s", c.name, f.projectAPI())
		}
	}
	if n := remoteHits(); n != 0 {
		t.Errorf("%d request(s) reached the [remote] host", n)
	}
	if tasks := f.idsOf("Task"); len(tasks) != 2 {
		t.Errorf("task items = %v, want the two task sync created", tasks)
	}
}

// TestIssuePullADOEnvelope: pull lists the hand-filed Bug (by type, untagged)
// and the item tagged enhancement — never a dross mirror, a closed Bug or an
// item board.json links — and an outage is an error, not an empty board.
func TestIssuePullADOEnvelope(t *testing.T) {
	f := newADOFake(t)
	f.seed(7, "Bug", "New", "")                         // a Bug by type, untagged
	f.seed(8, "Bug", "New", "dross")                    // a dross mirror
	f.seed(9, adoFakeRequirement, "New", "enhancement") // tagged
	f.seed(10, "Bug", "Done", "")                       // closed
	f.seed(11, "Task", "To Do", "")                     // neither label
	f.seed(12, "Bug", "Committed", "")                  // linked in board.json
	dir, _ := adoBoardRepo(t, f)
	mustWrite(t, filepath.Join(dir, ".dross", "board.json"), `{"phases":{"x":"12"}}`)

	pull := func(t *testing.T) (string, pullResult) {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = runCmd(t, Issue(), "pull", "--labels", "bug,enhancement", "--json") })
		if err != nil {
			t.Fatalf("pull exited non-zero: %v", err)
		}
		return out, decodePull(t, out)
	}
	_, got := pull(t)
	if got.Error != nil {
		t.Errorf("error = %q on a healthy board, want null", *got.Error)
	}
	var keys []string
	for _, iss := range got.Issues {
		keys = append(keys, iss.Key)
	}
	if strings.Join(keys, ",") != "7,9" {
		t.Errorf("issues = %v, want [7 9]", keys)
	}

	t.Run("an outage is not an empty board", func(t *testing.T) {
		f.mu.Lock()
		f.wiqlStatus = 503
		f.mu.Unlock()
		out, got := pull(t)
		if !strings.Contains(out, `"issues":[]`) || len(got.Issues) != 0 {
			t.Errorf("issues = %v, want []:\n%s", got.Issues, out)
		}
		if got.Error == nil || !strings.Contains(*got.Error, "HTTP 503") {
			t.Errorf("error = %v, want one naming HTTP 503", got.Error)
		}
	})
}

// TestADOStatusChangeReplacesStatusTag: moving a card to a new status leaves
// exactly that dross/status: tag on it. The live service merges a tags "add"
// into the set the item has (t-21), so an update that adds would strand the
// old status tag beside the new one.
func TestADOStatusChangeReplacesStatusTag(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		steps     [][]string
		want      string
	}{
		{"task", "Task", [][]string{
			{"phase", "sync", "x"},
			{"task", "sync", "x", "t-1", "--status", "task-in-progress"},
			{"task", "sync", "x", "t-1", "--status", "task-complete"},
		}, "dross/status:task-complete"},
		{"phase", adoFakeRequirement, [][]string{
			{"phase", "sync", "x", "--status", "planned"},
			{"phase", "sync", "x", "--status", "in-progress"},
		}, "dross/status:in-progress"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newADOFake(t)
			dir, _ := adoBoardRepo(t, f)
			adoPhaseFiles(t, dir)
			for _, args := range c.steps {
				var err error
				captureStdout(t, func() { err = runCmd(t, Issue(), args...) })
				if err != nil {
					t.Fatalf("issue %v: %v", args, err)
				}
			}
			ids := f.idsOf(c.typ)
			if len(ids) != 1 {
				t.Fatalf("%s items = %v, want one", c.typ, ids)
			}
			f.mu.Lock()
			tags := f.items[ids[0]].tags
			f.mu.Unlock()
			var status []string
			for _, tag := range strings.Split(tags, ";") {
				if tag = strings.TrimSpace(tag); strings.HasPrefix(tag, "dross/status:") {
					status = append(status, tag)
				}
			}
			if len(status) != 1 || status[0] != c.want {
				t.Errorf("work item %d status tags = %v, want only %s (tags %q)", ids[0], status, c.want, tags)
			}
		})
	}
}

// TestIssueCloseADOReadBack: with every state write refused — or accepted and
// not recorded, which only the read-back can catch — --close fails naming the
// work item and never prints "(closed)" over an open item.
func TestIssueCloseADOReadBack(t *testing.T) {
	for _, mode := range []string{"refused", "accepted but not recorded"} {
		t.Run(mode, func(t *testing.T) { adoCloseReadBack(t, mode) })
	}
}

func adoCloseReadBack(t *testing.T, mode string) {
	f := newADOFake(t)
	dir, _ := adoBoardRepo(t, f)
	adoPhaseFiles(t, dir)
	f.mu.Lock()
	f.refuseState = mode == "refused"
	f.dropState = mode != "refused"
	f.mu.Unlock()
	for _, c := range []struct {
		name, typ string
		args      []string
	}{
		{"phase", adoFakeRequirement, []string{"phase", "sync", "x", "--status", "complete", "--close"}},
		{"milestone", "Epic", []string{"milestone", "sync", "v1.8", "--close"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = runCmd(t, Issue(), c.args...) })
			ids := f.idsOf(c.typ)
			if len(ids) != 1 {
				t.Fatalf("%s items = %v, want one", c.typ, ids)
			}
			if err == nil || !strings.Contains(err.Error(), "work item "+strconv.Itoa(ids[0])) {
				t.Errorf("err = %v; want a failure naming work item %d", err, ids[0])
			}
			if strings.Contains(out, "(closed)") {
				t.Errorf("printed (closed) over an open item:\n%s", out)
			}
			if s := f.stateOf(ids[0]); s == "Done" {
				t.Errorf("work item %d reads Done though every state write was refused", ids[0])
			}
		})
	}
}
