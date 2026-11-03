package boardsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Rivil/dross/internal/board"
	"github.com/Rivil/dross/internal/forge"
	"github.com/Rivil/dross/internal/phase"
)

// The board mirror at task granularity: one issue per plan task, related to
// the phase's, driven through the tracker's OWN workflow field rather than a
// dross label — a label is dross talking to itself; the state field is what
// the tracker's columns read.

// TaskCloseError is a close that the tracker refused for ONE card. It is a
// distinct type so syncTasks can tell it apart from a create/update failure:
// a refused close must not stop the remaining cards from reaching their
// terminal state — one stuck card would otherwise strand every card after it,
// which is the exact defect this phase exists to fix.
type TaskCloseError struct {
	taskID string
	err    error
}

func (e *TaskCloseError) Error() string { return fmt.Sprintf("task %s: %v", e.taskID, e.err) }

func (e *TaskCloseError) Unwrap() error { return e.err }

// SyncTasks mirrors one phase's plan tasks.
func SyncTasks(ctx *Ctx, phaseID, only, status string, doClose bool) error {
	dir := phase.Dir(ctx.Root, phaseID)
	plan, err := phase.LoadPlan(filepath.Join(dir, "plan.toml"))
	if err != nil {
		return fmt.Errorf("load plan for %s: %w", phaseID, err)
	}

	// The phase's own issue is the parent. Resolved once — not per task —
	// because every child relates to the same one, and re-resolving would be
	// a tracker round trip per task for an answer that cannot change mid-run.
	parent, ok := ctx.Board.PhaseIssue(phaseID)
	if !ok {
		return fmt.Errorf("phase %s has no board issue yet — run `dross issue phase sync %s` first", phaseID, phaseID)
	}

	// A tracker with a real parent link (Azure Boards' Hierarchy-Reverse)
	// is preferred over a plain relation: a Task under its phase item is what
	// the board's hierarchy views read, where a Related link groups nothing.
	parentLinker, canParent := ctx.Client.(forge.ParentLinker)
	linker, canLink := ctx.Client.(forge.IssueLinker)
	warn := &runWarnings{}
	var refused []string

	for _, t := range plan.Task {
		if only != "" && t.ID != only {
			continue
		}
		key, err := syncOneTask(ctx, phaseID, parent, t, status, doClose, warn)
		if err != nil {
			var closeErr *TaskCloseError
			if errors.As(err, &closeErr) {
				// Record and keep going. The command still fails at the end,
				// naming every card the tracker refused.
				refused = append(refused, closeErr.Error())
				continue
			}
			return err
		}
		if canParent {
			if err := parentLinker.LinkParent(parent, key); err != nil {
				// Once per run, the rest still parented; the next sync
				// finds the task unparented and retries.
				warn.once(&warn.noLink, "could not put task issues under %s (%v) — they carry the phase label; the next sync retries", parent, err)
			}
			continue
		}
		if !canLink {
			// ONCE per run, not once per task: an eight-task phase would
			// otherwise print eight identical lines, which is how a warning
			// becomes scrollback.
			warn.once(&warn.noLink, "%s cannot relate issues — task issues carry %s and the phase label instead of a link",
				ctx.Proj.Board.Provider, TaskLabel(phaseID, "<task>"))
			continue
		}
		if err := linker.LinkIssues(parent, key); err != nil {
			// A capability gap the tracker reports at call time rather than
			// through the interface. Same floor: say it once, keep going —
			// the issues themselves are the substance, and the link is how
			// they are grouped.
			warn.once(&warn.noLink, "could not relate task issues to %s (%v) — they carry the phase label instead", parent, err)
		}
	}
	// Saved before the verdict: the cards that DID close have their links (and,
	// where they closed, their agreement points) recorded, so a partial run is
	// resumable rather than repeated wholesale.
	if err := ctx.Board.Save(ctx.BoardPath); err != nil {
		return err
	}
	if len(refused) > 0 {
		return fmt.Errorf("the tracker refused %d task close(s):\n  %s", len(refused), strings.Join(refused, "\n  "))
	}
	return nil
}

// runWarnings holds the once-per-run gates for the capability gaps a task sync
// can hit — a backend that cannot relate issues, and one with no workflow state
// field at all.
//
// Run-scoped rather than package-scoped, deliberately: the locked
// warn_once_per_run decision is about one RUN not repeating itself, and a
// package-level flag would also silence the next invocation, which is a
// different and much worse thing.
type runWarnings struct {
	noLink  bool
	noState bool
}

