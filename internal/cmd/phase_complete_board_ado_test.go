package cmd

import (
	"path/filepath"
	"testing"
)

// `dross phase complete` against an azure-devops [board] (azure-boards-backend
// c-4, c-7), on the fake in ado_board_fake_test.go.

// adoCompleteFixture ships phase x — with a two-task plan — through the mock
// forge, squash-merges it on origin, and points [board] at f, a host other
// than the remote's. It stops just before `dross phase complete`.
func adoCompleteFixture(t *testing.T, f *adoFake) string {
	t.Helper()
	dir := shipFixture(t, "https://forge.example/me/p.git")
	shipMockFlow(t, dir)
	stubPRMerged(t, true)
	pointBoardAtADO(t, dir, f)
	writePlan(t, dir, "x", "task_seq = 2\n\n[phase]\n  id = \"x\"\n\n[[task]]\n  id = \"t-1\"\n  wave = 1\n  title = \"one\"\n  status = \"done\"\n\n[[task]]\n  id = \"t-2\"\n  wave = 1\n  title = \"two\"\n  status = \"done\"\n")
	gitCommit(t, dir, "test: board config and a plan")

	mustGit(t, dir, "push", "-q", "origin", "main:main")
	mustGit(t, dir, "fetch", "-q", "origin")
	if err := runCmd(t, Ship()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	simulateSquashMerge(t, dir, "phase/x", "main")
	return dir
}

// TestCompleteFinalizesADOBoard: complete leaves both task items and then the
// phase item in Done — the tasks' state writes ahead of the phase's — and a
// second run writes nothing and leaves board.json byte-identical.
func TestCompleteFinalizesADOBoard(t *testing.T) {
	f := newADOFake(t)
	dir := adoCompleteFixture(t, f)
	if err := runCmd(t, Phase(), "complete", "x"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	phaseItems, tasks := f.idsOf(adoFakeRequirement), f.idsOf("Task")
	if len(phaseItems) != 1 || len(tasks) != 2 {
		t.Fatalf("phase items %v, task items %v; want one and two", phaseItems, tasks)
	}
	for _, id := range append(append([]int{}, tasks...), phaseItems...) {
		if s := f.stateOf(id); s != "Done" {
			t.Errorf("work item %d reads %s, want Done", id, s)
		}
	}
	order := f.stateWrites("Done")
	pos := func(id int, last bool) int {
		at := -1
		for i, x := range order {
			if x == id {
				at = i
				if !last {
					break
				}
			}
		}
		return at
	}
	phaseAt := pos(phaseItems[0], false)
	for _, task := range tasks {
		if at := pos(task, true); at < 0 || phaseAt < 0 || at > phaseAt {
			t.Errorf("Done writes in order %v; want task item %d ahead of phase item %d", order, task, phaseItems[0])
		}
	}

	boardPath := filepath.Join(dir, ".dross", "board.json")
	before := mustRead(t, boardPath)
	mark := f.logLen()
	if err := runCmd(t, Phase(), "complete", "x"); err != nil {
		t.Fatalf("re-running complete: %v", err)
	}
	if writes := f.writesSince(mark); len(writes) != 0 {
		t.Errorf("a re-run wrote to the board: %v", writes)
	}
	if mustRead(t, boardPath) != before {
		t.Error("a re-run changed board.json")
	}
}