// once prints a warning the first time its gate is unset, and sets it. The gate
// is passed explicitly so each call site names which gap it is reporting.
func (w *runWarnings) once(gate *bool, format string, args ...any) {
	if *gate {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
	*gate = true
}

// syncOneTask creates or updates the issue for one task and returns its key.
func syncOneTask(ctx *Ctx, phaseID, parent string, t phase.Task, status string, doClose bool, warn *runWarnings) (string, error) {
	title := fmt.Sprintf("%s/%s — %s", phaseID, t.ID, t.Title)
	body := RenderTaskBody(phaseID, parent, t)
	labels := []string{LabelMarker, PhaseLabel(phaseID), TaskLabel(phaseID, t.ID)}
	if status != "" {
		labels = append(labels, StatusLabel(status))
	}

	key, err := resolveTaskIssue(ctx, phaseID, t.ID)
	if err != nil {
		return "", err
	}
	if key == "" {
		iss, err := ctx.Client.CreateIssue(forge.IssueInput{Title: title, Body: body, Labels: labels})
		if err != nil {
			return "", Wrap(err)
		}
		key = iss.Key
	} else {
		patch := forge.IssuePatch{Title: &title, Body: &body, Labels: &labels}
		if _, err := ctx.Client.UpdateIssue(key, patch); err != nil {
			return "", Wrap(err)
		}
	}
	// The tracker's own field, not the label above. The label is dross talking
	// to itself; the state field is what the board's columns read, and moving
	// it is the whole point of mirroring a task at all.
	if status != "" {
		if err := setBoardState(ctx, key, status, warn); err != nil {
			return "", err
		}
	}
	// The close goes through CloseIssue, so a task card is resolved the
	// same verified way a phase card is — and on the flat boards it plainly
	// closes rather than refusing by name, which would strand every task card
	// on forgejo, gitea, gitlab and github forever.
	if doClose {
		if err := CloseIssue(ctx, key, status); err != nil {
			// The LINK is still recorded — losing it would cost a label query
			// on every later run — but no agreement point: the card did not
			// reach the state the run was about to claim it had, and
			// `task-pull` compares against that claim.
			ctx.Board.SetTask(phaseID, t.ID, key)
			return key, &TaskCloseError{taskID: t.ID, err: err}
		}
	}

	// Record the AGREEMENT POINT, not just the mapping: what the plan held and
	// what the board was told, at this moment. `dross issue task pull` compares
	// against this to tell a board move from a plan move from both — which two
	// current values cannot distinguish. Recorded AFTER the state write and the
	// close, so it only ever describes a card that actually got there.
	//
	// Only when a status was actually asserted. A sync run without --status
	// wipes the dross/status label (the patch replaces the whole label set), so
	// claiming an agreement on a value neither side now shows would make the
	// next pull read a phantom move.
	if status != "" {
		ctx.Board.SetTaskSynced(phaseID, t.ID, key, t.Status, status)
	} else {
		ctx.Board.SetTask(phaseID, t.ID, key)
	}
	return key, nil
}

// resolveTaskIssue finds an existing issue for a task: the board cache first,
// then the tracker by label.
//
// Same two-step as ResolvePhaseIssue, and for the same reason: board.json is a
// cache, so an entry that no longer resolves must not shadow the live issue,
// and a re-clone with no cache must still find what is already there rather
// than creating a second issue for every task.
func resolveTaskIssue(ctx *Ctx, phaseID, taskID string) (string, error) {
	if key, ok := ctx.Board.TaskIssue(phaseID, taskID); ok {
		if iss, err := ctx.Client.GetIssue(key); err == nil && iss != nil && HasMarker(*iss) {
			return key, nil
		}
		fmt.Fprintf(os.Stderr, "warning: board.json points %s at %s, which no longer resolves — re-resolving from the tracker\n",
			board.TaskKey(phaseID, taskID), key)
		delete(ctx.Board.Tasks, board.TaskKey(phaseID, taskID))
	}
	found, err := ctx.Client.ListIssues(forge.IssueFilter{State: "all", Labels: []string{TaskLabel(phaseID, taskID)}})
	if err != nil {
		return "", Wrap(err)
	}
	var matches []string
	for _, iss := range found {
		if HasMarker(iss) {
			matches = append(matches, iss.Key)
		}
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Strings(matches)
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "warning: %d issues carry %s (%s) — updating %s and leaving the rest\n",
			len(matches), TaskLabel(phaseID, taskID), strings.Join(matches, ", "), matches[0])
	}
	return matches[0], nil
}

// renderTaskBody is what a reader sees on the tracker. It names the phase issue
// so a card opened from a board column can be traced back even where the
// tracker could not express the link.
func RenderTaskBody(phaseID, parent string, t phase.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task `%s` of phase `%s` (%s).\n\n", t.ID, phaseID, parent)
	if t.Title != "" {
		fmt.Fprintf(&b, "**%s**\n\n", t.Title)
	}
	if t.Description != "" {
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(t.Description))
	}
	if len(t.Files) > 0 {
		fmt.Fprintf(&b, "\nFiles: %s\n", strings.Join(t.Files, ", "))
	}
	if len(t.TestContract) > 0 {
		b.WriteString("\nTest contract:\n")
		for _, tc := range t.TestContract {
			fmt.Fprintf(&b, "- %s\n", tc)
		}
	}
	b.WriteString("\n_Mirrored by dross. Edits here do not travel back to plan.toml._\n")
	return b.String()
}

// setBoardState drives whichever workflow field the backend exposes.
//
// One place, so the phase sync and the task sync cannot come to disagree about
// how a state reaches a tracker — which is the class of bug this milestone has
// spent several phases closing.
func setBoardState(ctx *Ctx, key, status string, warn *runWarnings) error {
	switch c := ctx.Client.(type) {
	case *forge.YouTrackClient:
		if err := c.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
	case *forge.JiraClient:
		if err := c.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
	case *forge.AzureBoardsClient:
		if err := c.SetState(key, status, ctx.Proj.Board.StateMap); err != nil {
			return Wrap(err)
		}
	default:
		// Every other backend has no state field: forge REST models an issue
		// as open or closed and nothing else. The status label is already on
		// the issue, which is the honest floor — but a floor that says nothing
		// leaves the board looking authoritative while being partial, which is
		// exactly the failure c-5 exists to prevent. Say it once per run,
		// naming the provider and the value that never reached a column.
		warn.once(&warn.noState, "%s has no workflow state field — %q is carried as a dross label only, so the tracker's columns will not move",
			ctx.Proj.Board.Provider, status)
	}
	return nil
}
